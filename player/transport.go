package player

import (
	"math"

	"github.com/gopxl/beep"
)

const (
	grainSize = 2048
	overlap   = 512
	hop       = grainSize - overlap
)

type sample = [2]float64
type pcmSample [2]float32

type transportStreamer struct {
	buffer      *pcmBuffer
	source      []pcmSample
	totalFrames int
	sourceDone  bool
	buffering   bool
	rate        float64
	speed       float64
	pitch       float64
	volume      float64
	eq          [3]float64
	lowState    [2]float64
	upperState  [2]float64
	sourceBase  float64
	consumed    int
	generated   int
	queue       []sample
	queueOffset int
	pending     []sample
	grainBuf    []sample
	started     bool
	peak        float64
	rms         float64
	channelRMS  [2]float64
	bassRMS     float64
	lowAlpha    float64
	upperAlpha  float64
	analyzer    [analyzerSize]float32
	analyzerPos int
}

func newTransportStreamer(source []pcmSample, sampleRate int) *transportStreamer {
	return newBufferedTransportStreamer(newCompletePCMBuffer(source), sampleRate)
}

func newBufferedTransportStreamer(source *pcmBuffer, sampleRate int) *transportStreamer {
	streamer := &transportStreamer{
		buffer:   source,
		rate:     float64(sampleRate),
		speed:    1,
		pitch:    1,
		volume:   1,
		eq:       [3]float64{1, 1, 1},
		queue:    make([]sample, 0, grainSize),
		pending:  make([]sample, 0, overlap),
		grainBuf: make([]sample, grainSize),
	}
	streamer.setFilterRates()
	return streamer
}

func (s *transportStreamer) Stream(out [][2]float64) (int, bool) {
	s.source, s.totalFrames, s.sourceDone, _ = s.buffer.snapshot()
	s.buffering = false
	written := 0
	peak := 0.0
	sumSquares := 0.0
	channelSquares := [2]float64{}
	bassSquares := 0.0
	frameCount := 0
	for written < len(out) {
		if s.consumed >= s.remainingFrames() {
			s.updateLevels(peak, sumSquares, channelSquares, bassSquares, frameCount)
			return written, false
		}
		if s.queueOffset >= len(s.queue) {
			s.queue = s.queue[:0]
			s.queueOffset = 0
			if !s.generateGrain() {
				s.updateLevels(peak, sumSquares, channelSquares, bassSquares, frameCount)
				if !s.sourceDone {
					s.buffering = true
					for i := written; i < len(out); i++ {
						out[i] = sample{}
					}
					return len(out), true
				}
				return written, false
			}
		}
		for written < len(out) && s.queueOffset < len(s.queue) && s.consumed < s.remainingFrames() {
			in := s.queue[s.queueOffset]
			s.queueOffset++
			for channel := 0; channel < 2; channel++ {
				low := s.lowState[channel] + s.lowAlpha*(in[channel]-s.lowState[channel])
				s.lowState[channel] = low
				upper := s.upperState[channel] + s.upperAlpha*(in[channel]-s.upperState[channel])
				s.upperState[channel] = upper
				high := in[channel] - upper
				mid := upper - low
				value := softLimit((low*s.eq[0] + mid*s.eq[1] + high*s.eq[2]) * s.volume)
				out[written][channel] = value
				channelSquares[channel] += value * value
				bass := low * s.eq[0] * s.volume
				bassSquares += bass * bass
				if abs(value) > peak {
					peak = abs(value)
				}
				sumSquares += value * value
			}
			s.analyzer[s.analyzerPos] = float32((out[written][0] + out[written][1]) * 0.5)
			s.analyzerPos = (s.analyzerPos + 1) % analyzerSize
			written++
			s.consumed++
			frameCount++
		}
	}
	s.updateLevels(peak, sumSquares, channelSquares, bassSquares, frameCount)
	return written, s.consumed < s.remainingFrames()
}

func (s *transportStreamer) updateLevels(peak, sumSquares float64, channelSquares [2]float64, bassSquares float64, frameCount int) {
	s.peak = peak
	s.rms = 0
	s.channelRMS = [2]float64{}
	s.bassRMS = 0
	if frameCount > 0 {
		s.rms = math.Sqrt(sumSquares / float64(frameCount*2))
		s.channelRMS[0] = math.Sqrt(channelSquares[0] / float64(frameCount))
		s.channelRMS[1] = math.Sqrt(channelSquares[1] / float64(frameCount))
		s.bassRMS = math.Sqrt(bassSquares / float64(frameCount*2))
	}
}

func (s *transportStreamer) Err() error { return nil }

func (s *transportStreamer) generateGrain() bool {
	if s.remainingFrames() <= s.generated {
		return false
	}
	nominal := s.sourceBase + float64(s.generated)*s.speed
	if !s.sourceDone {
		needed := nominal + float64(grainSize)*s.pitch
		if s.started {
			needed += 160
		}
		if needed >= float64(len(s.source)) {
			return false
		}
	}
	start := nominal
	if s.started {
		start = s.alignGrain(nominal)
	}
	// Grain playback rate sets pitch; grain-start spacing sets tempo
	// independently. The grain buffer is reused so playback never allocates.
	grain := s.grainBuf
	for i := range grain {
		position := start + float64(i)*s.pitch
		grain[i] = s.interpolate(position)
	}

	if !s.started {
		s.queue = append(s.queue, grain[:hop]...)
		s.pending = append(s.pending[:0], grain[hop:]...)
		s.started = true
	} else {
		for i := 0; i < overlap; i++ {
			mix := float64(i+1) / float64(overlap+1)
			s.queue = append(s.queue, sample{
				s.pending[i][0]*(1-mix) + grain[i][0]*mix,
				s.pending[i][1]*(1-mix) + grain[i][1]*mix,
			})
		}
		s.queue = append(s.queue, grain[overlap:hop]...)
		copy(s.pending, grain[hop:])
	}
	s.generated += hop
	return true
}

func (s *transportStreamer) alignGrain(nominal float64) float64 {
	// Correlating the overlap against nearby source offsets reduces discontinuities at grain joins.
	bestStart := nominal
	bestScore := math.Inf(-1)
	search := 160
	for offset := -search; offset <= search; offset++ {
		candidate := nominal + float64(offset)
		if candidate < 0 || candidate+float64(overlap)*s.pitch >= float64(len(s.source)) {
			continue
		}
		score := 0.0
		for i := 0; i < overlap; i += 4 {
			current := s.interpolate(candidate + float64(i)*s.pitch)
			score += (s.pending[i][0]*current[0] + s.pending[i][1]*current[1])
		}
		if score > bestScore {
			bestScore = score
			bestStart = candidate
		}
	}
	return bestStart
}

func (s *transportStreamer) interpolate(position float64) sample {
	if position < 0 || position >= float64(len(s.source)-1) {
		return sample{}
	}
	index := int(position)
	fraction := position - float64(index)
	first, second := s.source[index], s.source[index+1]
	return sample{
		float64(first[0]) + (float64(second[0])-float64(first[0]))*fraction,
		float64(first[1]) + (float64(second[1])-float64(first[1]))*fraction,
	}
}

func (s *transportStreamer) Configure(speed, pitch float64) {
	position := s.SourcePosition()
	s.speed = clamp(speed, 0.5, 2)
	s.pitch = clamp(pitch, math.Pow(2, -2), math.Pow(2, 2))
	s.sourceBase = position
	s.consumed = 0
	s.generated = 0
	s.queue = nil
	s.queueOffset = 0
	s.pending = nil
	s.started = false
}

func (s *transportStreamer) Seek(seconds float64) {
	s.sourceBase = clamp(seconds*s.rate, 0, float64(s.buffer.totalLength()))
	s.consumed = 0
	s.generated = 0
	s.queue = nil
	s.queueOffset = 0
	s.pending = nil
	s.started = false
	s.lowState = [2]float64{}
	s.upperState = [2]float64{}
}

func (s *transportStreamer) SourcePosition() float64 {
	return clamp(s.sourceBase+float64(s.consumed)*s.speed, 0, float64(s.buffer.totalLength()))
}

func (s *transportStreamer) PositionSeconds() float64 { return s.SourcePosition() / s.rate }

func (s *transportStreamer) DurationSeconds() float64 {
	return float64(s.buffer.totalLength()) / s.rate
}

func (s *transportStreamer) Volume() float64 { return s.volume }

func (s *transportStreamer) SetVolume(value float64) { s.volume = clamp(value, 0, 1) }

func (s *transportStreamer) SetEQ(band int, value float64) {
	if band >= 0 && band < len(s.eq) {
		s.eq[band] = clamp(value, 0, 2)
	}
}

func (s *transportStreamer) Peak() float64 { return s.peak }

func (s *transportStreamer) RMS() float64 { return s.rms }

func (s *transportStreamer) ChannelRMS() [2]float64 { return s.channelRMS }

func (s *transportStreamer) BassRMS() float64 { return s.bassRMS }

// CopyWaveform fills dst with the most recent output samples in chronological
// order for the spectrum analyzer. Stream mutates the ring, so callers must
// hold speaker.Lock().
func (s *transportStreamer) CopyWaveform(dst []float32) {
	n := min(len(dst), analyzerSize)
	start := (s.analyzerPos - n + analyzerSize) % analyzerSize
	for i := 0; i < n; i++ {
		dst[i] = s.analyzer[(start+i)%analyzerSize]
	}
}

func (s *transportStreamer) Done() bool {
	_, totalFrames, done, _ := s.buffer.snapshot()
	return done && s.consumed >= s.remainingFramesFor(totalFrames)
}

func (s *transportStreamer) remainingFrames() int {
	return s.remainingFramesFor(s.totalFrames)
}

func (s *transportStreamer) remainingFramesFor(totalFrames int) int {
	return int(math.Ceil((float64(totalFrames) - s.sourceBase) / s.speed))
}

func (s *transportStreamer) Buffering() bool { return s.buffering }

func (s *transportStreamer) setFilterRates() {
	s.lowAlpha = 1 - math.Exp(-2*math.Pi*250/s.rate)
	s.upperAlpha = 1 - math.Exp(-2*math.Pi*4000/s.rate)
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// softLimit smoothly saturates the output above |threshold| while remaining
// exactly transparent below it. This keeps EQ/volume boosts from hard-clipping
// at the audio device while leaving normal-level signal untouched.
func softLimit(value float64) float64 {
	const threshold = 0.9
	if value > threshold {
		return threshold + (1-threshold)*math.Tanh((value-threshold)/(1-threshold))
	}
	if value < -threshold {
		return -threshold - (1-threshold)*math.Tanh((-value-threshold)/(1-threshold))
	}
	return value
}

func clamp(value, low, high float64) float64 {
	return math.Max(low, math.Min(high, value))
}

var _ beep.Streamer = (*transportStreamer)(nil)
