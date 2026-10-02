package player

import (
	"math"

	"github.com/gopxl/beep"
)

const (
	grainSize = 2048
	overlap   = 512
	hop       = grainSize - overlap

	// ringWindowFrames bounds the per-call source copy. It covers the playback
	// callback (2048 output frames) plus grain/pitch worst cases (~19k frames);
	// larger test buffers grow the scratch once, outside steady state.
	ringWindowFrames = 32 * 1024
)

type sample = [2]float64
type pcmSample [2]float32

type transportStreamer struct {
	ring          *pcmRing
	source        []pcmSample // contiguous window of the ring covering this call
	windowBase    int64       // absolute frame of source[0]
	windowScratch []pcmSample // preallocated destination for the window copy
	ringBlocked   bool        // ring could not serve the window this call
	totalFrames   int
	sourceDone    bool
	buffering     bool
	rate          float64
	speed         float64
	pitch         float64
	volume        float64
	eq            [3]float64
	lowStages     [2]biquad
	highStages    [2]biquad
	lowZ          [2][4]float64
	highZ         [2][4]float64
	sourceBase    float64
	consumed      int
	generated     int
	queue         []sample
	queueOffset   int
	pending       []sample
	grainBuf      []sample
	started       bool
	peak          float64
	rms           float64
	channelRMS    [2]float64
	bassRMS       float64
	analyzer      [analyzerSize]float32
	analyzerPos   int
}

func newTransportStreamer(source []pcmSample, sampleRate int) *transportStreamer {
	return newStreamingTransport(newCompletePCMRing(source), sampleRate)
}

func newStreamingTransport(source *pcmRing, sampleRate int) *transportStreamer {
	streamer := &transportStreamer{
		ring:          source,
		rate:          float64(sampleRate),
		speed:         1,
		pitch:         1,
		volume:        1,
		eq:            [3]float64{1, 1, 1},
		queue:         make([]sample, 0, grainSize),
		pending:       make([]sample, 0, overlap),
		grainBuf:      make([]sample, grainSize),
		windowScratch: make([]pcmSample, ringWindowFrames),
	}
	streamer.setFilterRates()
	return streamer
}

func (s *transportStreamer) Stream(out [][2]float64) (int, bool) {
	s.refreshSource(len(out))
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
				if s.needsMoreData() {
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
				low := s.processLow(channel, in[channel])
				high := s.processHigh(channel, in[channel])
				mid := in[channel] - low - high
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
	if s.ringBlocked {
		return false
	}
	nominal := s.sourceBase + float64(s.generated)*s.speed
	// The ring window must cover the whole grain before it is generated. When
	// the producer has finished, the window is pinned to the track end and
	// interpolate handles the final partial grain with silence, as before.
	needed := nominal + float64(grainSize)*s.pitch
	if s.started {
		needed += ringMargin
	}
	if !s.sourceDone && needed >= float64(s.windowEnd()) {
		return false
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
	search := ringMargin
	for offset := -search; offset <= search; offset++ {
		candidate := nominal + float64(offset)
		if candidate < float64(s.windowBase) || candidate+float64(overlap)*s.pitch >= float64(s.windowEnd()) {
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
	index := int(position) - int(s.windowBase)
	if index < 0 || index+1 >= len(s.source) {
		return sample{}
	}
	fraction := position - float64(int(position))
	first, second := s.source[index], s.source[index+1]
	return sample{
		float64(first[0]) + (float64(second[0])-float64(first[0]))*fraction,
		float64(first[1]) + (float64(second[1])-float64(first[1]))*fraction,
	}
}

// refreshSource publishes the consumer position and fetches the contiguous ring
// window covering every grain this Stream call may generate.
func (s *transportStreamer) refreshSource(outLen int) {
	nominal := s.sourceBase + float64(s.generated)*s.speed
	lo := int64(math.Floor(nominal))
	if s.started {
		lo -= ringMargin
	}
	if lo < 0 {
		lo = 0
	}
	span := int64(math.Ceil(float64(outLen+2*hop)*s.speed)) + ringAheadWindow
	scratch := s.windowScratch
	if span > int64(len(scratch)) {
		scratch = make([]pcmSample, span)
		s.windowScratch = scratch
	}
	view := s.ring.window(lo, lo+span, scratch)
	s.totalFrames = int(view.total)
	s.sourceDone = view.finished
	if !view.ready {
		s.source = nil
		s.windowBase = lo
		s.ringBlocked = true
		return
	}
	s.ringBlocked = false
	s.source = view.samples
	s.windowBase = view.base
}

// needsMoreData distinguishes "wait for the producer" from the real end of the
// track. A blocked ring (seek rewrite in flight, or evicted data) takes the
// same buffering path as a producer that has not decoded far enough yet.
func (s *transportStreamer) needsMoreData() bool {
	return s.ringBlocked || !s.sourceDone
}

func (s *transportStreamer) windowEnd() int64 {
	return s.windowBase + int64(len(s.source))
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
	s.ring.ensure(int64(math.Floor(position)))
}

func (s *transportStreamer) Seek(seconds float64) {
	target := seconds * s.rate
	if _, total := s.ring.status(); total > 0 {
		target = clamp(target, 0, float64(total))
	} else if target < 0 {
		target = 0
	}
	s.sourceBase = target
	s.consumed = 0
	s.generated = 0
	s.queue = nil
	s.queueOffset = 0
	s.pending = nil
	s.started = false
	s.lowZ = [2][4]float64{}
	s.highZ = [2][4]float64{}
	s.ring.ensure(int64(math.Floor(target)))
}

func (s *transportStreamer) SourcePosition() float64 {
	position := s.sourceBase + float64(s.consumed)*s.speed
	if _, total := s.ring.status(); total > 0 && position > float64(total) {
		position = float64(total)
	}
	if position < 0 {
		position = 0
	}
	return position
}

func (s *transportStreamer) PositionSeconds() float64 { return s.SourcePosition() / s.rate }

func (s *transportStreamer) DurationSeconds() float64 {
	_, total := s.ring.status()
	return float64(total) / s.rate
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
	finished, total := s.ring.status()
	if !finished {
		return false
	}
	return s.consumed >= s.remainingFramesFor(int(total))
}

func (s *transportStreamer) remainingFrames() int {
	return s.remainingFramesFor(s.totalFrames)
}

// remainingFramesFor treats an unknown length (0) as unbounded so a container
// that does not report its length still plays until the producer finishes.
func (s *transportStreamer) remainingFramesFor(totalFrames int) int {
	if totalFrames <= 0 || float64(totalFrames) <= s.sourceBase {
		if totalFrames <= 0 {
			return math.MaxInt32
		}
		return 0
	}
	return int(math.Ceil((float64(totalFrames) - s.sourceBase) / s.speed))
}

func (s *transportStreamer) Buffering() bool { return s.buffering }

// setFilterRates builds the fixed 3-way crossover filters. LOW and HIGH are
// each Linkwitz-Riley 4th-order (two cascaded Butterworth biquad sections) so
// the bands separate steeply while still summing back to the input at unity
// gain: low + (in-low-high) + high == in exactly.
func (s *transportStreamer) setFilterRates() {
	low := butterworthLowPass(250, s.rate)
	s.lowStages = [2]biquad{low, low}
	high := butterworthHighPass(4000, s.rate)
	s.highStages = [2]biquad{high, high}
}

// processLow runs one sample through the cascaded low-pass sections for channel.
func (s *transportStreamer) processLow(channel int, x float64) float64 {
	z := &s.lowZ[channel]
	x = s.lowStages[0].process(x, &z[0], &z[1])
	return s.lowStages[1].process(x, &z[2], &z[3])
}

// processHigh runs one sample through the cascaded high-pass sections.
func (s *transportStreamer) processHigh(channel int, x float64) float64 {
	z := &s.highZ[channel]
	x = s.highStages[0].process(x, &z[0], &z[1])
	return s.highStages[1].process(x, &z[2], &z[3])
}

// biquad is a normalized second-order section in direct form II transposed. a1
// and a2 are already divided by a0.
type biquad struct {
	b0, b1, b2, a1, a2 float64
}

func (b biquad) process(x float64, z1, z2 *float64) float64 {
	y := b.b0*x + *z1
	*z1 = b.b1*x - b.a1*y + *z2
	*z2 = b.b2*x - b.a2*y
	return y
}

// butterworthLowPass returns a normalized 2nd-order Butterworth low-pass at
// frequency (Q = 1/sqrt(2), so 2Q = sqrt(2)).
func butterworthLowPass(frequency, rate float64) biquad {
	omega := 2 * math.Pi * frequency / rate
	cosine, sine := math.Cos(omega), math.Sin(omega)
	alpha := sine / math.Sqrt2
	b0 := (1 - cosine) / 2
	b1 := 1 - cosine
	b2 := b0
	a0 := 1 + alpha
	a1 := -2 * cosine
	a2 := 1 - alpha
	return biquad{b0 / a0, b1 / a0, b2 / a0, a1 / a0, a2 / a0}
}

// butterworthHighPass returns a normalized 2nd-order Butterworth high-pass.
func butterworthHighPass(frequency, rate float64) biquad {
	omega := 2 * math.Pi * frequency / rate
	cosine, sine := math.Cos(omega), math.Sin(omega)
	alpha := sine / math.Sqrt2
	b0 := (1 + cosine) / 2
	b1 := -(1 + cosine)
	b2 := b0
	a0 := 1 + alpha
	a1 := -2 * cosine
	a2 := 1 - alpha
	return biquad{b0 / a0, b1 / a0, b2 / a0, a1 / a0, a2 / a0}
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
