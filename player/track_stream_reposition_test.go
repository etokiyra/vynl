package player

import (
	"testing"
	"time"
)

// These tests pin the decode-ahead reposition: a forward consumer seek within
// the ring's capacity is served by the sequential decoder racing to cover the
// target, NOT by a decoder Seek and window reset. Repositioning decoders is
// not always cheap — a FLAC without a seek table decodes the entire stream
// inside its Seek just to index frames (seconds for a whole album-length
// file, on every fresh track decoder) — so vynl avoids it for the common case.

// TestForwardSeekRepositionsViaDecodeAhead drives the real producer pipeline:
// after a forward seek the ring window must stay continuous (base and length
// intact) while the producer decodes to cover the target, and playback must
// resume past the target.
func TestForwardSeekRepositionsViaDecodeAhead(t *testing.T) {
	stream, err := openTrackStream(0, writeTestWAV(t, 60, 44100))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()

	out := make([]sample, 2048)
	for i := 0; i < 16; i++ {
		stream.transport.Stream(out)
	}
	ring := stream.ring
	ring.mu.Lock()
	baseBefore, lengthBefore := ring.base, ring.length
	ring.mu.Unlock()

	seeks := stream.transport.PositionSeconds() + 5
	stream.transport.Seek(seeks)

	// The producer must ack the seek with the window intact and the extent at
	// or beyond the target. Poll with a deadline; a wedge (e.g. buffering on a
	// repositioning seek-table rebuild) fails the deadline.
	deadline := time.Now().Add(10 * time.Second)
	acked, continuous, covered := false, false, false
	for time.Now().Before(deadline) {
		if _, more := stream.transport.Stream(out); !more {
			break
		}
		ring.mu.Lock()
		acked = ring.seekGen > 0 && ring.seekGen == ring.seekAcked
		continuous = ring.base == baseBefore && ring.length >= lengthBefore
		covered = ring.frameEndLocked() >= int64(seeks)
		ring.mu.Unlock()
		if acked && continuous && covered {
			break
		}
	}
	ring.mu.Lock()
	acked = ring.seekGen == ring.seekAcked
	ring.mu.Unlock()
	if !acked || !continuous || !covered {
		t.Fatalf("decode-ahead did not settle: acked=%t continuous=%t covered=%t "+
			"(base before %d, target frame %d)", acked, continuous, covered,
			baseBefore, int64(seeks))
	}
	if got := stream.transport.PositionSeconds(); got < seeks {
		t.Fatalf("position after decode-ahead seek = %.3f, want >= %.3f", got, seeks)
	}
	if !stream.transport.Buffering() {
		// Transport healthy: a subsequent Stream call returns data.
		n, more := stream.transport.Stream(out)
		if !more || n != len(out) {
			t.Fatalf("post-seek Stream = (%d, %t)", n, more)
		}
	}
}

// TestBackwardSeekStillRepositions pins the fallback: a backward hop beyond
// the resident window cannot be served by decoding ahead, so the ring resets
// to the target frame (real decoder reseek) and playback resumes.
func TestBackwardSeekStillRepositions(t *testing.T) {
	stream, err := openTrackStream(0, writeTestWAV(t, 60, 44100))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()

	out := make([]sample, 2048)
	for i := 0; i < 48; i++ {
		stream.transport.Stream(out)
	}
	stream.transport.Seek(0) // far backward: past the retained window
	if got := stream.transport.PositionSeconds(); got != 0 {
		t.Fatalf("position after backward seek = %.3f, want 0", got)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, more := stream.transport.Stream(out); !more {
			break
		}
		if !stream.transport.Buffering() {
			break
		}
	}
	if stream.transport.Buffering() {
		t.Fatal("backward seek did not resume after the real reposition")
	}
}

// TestCommitAfterDecodeAckKeepsWindow pins the ring-level ack semantics: the
// ack publishes without touching base/length/samples, and a stale generation
// (already superseded) is ignored.
func TestCommitAfterDecodeAckKeepsWindow(t *testing.T) {
	ring := newPCMRing(ringAheadWindow + ringMargin + ringChunkFrames)
	fillRingToFlowLimit(t, ring)
	ring.mu.Lock()
	base, length := ring.base, ring.length
	ring.mu.Unlock()

	ring.mu.Lock()
	ring.seekFrame = 0
	ring.seekGen++
	gen := ring.seekGen
	ring.mu.Unlock()
	ring.commitAfterDecode(gen)

	ring.mu.Lock()
	if ring.seekAcked != gen {
		ring.mu.Unlock()
		t.Fatalf("seekAcked = %d, want %d", ring.seekAcked, gen)
	}
	if ring.base != base || ring.length != length {
		ring.mu.Unlock()
		t.Fatalf("ack reset the window: base %d -> %d, length %d -> %d",
			base, ring.base, length, ring.length)
	}
	ring.mu.Unlock()

	// A superseded request must not ack.
	ring.mu.Lock()
	ring.seekGen++
	ring.mu.Unlock()
	ring.commitAfterDecode(gen)
	ring.mu.Lock()
	if ring.seekAcked == ring.seekGen {
		ring.mu.Unlock()
		t.Fatal("stale generation acked the newer seek")
	}
	ring.mu.Unlock()
}

// TestCloseWithTimeoutAbandonsSlowJoin pins the janitor behavior: when the
// producer cannot answer cancel promptly (here: blocked decoding with a full
// ring and no consumer), closing with a timeout returns almost immediately and
// the join still finishes out-of-band.
func TestCloseWithTimeoutAbandonsSlowJoin(t *testing.T) {
	stream, err := openTrackStream(0, writeTestWAV(t, 60, 44100))
	if err != nil {
		t.Fatal(err)
	}
	// Fill the ring completely and never consume: the producer wedges inside
	// its flow-control wait, which simulates a decoder call that does not
	// promptly observe the cancel.
	for {
		if !stream.ring.tryWrite(make([]pcmSample, ringChunkFrames)) {
			break
		}
	}

	start := time.Now()
	stream.closeWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 900*time.Millisecond {
		t.Fatalf("closeWithTimeout blocked for %v; the join should hand off", elapsed)
	}
	// The background join still completes: wait generous time for done.
	select {
	case <-stream.ring.wake:
	default:
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		// The producer exits once cancel fires; done closes and the janitor
		// clears the handles, at which point a finished close is visible.
		for {
			time.Sleep(5 * time.Millisecond)
			stream.closeMu.Lock()
			joinedOK := stream.cancel == nil
			stream.closeMu.Unlock()
			if joinedOK {
				return
			}
		}
	}()
	select {
	case <-joined:
	case <-time.After(10 * time.Second):
		t.Fatal("janitor never finished the abandoned join")
	}
}
