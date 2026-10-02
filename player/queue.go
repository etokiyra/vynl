package player

import "math"

// queueStreamer is the persistent top-level beep.Streamer for the engine. It
// outlives individual tracks, so the audio device never restarts at a track
// boundary and consecutive tracks can transition gaplessly.
//
// Ownership: the engine owns the queue and every trackStream; the audio
// callback only calls Stream. All queue state is accessed under speaker.Lock —
// the engine mutates from its goroutine, the callback reads and writes during
// Stream, and beep serializes the two. Stream never blocks on a decoder: a
// stream it is done with is appended to retired, and the engine closes it later
// on its own goroutine.
//
// A transition is either gapless (crossfade == 0: promote pending exactly at the
// outgoing track's end) or a crossfade (crossfade > 0: overlap the outgoing and
// incoming streams for crossfade frames with an equal-power envelope).
type queueStreamer struct {
	primary *trackStream
	pending *trackStream
	// fading is the outgoing stream during a crossfade; it is retired once the
	// fade completes.
	fading *trackStream

	// crossfade is the overlap length in output frames (0 disables crossfade).
	// fadePos/fadeTotal track the active overlap.
	crossfade int
	fadePos   int
	fadeTotal int
	scratch   []sample

	// retired holds streams the callback finished with (a promoted primary or a
	// completed fade). The engine drains and closes them; the callback must
	// never join a decoder.
	retired []*trackStream

	// exhausted is set when primary ends with no pending, so the engine can run
	// the repeat/stop/skip logic. The engine clears it when it reconciles.
	exhausted bool
}

func newQueueStreamer() *queueStreamer { return &queueStreamer{} }

// Stream fills out. When the primary reaches end-of-stream and a pending track
// is installed, it promotes the pending stream and keeps filling the same
// buffer, so the join is sample-continuous. It always reports ok=true: the
// streamer must stay installed until the engine removes it (speaker.Clear on
// shutdown), and a false return would make beep drop it permanently.
func (q *queueStreamer) Stream(out [][2]float64) (int, bool) {
	if q.primary == nil {
		// Nothing to play (startup, parked, or shutting down). Stay installed
		// and silent; the engine pauses the control in these states anyway.
		q.cancelFade()
		zeroSamples(out)
		return len(out), true
	}
	if q.fading == nil {
		q.maybeStartFade(len(out))
	}
	if q.fading != nil {
		return q.streamFading(out)
	}
	return q.streamGapless(out)
}

// streamGapless promotes pending at the exact end of the primary.
func (q *queueStreamer) streamGapless(out [][2]float64) (int, bool) {
	written := 0
	for written < len(out) {
		n, ok := q.primary.transport.Stream(out[written:])
		written += n
		if ok {
			return written, true
		}
		if q.pending == nil {
			// Genuine end of playback: pad the rest with silence and let the
			// engine decide whether to stop, repeat, or skip.
			zeroSamples(out[written:])
			q.exhausted = true
			return len(out), true
		}
		q.retire(q.primary)
		q.primary = q.pending
		q.pending = nil
	}
	return written, true
}

// maybeStartFade begins a crossfade when the outgoing track is within the fade
// length of its end.
func (q *queueStreamer) maybeStartFade(outLen int) {
	if q.crossfade <= 0 || q.pending == nil || q.primary == nil {
		return
	}
	remaining := q.primary.transport.framesRemaining()
	if remaining > q.crossfade+outLen {
		return
	}
	incoming := q.pending.transport.framesRemaining()
	fade := q.crossfade
	if remaining < fade {
		fade = remaining
	}
	if incoming < fade {
		fade = incoming
	}
	if fade < 1 {
		fade = 1
	}
	q.fading = q.primary
	q.primary = q.pending
	q.pending = nil
	q.fadePos = 0
	q.fadeTotal = fade
}

// streamFading mixes the incoming primary (fading in) with the outgoing fading
// stream (fading out) using an equal-power envelope.
func (q *queueStreamer) streamFading(out [][2]float64) (int, bool) {
	scratch := q.ensureScratch(len(out))
	primaryN, _ := q.primary.transport.Stream(out)
	fadingN, _ := q.fading.transport.Stream(scratch)

	frames := primaryN
	if fadingN > frames {
		frames = fadingN
	}
	if frames > len(out) {
		frames = len(out)
	}
	for i := 0; i < frames; i++ {
		t := 1.0
		if q.fadeTotal > 0 {
			t = clamp(float64(q.fadePos+i)/float64(q.fadeTotal), 0, 1)
		}
		inGain, outGain := fadeGains(t)
		var mixed sample
		if i < primaryN {
			mixed[0] += out[i][0] * inGain
			mixed[1] += out[i][1] * inGain
		}
		if i < fadingN {
			mixed[0] += scratch[i][0] * outGain
			mixed[1] += scratch[i][1] * outGain
		}
		out[i][0] = softLimit(mixed[0])
		out[i][1] = softLimit(mixed[1])
	}
	// Both transports fill their whole slice (padding with silence on
	// underflow), so a short read here only happens at a track end; silence the
	// remainder rather than leaking stale samples.
	for i := frames; i < len(out); i++ {
		out[i] = sample{}
	}
	q.fadePos += frames
	if q.fadePos >= q.fadeTotal {
		q.finishFade()
	}
	return len(out), true
}

// finishFade retires the outgoing stream and clears the fade state.
func (q *queueStreamer) finishFade() {
	q.retire(q.fading)
	q.fading = nil
	q.fadePos = 0
	q.fadeTotal = 0
}

// cancelFade abandons an in-progress crossfade, retiring the outgoing stream
// immediately. Used when a seek or hard track change must take effect at once.
func (q *queueStreamer) cancelFade() {
	q.retire(q.fading)
	q.fading = nil
	q.fadePos = 0
	q.fadeTotal = 0
}

func (q *queueStreamer) ensureScratch(n int) []sample {
	if cap(q.scratch) < n {
		q.scratch = make([]sample, n)
	}
	return q.scratch[:n]
}

// fadeGains returns the incoming and outgoing gains for a normalized fade
// position t in [0, 1]. The equal-power (sine/cosine) curve keeps the perceived
// loudness constant across the overlap: in^2 + out^2 == 1. The soft limiter
// bounds any peak from correlated material.
func fadeGains(t float64) (in, out float64) {
	angle := clamp(t, 0, 1) * math.Pi / 2
	return math.Sin(angle), math.Cos(angle)
}

// Err always returns nil: decoder errors are surfaced through the ring and
// Status.Err, not by dropping the persistent streamer.
func (q *queueStreamer) Err() error { return nil }

// drainRetired returns the streams the callback finished with and clears the
// list. The caller closes them outside speaker.Lock.
func (q *queueStreamer) drainRetired() []*trackStream {
	retired := q.retired
	q.retired = nil
	return retired
}

// setPrimary installs ts as the current track, retiring the previous primary,
// any pending stream, and any fading stream. Used for hard (non-gapless) track
// changes; the gapless/crossfade transition happens inside Stream.
func (q *queueStreamer) setPrimary(ts *trackStream) {
	q.retire(q.primary)
	q.retire(q.pending)
	q.retire(q.fading)
	q.primary = ts
	q.pending = nil
	q.fading = nil
	q.fadePos = 0
	q.fadeTotal = 0
	q.exhausted = false
}

// setPending installs the prefetched next track, retiring any previous pending.
func (q *queueStreamer) setPending(ts *trackStream) {
	q.retire(q.pending)
	q.pending = ts
}

// clearPending retires the prefetched next track without touching the primary.
func (q *queueStreamer) clearPending() {
	q.retire(q.pending)
	q.pending = nil
}

// clear retires every stream. Used when stopping playback or shutting down.
func (q *queueStreamer) clear() {
	q.retire(q.primary)
	q.retire(q.pending)
	q.retire(q.fading)
	q.primary = nil
	q.pending = nil
	q.fading = nil
	q.fadePos = 0
	q.fadeTotal = 0
	q.exhausted = false
}

func (q *queueStreamer) retire(ts *trackStream) {
	if ts != nil {
		q.retired = append(q.retired, ts)
	}
}

func zeroSamples(out [][2]float64) {
	for i := range out {
		out[i] = sample{}
	}
}
