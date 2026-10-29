// Regression test for the reported switch/seek bug: "skipping forward a little
// bit breaks into buffering every time". The production ring's forward-resident
// window (capacity - lookback) must exceed the default seek step ([steps]
// seek = 5 s) plus the transport's ringAheadWindow, so a small forward skip is
// served from the ring without a decoder re-seek and the buffering it causes.
package player

import "testing"

func TestRingStaysResidentAcrossADefaultForwardSkip(t *testing.T) {
	ring := newPCMRing(ringCapacityFrames)

	// Steady state: the producer has filled as far ahead as flow control
	// allows, with the consumer's readHead (published by the last window call)
	// 10 s into the track.
	readHead := int64(10 * outputRate)
	ring.ensure(readHead)
	// The producer answers the initial positioning like the real producer does:
	// reposition and ack the seek so the resident window is usable.
	gen, frame, ok := ring.pendingSeek()
	if !ok {
		t.Fatal("setup: the initial positioning did not request a decoder seek")
	}
	ring.commitSeek(gen, frame)
	fillRingToFlowLimit(t, ring)

	ring.mu.Lock()
	residentAhead := ring.base + int64(ring.length) - readHead
	ring.mu.Unlock()
	if residentAhead <= int64(2*outputRate) {
		t.Fatalf("steady-state look-ahead = %.2f s, want comfortable headroom above the skip step",
			float64(residentAhead)/outputRate)
	}

	// A default-size forward skip must land inside the resident window and NOT
	// request a decoder seek (window() would serve it; the decode time a
	// re-seek needs is the "buffering break" users hear).
	defaultStep := int64(5 * outputRate)
	ring.ensure(readHead + defaultStep)
	ring.mu.Lock()
	resident := ring.seekGen == ring.seekAcked
	ring.mu.Unlock()
	if !resident {
		t.Fatalf("a +5 s forward skip left the resident window (look-ahead %.2f s); "+
			"the ring must cover the default seek step without a decoder re-seek",
			float64(residentAhead)/outputRate)
	}
}

// fillRingToFlowLimit writes zero chunks until the producer's flow control
// fills the ring exactly to the steady-state extent.
func fillRingToFlowLimit(t *testing.T, ring *pcmRing) {
	t.Helper()
	for {
		ring.mu.Lock()
		free := ring.freeSpaceLocked()
		if free <= 0 {
			ring.mu.Unlock()
			return
		}
		ring.mu.Unlock()
		if !ring.tryWrite(make([]pcmSample, min(ringChunkFrames, free))) {
			return
		}
	}
}
