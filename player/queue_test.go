package player

import (
	"math"
	"sync"
	"testing"

	"github.com/gopxl/beep/speaker"
)

func constantPCM(value float32, n int) []pcmSample {
	samples := make([]pcmSample, n)
	for i := range samples {
		samples[i] = pcmSample{value, value}
	}
	return samples
}

// staticTrackStream builds a stream backed by an already-decoded, complete ring
// so the queue can be tested without a decoder or an audio device.
func staticTrackStream(index int, samples []pcmSample) *trackStream {
	ring := newCompletePCMRing(samples)
	return &trackStream{
		index:     index,
		path:      "static",
		ring:      ring,
		transport: newStreamingTransport(ring, outputRate),
	}
}

// collectFrames streams the queue in chunk-sized calls until it has produced
// total frames, returning them (any exhaustion padding is trimmed away).
func collectFrames(t *testing.T, queue *queueStreamer, chunk, total int) []sample {
	t.Helper()
	out := make([]sample, chunk)
	got := make([]sample, 0, total)
	for len(got) < total {
		n, ok := queue.Stream(out)
		if !ok {
			t.Fatal("queue returned ok=false; the persistent streamer must stay installed")
		}
		got = append(got, out[:n]...)
		if len(got) > total+2*chunk {
			t.Fatalf("queue produced %d frames, expected about %d", len(got), total)
		}
	}
	return got[:total]
}

// TestQueueStreamerPromotesGaplessly is the core gapless guarantee: when the
// primary ends and a pending stream is installed, the output continues with the
// pending stream in the same call, with no inserted silence and no gap.
func TestQueueStreamerPromotesGaplessly(t *testing.T) {
	first := staticTrackStream(0, constantPCM(0.5, 1000))
	second := staticTrackStream(1, constantPCM(0.25, 1000))
	queue := newQueueStreamer()
	queue.setPrimary(first)
	queue.setPending(second)

	got := collectFrames(t, queue, 256, 2000)
	for i := 0; i < 1000; i++ {
		if math.Abs(got[i][0]-0.5) > 1e-6 {
			t.Fatalf("frame %d = %v, want 0.5 (first track)", i, got[i][0])
		}
	}
	for i := 1000; i < 2000; i++ {
		if math.Abs(got[i][0]-0.25) > 1e-6 {
			t.Fatalf("frame %d = %v, want 0.25 (promoted track)", i, got[i][0])
		}
	}
	if queue.primary != second {
		t.Fatal("pending was not promoted to primary")
	}
	if queue.pending != nil {
		t.Fatal("pending not cleared after promotion")
	}
	if len(queue.retired) != 1 || queue.retired[0] != first {
		t.Fatalf("retired = %v, want exactly the first stream", queue.retired)
	}
}

// TestQueueStreamerMultipleTransitions promotes twice in a row and checks both
// the audio content and the retired list.
func TestQueueStreamerMultipleTransitions(t *testing.T) {
	queue := newQueueStreamer()
	first := staticTrackStream(0, constantPCM(0.1, 300))
	second := staticTrackStream(1, constantPCM(0.2, 300))
	third := staticTrackStream(2, constantPCM(0.3, 300))
	queue.setPrimary(first)
	queue.setPending(second)

	got := collectFrames(t, queue, 64, 600)
	queue.setPending(third)
	got = append(got, collectFrames(t, queue, 64, 300)...)

	segments := []struct {
		start, end int
		want       float64
	}{{0, 300, 0.1}, {300, 600, 0.2}, {600, 900, 0.3}}
	for _, segment := range segments {
		for i := segment.start; i < segment.end; i++ {
			if math.Abs(got[i][0]-segment.want) > 1e-6 {
				t.Fatalf("frame %d = %v, want %v", i, got[i][0], segment.want)
			}
		}
	}
	// The first transition retired `first`; the second retired `second`.
	if len(queue.retired) != 2 || queue.retired[0] != first || queue.retired[1] != second {
		t.Fatalf("retired = %v, want [first second]", queue.retired)
	}
	if queue.primary != third {
		t.Fatal("third stream did not become primary")
	}
}

func TestQueueStreamerExhaustsWithoutPending(t *testing.T) {
	queue := newQueueStreamer()
	queue.setPrimary(staticTrackStream(0, constantPCM(0.5, 400)))
	collectFrames(t, queue, 128, 400)

	out := make([]sample, 128)
	n, ok := queue.Stream(out)
	if !ok || n != len(out) {
		t.Fatalf("exhausted Stream = (%d, %t), want (%d, true)", n, ok, len(out))
	}
	if !queue.exhausted {
		t.Fatal("queue did not report exhaustion with no pending")
	}
	for i, frame := range out {
		if frame != (sample{}) {
			t.Fatalf("padding frame %d = %v, want silence", i, frame)
		}
	}
}

func TestQueueStreamerClearRetiresEverything(t *testing.T) {
	queue := newQueueStreamer()
	primary := staticTrackStream(0, constantPCM(0.1, 10))
	pending := staticTrackStream(1, constantPCM(0.2, 10))
	queue.setPrimary(primary)
	queue.setPending(pending)

	queue.clear()
	if queue.primary != nil || queue.pending != nil || queue.exhausted {
		t.Fatalf("clear left queue state: primary %v pending %v exhausted %t",
			queue.primary, queue.pending, queue.exhausted)
	}
	if len(queue.retired) != 2 {
		t.Fatalf("clear retired %d streams, want 2", len(queue.retired))
	}
	if drained := queue.drainRetired(); len(drained) != 2 || queue.retired != nil {
		t.Fatalf("drainRetired = %v, retained %v", drained, queue.retired)
	}
}

// TestQueueStreamerEmptyIsSilent checks the persistent streamer is safe before
// any track is installed: it must output silence and stay installed.
func TestQueueStreamerEmptyIsSilent(t *testing.T) {
	queue := newQueueStreamer()
	out := make([]sample, 64)
	n, ok := queue.Stream(out)
	if !ok || n != len(out) {
		t.Fatalf("empty queue Stream = (%d, %t), want (%d, true)", n, ok, len(out))
	}
	for i, frame := range out {
		if frame != (sample{}) {
			t.Fatalf("frame %d = %v, want silence", i, frame)
		}
	}
}

// TestFadeGainsEqualPower checks the envelope endpoints and the constant-power
// identity used by the crossfade mixer.
func TestFadeGainsEqualPower(t *testing.T) {
	if in, out := fadeGains(0); in != 0 || out != 1 {
		t.Fatalf("fadeGains(0) = (%v, %v), want (0, 1)", in, out)
	}
	if in, out := fadeGains(1); math.Abs(in-1) > 1e-12 || math.Abs(out) > 1e-12 {
		t.Fatalf("fadeGains(1) = (%v, %v), want (1, 0)", in, out)
	}
	for step := 0; step <= 10; step++ {
		in, out := fadeGains(float64(step) / 10)
		if math.Abs(in*in+out*out-1) > 1e-9 {
			t.Fatalf("fadeGains(%.1f) is not equal-power: %v^2 + %v^2", float64(step)/10, in, out)
		}
	}
}

// TestQueueStreamerCrossfadeEnvelope fades the whole outgoing track into the
// incoming one and checks every frame against the equal-power envelope.
func TestQueueStreamerCrossfadeEnvelope(t *testing.T) {
	const total = 4000
	queue := newQueueStreamer()
	queue.crossfade = total
	outgoing := staticTrackStream(0, constantPCM(0.5, total))
	incoming := staticTrackStream(1, constantPCM(0.25, total))
	queue.setPrimary(outgoing)
	queue.setPending(incoming)

	got := collectFrames(t, queue, 256, total)
	for i := 0; i < total; i++ {
		tPos := float64(i) / float64(total)
		want := 0.5*math.Cos(tPos*math.Pi/2) + 0.25*math.Sin(tPos*math.Pi/2)
		if math.Abs(got[i][0]-want) > 1e-6 {
			t.Fatalf("crossfade frame %d = %.7f, want %.7f", i, got[i][0], want)
		}
	}
	if queue.fading != nil {
		t.Fatal("fade did not finish")
	}
	if queue.primary != incoming {
		t.Fatal("incoming stream is not primary after the fade")
	}
	if len(queue.retired) != 1 || queue.retired[0] != outgoing {
		t.Fatalf("retired = %v, want the outgoing stream", queue.retired)
	}
}

// TestQueueStreamerCrossfadeStartsBeforeEnd checks the common case: a short
// crossfade overlaying the end of a longer outgoing track, with the incoming
// continuing past the fade. The join must be continuous (no step change).
func TestQueueStreamerCrossfadeStartsBeforeEnd(t *testing.T) {
	const fade = 1000
	queue := newQueueStreamer()
	queue.crossfade = fade
	queue.setPrimary(staticTrackStream(0, constantPCM(0.5, 5000)))
	queue.setPending(staticTrackStream(1, constantPCM(0.25, 5000)))

	got := collectFrames(t, queue, 256, 8000)
	if math.Abs(got[0][0]-0.5) > 1e-6 {
		t.Fatalf("first frame = %v, want pure outgoing 0.5", got[0][0])
	}
	if math.Abs(got[len(got)-1][0]-0.25) > 1e-6 {
		t.Fatalf("last frame = %v, want pure incoming 0.25", got[len(got)-1][0])
	}
	for i := 1; i < len(got); i++ {
		if math.Abs(got[i][0]-got[i-1][0]) > 0.01 {
			t.Fatalf("crossfade discontinuity at frame %d: %.5f -> %.5f", i, got[i-1][0], got[i][0])
		}
	}
	if len(queue.retired) < 1 {
		t.Fatal("outgoing stream was not retired after the fade")
	}
}

// TestQueueStreamerCrossfadeShortIncoming caps the fade to the incoming track's
// length so a very short incoming track is not outrun by the envelope.
func TestQueueStreamerCrossfadeShortIncoming(t *testing.T) {
	queue := newQueueStreamer()
	queue.crossfade = 4000
	queue.setPrimary(staticTrackStream(0, constantPCM(0.5, 4000)))
	queue.setPending(staticTrackStream(1, constantPCM(0.25, 200)))

	got := collectFrames(t, queue, 64, 200)
	// The fade is clamped to the incoming length, so the output is a complete
	// envelope from 0.5 to 0.25 across the 200 incoming frames.
	if math.Abs(got[0][0]-0.5) > 1e-6 {
		t.Fatalf("first frame = %v, want 0.5", got[0][0])
	}
	if math.Abs(got[199][0]-0.25) > 0.01 {
		t.Fatalf("last frame = %v, want about 0.25 (fade capped to the incoming length)", got[199][0])
	}
}

// TestQueueStreamerCancelFade retires the outgoing stream immediately.
func TestQueueStreamerCancelFade(t *testing.T) {
	queue := newQueueStreamer()
	queue.crossfade = 2000
	outgoing := staticTrackStream(0, constantPCM(0.5, 4000))
	incoming := staticTrackStream(1, constantPCM(0.25, 4000))
	queue.setPrimary(outgoing)
	queue.setPending(incoming)

	collectFrames(t, queue, 64, 100) // not far enough to start the fade
	// Force a fade, then cancel it.
	queue.crossfade = 4000
	queue.maybeStartFade(64)
	if queue.fading == nil {
		t.Fatal("fade did not start")
	}
	queue.cancelFade()
	if queue.fading != nil {
		t.Fatal("cancelFade left a fading stream")
	}
	if queue.primary != incoming {
		t.Fatal("cancelFade changed the primary")
	}
	if len(queue.retired) == 0 || queue.retired[len(queue.retired)-1] != outgoing {
		t.Fatalf("cancelFade did not retire the outgoing stream: %v", queue.retired)
	}
}

// TestQueueStreamerCrossfadeNoClipping drives a correlated full-scale overlap and
// checks the mixer never exceeds the soft limiter's bound.
func TestQueueStreamerCrossfadeNoClipping(t *testing.T) {
	queue := newQueueStreamer()
	queue.crossfade = 2000
	queue.setPrimary(staticTrackStream(0, constantPCM(1.0, 4000)))
	queue.setPending(staticTrackStream(1, constantPCM(1.0, 4000)))

	got := collectFrames(t, queue, 256, 3000)
	for i, frame := range got {
		if math.Abs(frame[0]) > 1 || math.Abs(frame[1]) > 1 {
			t.Fatalf("crossfade frame %d = %v, exceeds full scale", i, frame)
		}
	}
}

// TestQueueStreamerConcurrentMutationIsRaceFree runs the audio callback (under
// speaker.Lock, as beep does) concurrently with the engine-style queue
// mutations. Under -race this validates the single-lock discipline that guards
// primary/pending/retired. Static streams keep it fast: no decoders or I/O.
func TestQueueStreamerConcurrentMutationIsRaceFree(t *testing.T) {
	queue := newQueueStreamer()
	queue.setPrimary(staticTrackStream(0, constantPCM(0.1, 4096)))

	stop := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		out := make([]sample, 512)
		for {
			select {
			case <-stop:
				return
			default:
			}
			speaker.Lock()
			queue.Stream(out)
			speaker.Unlock()
		}
	}()

	for i := 0; i < 1000; i++ {
		speaker.Lock()
		if queue.primary == nil {
			queue.setPrimary(staticTrackStream(0, constantPCM(0.1, 4096)))
		}
		if queue.pending == nil {
			queue.setPending(staticTrackStream(1, constantPCM(0.2, 4096)))
		}
		queue.drainRetired()
		speaker.Unlock()
	}
	close(stop)
	group.Wait()
}
