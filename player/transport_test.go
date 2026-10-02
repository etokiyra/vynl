package player

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestTransportSpeedControlsDuration(t *testing.T) {
	streamer := newTransportStreamer(testTone(12000), 12000)
	streamer.Configure(1.5, 1)

	if got := countSamples(streamer); got != 8000 {
		t.Fatalf("output length = %d, want 8000", got)
	}
}

func TestTransportPitchDoesNotChangeDuration(t *testing.T) {
	streamer := newTransportStreamer(testTone(12000), 12000)
	streamer.Configure(1, 2)

	if got := countSamples(streamer); got != 12000 {
		t.Fatalf("output length = %d, want 12000", got)
	}
}

func TestTransportConfigureKeepsMediaPosition(t *testing.T) {
	streamer := newTransportStreamer(testTone(24000), 12000)
	buffer := make([]sample, 1200)
	streamer.Stream(buffer)
	before := streamer.PositionSeconds()
	streamer.Configure(0.75, math.Pow(2, 1.0/12))

	if after := streamer.PositionSeconds(); math.Abs(after-before) > 0.001 {
		t.Fatalf("position changed from %.3f to %.3f seconds", before, after)
	}
}

func TestTransportRMSMeasuresOutputBuffer(t *testing.T) {
	streamer := newTransportStreamer(testTone(12000), 12000)
	buffer := make([]sample, 1024)
	streamer.Stream(buffer)
	fullLevel := streamer.RMS()
	if fullLevel < 0.6 || fullLevel > 0.8 {
		t.Fatalf("full-volume RMS = %.3f, want around 0.707", fullLevel)
	}

	streamer.SetVolume(0.25)
	streamer.Stream(buffer)
	if got := streamer.RMS(); got < 0.14 || got > 0.21 {
		t.Fatalf("quarter-volume RMS = %.3f, want around 0.177", got)
	}
}

func TestTransportReportsIndependentChannelAndBassLevels(t *testing.T) {
	samples := testTone(12000)
	for i := range samples {
		samples[i][1] *= 0.25
	}
	streamer := newTransportStreamer(samples, 12000)
	buffer := make([]sample, 1024)
	streamer.Stream(buffer)
	channels := streamer.ChannelRMS()
	if channels[0] < channels[1]*3.5 || channels[0] > channels[1]*4.5 {
		t.Fatalf("channel RMS = %.3f/%.3f, want about 4:1", channels[0], channels[1])
	}
	if bass := streamer.BassRMS(); bass <= 0 || bass >= streamer.RMS() {
		t.Fatalf("bass RMS = %.3f, total RMS = %.3f; expected a nonzero low-band level below total", bass, streamer.RMS())
	}
}

func TestStreamingTransportPlaysUnknownLengthContainer(t *testing.T) {
	ring := ringTestRing()
	if !ring.tryWrite(testTone(4096)) {
		t.Fatal("initial write rejected")
	}
	streamer := newStreamingTransport(ring, 12000)
	output := make([]sample, 512)
	if n, more := streamer.Stream(output); n != len(output) || !more || streamer.Buffering() {
		t.Fatalf("unknown-length stream returned n=%d more=%t buffering=%t, want full output", n, more, streamer.Buffering())
	}
	ring.finish(nil)
	if got := streamer.DurationSeconds(); got <= 0 {
		t.Fatalf("duration after finish = %.3f, want positive", got)
	}
}

func TestStreamingTransportBuffersWhenProducerIsBehind(t *testing.T) {
	ring := ringTestRing()
	ring.setTotal(12000)
	if !ring.tryWrite(testTone(1024)) {
		t.Fatal("initial write rejected")
	}
	streamer := newStreamingTransport(ring, 12000)
	output := make([]sample, 512)
	n, more := streamer.Stream(output)
	if n != len(output) || !more || !streamer.Buffering() {
		t.Fatalf("underflow result = n:%d more:%t buffering:%t", n, more, streamer.Buffering())
	}
	for i, frame := range output {
		if frame != (sample{}) {
			t.Fatalf("underflow output frame %d is not silence: %v", i, frame)
		}
	}
	if got := streamer.PositionSeconds(); got != 0 {
		t.Fatalf("position advanced during silence to %.3f seconds", got)
	}

	if !ring.tryWrite(testTone(12000 - 1024)) {
		t.Fatal("failed to append decoded samples")
	}
	ring.finish(nil)
	n, more = streamer.Stream(output)
	if n != len(output) || !more || streamer.Buffering() {
		t.Fatalf("resumed result = n:%d more:%t buffering:%t", n, more, streamer.Buffering())
	}
	if got := streamer.PositionSeconds(); got <= 0 {
		t.Fatalf("position did not resume after samples arrived: %.3f", got)
	}
}

func TestStreamingTransportSeekWaitsForDecoder(t *testing.T) {
	ring := ringTestRing()
	ring.setTotal(12000)
	if !ring.tryWrite(testTone(3000)) {
		t.Fatal("initial write rejected")
	}
	streamer := newStreamingTransport(ring, 12000)
	streamer.Seek(0.5) // output frame 6000, beyond decoded data
	output := make([]sample, 512)
	streamer.Stream(output)
	if !streamer.Buffering() {
		t.Fatal("seek beyond decoded data should report buffering")
	}
	if got := streamer.PositionSeconds(); math.Abs(got-0.5) > 0.001 {
		t.Fatalf("position during seek buffering = %.3f, want 0.5", got)
	}
	gen, frame, ok := ring.pendingSeek()
	if !ok || frame != 6000 {
		t.Fatalf("pending seek = (%d, %d, %t), want frame 6000", gen, frame, ok)
	}
	ring.commitSeek(gen, frame)
	if !ring.tryWrite(testTone(12000 - 6000)) {
		t.Fatal("failed to append seek target samples")
	}
	ring.finish(nil)
	streamer.Stream(output)
	if streamer.Buffering() || streamer.PositionSeconds() <= 0.5 {
		t.Fatalf("seek did not resume: buffering:%t position:%.3f", streamer.Buffering(), streamer.PositionSeconds())
	}
}

func TestStreamingTransportConsumesRingWithoutLoss(t *testing.T) {
	const totalFrames = 131072
	ring := ringTestRing()
	ring.setTotal(totalFrames)
	initial := testTone(2048)
	if !ring.tryWrite(initial) {
		t.Fatal("initial write rejected")
	}
	streamer := newStreamingTransport(ring, 12000)
	cancel := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		for remaining := totalFrames - len(initial); remaining > 0; {
			chunkSize := min(ringChunkFrames, remaining)
			chunk := testTone(chunkSize)
			for !ring.tryWrite(chunk) {
				if !ring.wait(cancel) {
					return
				}
			}
			remaining -= chunkSize
		}
		ring.finish(nil)
	}()

	output := make([]sample, 257)
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, more := streamer.Stream(output)
		if !more {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out consuming the ring")
		}
	}
	close(cancel)
	producer.Wait()
	if got := streamer.consumed; got != totalFrames {
		t.Fatalf("consumed %d frames after streaming, want %d", got, totalFrames)
	}
}

func TestSoftLimitIsTransparentAndBounded(t *testing.T) {
	for _, value := range []float64{0, 0.1, -0.1, 0.5, -0.5, 0.89, -0.89, 0.9, -0.9} {
		if got := softLimit(value); got != value {
			t.Fatalf("softLimit(%+.2f) = %.6f, want identity", value, got)
		}
	}
	for _, value := range []float64{0.95, 1, 2, 10, -1, -2, -10} {
		got := softLimit(value)
		if math.Abs(got) > 1 {
			t.Fatalf("softLimit(%+.2f) = %.6f, want magnitude at most 1", value, got)
		}
		if math.Signbit(got) != math.Signbit(value) {
			t.Fatalf("softLimit(%+.2f) = %.6f changed sign", value, got)
		}
	}
	if got := softLimit(0.91); got <= 0.9 || got > 0.91 {
		t.Fatalf("softLimit(0.91) = %.6f, want just above the threshold", got)
	}
}

func TestTransportLimitsClipping(t *testing.T) {
	streamer := newTransportStreamer(testTone(12000), 12000)
	streamer.SetEQ(0, 2)
	streamer.SetEQ(1, 2)
	streamer.SetEQ(2, 2)
	streamer.SetVolume(1)
	output := make([]sample, 1024)
	streamer.Stream(output)
	for i, frame := range output {
		if math.Abs(frame[0]) > 1 || math.Abs(frame[1]) > 1 {
			t.Fatalf("output frame %d exceeded the limiter: %v", i, frame)
		}
	}
	if streamer.Peak() > 1 {
		t.Fatalf("reported peak = %.6f, want at most 1", streamer.Peak())
	}
}

// TestCrossoverFiltersSeparateBands checks the 3-way Linkwitz-Riley crossover
// actually splits the spectrum: the low band passes bass and rejects treble,
// and the high band does the opposite. The unity reconstruction identity
// (low + (in-low-high) + high == in) is exercised by the RMS tests.
func TestCrossoverFiltersSeparateBands(t *testing.T) {
	const rate = 48000
	streamer := newTransportStreamer(testTone(8), rate)
	lowAt := func(freq float64) float64 { return crossoverGain(streamer.processLow, freq, rate) }
	highAt := func(freq float64) float64 { return crossoverGain(streamer.processHigh, freq, rate) }

	if gain := lowAt(100); gain < 0.95 {
		t.Fatalf("low band gain at 100 Hz = %.3f, want ~1", gain)
	}
	if gain := lowAt(10000); gain > 0.05 {
		t.Fatalf("low band gain at 10 kHz = %.3f, want ~0", gain)
	}
	if gain := highAt(10000); gain < 0.95 {
		t.Fatalf("high band gain at 10 kHz = %.3f, want ~1", gain)
	}
	if gain := highAt(100); gain > 0.05 {
		t.Fatalf("high band gain at 100 Hz = %.3f, want ~0", gain)
	}
}

// crossoverGain measures the steady-state RMS gain of one filter channel,
// skipping the first half of the run to avoid the filter transient.
func crossoverGain(process func(int, float64) float64, freq, rate float64) float64 {
	const samples = 48000
	var inSquares, outSquares float64
	for i := 0; i < samples; i++ {
		x := math.Sin(2 * math.Pi * freq * float64(i) / rate)
		y := process(0, x)
		if i >= samples/2 {
			inSquares += x * x
			outSquares += y * y
		}
	}
	return math.Sqrt(outSquares / inSquares)
}

func TestTransportConfigureKeepsEQFilterState(t *testing.T) {
	samples := make([]pcmSample, 12000)
	for i := range samples {
		samples[i] = pcmSample{0.3, 0.3}
	}
	streamer := newTransportStreamer(samples, 12000)
	streamer.SetEQ(0, 2) // boost the low band so stale filter state is visible
	output := make([]sample, 512)
	streamer.Stream(output)

	streamer.Configure(1, 1)
	n, _ := streamer.Stream(output)
	if n == 0 {
		t.Fatal("no output after Configure")
	}
	if output[0][0] < 0.55 {
		t.Fatalf("filter state reset on Configure: first sample %.3f, want about 0.6", output[0][0])
	}
}

func TestTransportStreamsWithoutAllocating(t *testing.T) {
	streamer := newTransportStreamer(testTone(200000), 12000)
	output := make([]sample, 512)
	streamer.Stream(output) // warm the queue/pending/grain buffers
	if allocs := testing.AllocsPerRun(200, func() {
		streamer.Stream(output)
	}); allocs != 0 {
		t.Fatalf("Stream allocated %.2f times per call in steady state, want 0", allocs)
	}
}

// TestStreamingTransportStreamsWithoutAllocating runs the same allocation check
// on the producer-backed ring path, where the window copy must also stay
// allocation-free.
func TestStreamingTransportStreamsWithoutAllocating(t *testing.T) {
	ring := ringTestRing()
	ring.setTotal(1 << 30)
	if !ring.tryWrite(makeRamp(0, ringTestCapacity)) {
		t.Fatal("initial write rejected")
	}
	cancel := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		pos := int64(ringTestCapacity)
		chunk := make([]pcmSample, ringChunkFrames)
		for {
			for i := range chunk {
				chunk[i] = rampFrame(pos + int64(i))
			}
			for !ring.tryWrite(chunk) {
				if !ring.wait(cancel) {
					return
				}
			}
			pos += int64(len(chunk))
		}
	}()

	streamer := newStreamingTransport(ring, 12000)
	output := make([]sample, 512)
	streamer.Stream(output) // warm the window scratch and grain buffers
	if allocs := testing.AllocsPerRun(200, func() {
		streamer.Stream(output)
	}); allocs != 0 {
		t.Fatalf("ring-backed Stream allocated %.2f times per call, want 0", allocs)
	}
	close(cancel)
	producer.Wait()
}

func testTone(length int) []pcmSample {
	samples := make([]pcmSample, length)
	for i := range samples {
		value := math.Sin(2 * math.Pi * 220 * float64(i) / 12000)
		samples[i] = pcmSample{float32(value), float32(value)}
	}
	return samples
}

func countSamples(streamer *transportStreamer) int {
	total := 0
	buffer := make([]sample, 257)
	for {
		n, ok := streamer.Stream(buffer)
		total += n
		if !ok {
			return total
		}
	}
}
