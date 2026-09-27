package player

import (
	"math"
	"sync"
	"testing"
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

func TestBufferedTransportFreezesPositionDuringUnderflow(t *testing.T) {
	initial := testTone(1024)
	source := newPCMBufferWithInitial(12000, initial)
	streamer := newBufferedTransportStreamer(source, 12000)
	if got := streamer.DurationSeconds(); math.Abs(got-1) > 0.001 {
		t.Fatalf("duration before decode completion = %.3f, want 1 second", got)
	}
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

	if !source.append(testTone(12000 - len(initial))) {
		t.Fatal("failed to append decoded samples")
	}
	n, more = streamer.Stream(output)
	if n != len(output) || !more || streamer.Buffering() {
		t.Fatalf("resumed result = n:%d more:%t buffering:%t", n, more, streamer.Buffering())
	}
	if got := streamer.PositionSeconds(); got <= 0 {
		t.Fatalf("position did not resume after samples arrived: %.3f", got)
	}
}

func TestBufferedTransportSeekWaitsWithoutAdvancing(t *testing.T) {
	initial := testTone(3000)
	source := newPCMBufferWithInitial(12000, initial)
	streamer := newBufferedTransportStreamer(source, 12000)
	streamer.Seek(0.5)
	output := make([]sample, 512)
	streamer.Stream(output)
	if !streamer.Buffering() {
		t.Fatal("seek beyond decoded prefix should report buffering")
	}
	if got := streamer.PositionSeconds(); math.Abs(got-0.5) > 0.001 {
		t.Fatalf("position during seek buffering = %.3f, want 0.5", got)
	}
	if !source.append(testTone(12000 - len(initial))) {
		t.Fatal("failed to append seek target samples")
	}
	streamer.Stream(output)
	if streamer.Buffering() || streamer.PositionSeconds() <= 0.5 {
		t.Fatalf("seek did not resume: buffering:%t position:%.3f", streamer.Buffering(), streamer.PositionSeconds())
	}
}

func TestPCMBufferSupportsConcurrentStreamingAndAppend(t *testing.T) {
	const totalFrames = 131072
	initial := testTone(2048)
	source := newPCMBufferWithInitial(totalFrames, initial)
	streamer := newBufferedTransportStreamer(source, 12000)
	start := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		<-start
		for remaining := totalFrames - len(initial); remaining > 0; {
			chunkSize := min(4096, remaining)
			if !source.append(testTone(chunkSize)) {
				return
			}
			remaining -= chunkSize
		}
		source.finish(nil)
	}()
	close(start)
	output := make([]sample, 257)
	for {
		_, more := streamer.Stream(output)
		if !more {
			break
		}
	}
	writer.Wait()
	if got := streamer.consumed; got != totalFrames {
		t.Fatalf("consumed %d frames after concurrent append, want %d", got, totalFrames)
	}
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
