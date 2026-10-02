package player

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// Test sizes: small enough to wrap quickly, large enough to satisfy the
// constructor bounds and to keep the producer ahead of the consumer.
const (
	ringTestCapacity = 20000
	ringTestLookback = 4000
)

func ringTestRing() *pcmRing {
	return newPCMRingWith(ringTestCapacity, ringTestLookback, ringAheadWindow)
}

func rampFrame(index int64) pcmSample {
	return pcmSample{float32(index), float32(-index)}
}

func makeRamp(start int64, n int) []pcmSample {
	chunk := make([]pcmSample, n)
	for i := range chunk {
		chunk[i] = rampFrame(start + int64(i))
	}
	return chunk
}

// TestRingWrapRetainsExactRampValues streams more frames than the ring can hold
// through a real producer/consumer pair and asserts every frame is delivered
// exactly once, in order, with the right value. This is the exact-value
// replacement for the old concurrent-append test: the SPSC design makes lost
// or reordered frames a hard failure rather than a statistical one.
func TestRingWrapRetainsExactRampValues(t *testing.T) {
	const total = 10 * ringTestCapacity
	ring := ringTestRing()

	cancel := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		for pos := int64(0); pos < total; {
			n := int64(ringChunkFrames)
			if total-pos < n {
				n = total - pos
			}
			chunk := makeRamp(pos, int(n))
			for !ring.tryWrite(chunk) {
				if !ring.wait(cancel) {
					return
				}
			}
			pos += n
		}
		ring.finish(nil)
	}()

	consumeRingRamp(t, ring, total, 997)
	close(cancel)
	producer.Wait()

	if got := ring.capacityFrames(); got != ringTestCapacity {
		t.Fatalf("ring capacity changed to %d, want %d", got, ringTestCapacity)
	}
	if got := ring.residentFrames(); got > ringTestCapacity {
		t.Fatalf("resident frames %d exceed capacity %d", got, ringTestCapacity)
	}
}

// TestRingKeepsCapacityBounded writes many multiples of the capacity and
// asserts the backing array never grows.
func TestRingKeepsCapacityBounded(t *testing.T) {
	const total = 20 * ringTestCapacity
	ring := ringTestRing()
	backing := ring.samples
	cancel := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		for pos := int64(0); pos < total; {
			n := int64(ringChunkFrames)
			if total-pos < n {
				n = total - pos
			}
			chunk := makeRamp(pos, int(n))
			for !ring.tryWrite(chunk) {
				if !ring.wait(cancel) {
					return
				}
			}
			pos += n
		}
		ring.finish(nil)
	}()
	consumeRingRamp(t, ring, total, 4096)
	close(cancel)
	producer.Wait()

	if len(ring.samples) != ringTestCapacity {
		t.Fatalf("backing slice length = %d, want constant %d", len(ring.samples), ringTestCapacity)
	}
	if &ring.samples[0] != &backing[0] {
		t.Fatal("ring reallocated its backing array")
	}
}

// TestRingFreeSpaceIsMonotonicInReadHead is the direct check of the safety
// property the producer relies on: moving readHead backward can never make more
// space available, so a backward seek cannot cause an overwrite.
func TestRingFreeSpaceIsMonotonicInReadHead(t *testing.T) {
	ring := ringTestRing()
	full := makeRamp(0, ringTestCapacity)
	if !ring.tryWrite(full) {
		t.Fatal("could not fill the ring")
	}
	previous := -1
	for head := int64(-int64(ringTestCapacity)); head <= int64(ringTestCapacity)+100; head++ {
		ring.mu.Lock()
		ring.readHead = head
		space := ring.freeSpaceLocked()
		ring.mu.Unlock()
		if space < previous {
			t.Fatalf("free space decreased when readHead advanced to %d: %d -> %d", head, previous, space)
		}
		previous = space
	}
}

// TestRingBackwardSeekInsideWindowIsServedFromRing checks that a backward jump
// within the lookback window does not request a decoder seek and still yields
// correct frames.
func TestRingBackwardSeekInsideWindowIsServedFromRing(t *testing.T) {
	ring := ringTestRing()
	// Buffer enough that the target's full read-ahead window is resident.
	if !ring.tryWrite(makeRamp(0, 3*ringChunkFrames)) {
		t.Fatal("initial write rejected")
	}
	target := int64(100)
	ring.ensure(target)
	if _, _, ok := ring.pendingSeek(); ok {
		t.Fatal("backward seek inside the resident window requested a decoder seek")
	}
	buf := make([]pcmSample, 500)
	view := ring.window(target, target+int64(len(buf)), buf)
	assertRamp(t, view.samples, target, "backward seek inside window")
}

// TestRingSeekBeyondWindowRequestsAndAcksSeek checks the consumer-driven
// re-seek path: beyond the resident window a seek is requested, the consumer
// refuses to read until the producer acks, and frames after the seek are
// correct.
func TestRingSeekBeyondWindowRequestsAndAcksSeek(t *testing.T) {
	ring := ringTestRing()
	if !ring.tryWrite(makeRamp(0, ringChunkFrames)) {
		t.Fatal("initial write rejected")
	}

	target := int64(500000) // far beyond decoded data
	ring.ensure(target)
	gen, frame, ok := ring.pendingSeek()
	if !ok || frame != target {
		t.Fatalf("pending seek = (%d, %d, %t), want target %d", gen, frame, ok, target)
	}
	buf := make([]pcmSample, 256)
	if view := ring.window(target, target+int64(len(buf)), buf); view.ready {
		t.Fatal("consumer read while a seek rewrite was in flight")
	}

	ring.commitSeek(gen, frame)
	if _, _, ok := ring.pendingSeek(); ok {
		t.Fatal("seek not acked after commitSeek")
	}
	if !ring.tryWrite(makeRamp(target, ringChunkFrames)) {
		t.Fatal("post-seek write rejected")
	}
	view := ring.window(target, target+int64(len(buf)), buf)
	assertRamp(t, view.samples, target, "after seek")
}

// TestRingStaleSeekCommitDoesNotAck checks that a seek result is discarded if
// the consumer requested a newer seek while the decoder was repositioning.
func TestRingStaleSeekCommitDoesNotAck(t *testing.T) {
	ring := ringTestRing()
	ring.ensure(1000)
	gen, _, _ := ring.pendingSeek()
	ring.ensure(2000) // newer request supersedes
	ring.commitSeek(gen, 1000)
	if _, frame, ok := ring.pendingSeek(); !ok || frame != 2000 {
		t.Fatalf("stale commit acked or lost the newer seek: frame=%d ok=%t", frame, ok)
	}
}

// TestRingFinishPinsUnknownLengthTotal documents that unknown-length containers
// get a usable total once decoding stops.
func TestRingFinishPinsUnknownLengthTotal(t *testing.T) {
	ring := ringTestRing()
	if err := ring.errValue(); err != nil {
		t.Fatal(err)
	}
	ring.finish(nil)
	if !ring.finished {
		t.Fatal("finish did not mark the ring complete")
	}
	if ring.total != 0 {
		t.Fatalf("empty ring total = %d, want 0", ring.total)
	}
}

// consumeRingRamp drains exactly total frames from the ring, checking each
// value, and fails on timeout rather than hanging.
func consumeRingRamp(t *testing.T, ring *pcmRing, total int64, window int) {
	t.Helper()
	buf := make([]pcmSample, window)
	lo := int64(0)
	deadline := time.Now().Add(30 * time.Second)
	for lo < total {
		hi := lo + int64(len(buf))
		view := ring.window(lo, hi, buf)
		if !view.ready || len(view.samples) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("timed out at frame %d of %d (ready=%t)", lo, total, view.ready)
			}
			runtime.Gosched()
			continue
		}
		if view.base != lo {
			t.Fatalf("window base = %d, want %d", view.base, lo)
		}
		assertRamp(t, view.samples, lo, "stream")
		lo += int64(len(view.samples))
	}
}

func assertRamp(t *testing.T, got []pcmSample, start int64, context string) {
	t.Helper()
	for i, sample := range got {
		index := start + int64(i)
		want := rampFrame(index)
		if sample != want {
			t.Fatalf("%s: frame %d = %v, want %v", context, index, sample, want)
		}
	}
}

// TestRingMemoryStaysBoundedOverALongTrack streams 30 minutes of frames through
// a producer/consumer ring pair. The old unbounded buffer would have held
// 30 min * 44100 Hz * 8 B = ~635 MiB; the ring must keep live-heap growth under
// an explicit 64 MiB cap. The consumer reads the ring directly (the DSP is
// covered elsewhere) so the check stays fast under -race.
func TestRingMemoryStaysBoundedOverALongTrack(t *testing.T) {
	const (
		sampleRate = 44100
		minutes    = 30
		total      = minutes * 60 * sampleRate // 79,380,000 frames
		limitBytes = 64 << 20                  // 64 MiB
	)
	ring := newPCMRing(ringCapacityFrames)
	ring.setTotal(total)

	cancel := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		pos := int64(0)
		chunk := make([]pcmSample, ringChunkFrames)
		for pos < int64(total) {
			n := int64(len(chunk))
			if int64(total)-pos < n {
				n = int64(total) - pos
			}
			for i := 0; i < int(n); i++ {
				chunk[i] = rampFrame(pos + int64(i))
			}
			for !ring.tryWrite(chunk[:n]) {
				if !ring.wait(cancel) {
					return
				}
			}
			pos += n
		}
		ring.finish(nil)
	}()

	buf := make([]pcmSample, 4096)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	lo := int64(0)
	deadline := time.Now().Add(120 * time.Second)
	for lo < int64(total) {
		view := ring.window(lo, lo+int64(len(buf)), buf)
		if !view.ready || len(view.samples) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("timed out streaming the long track")
			}
			runtime.Gosched()
			continue
		}
		lo += int64(len(view.samples))
	}
	close(cancel)
	producer.Wait()

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if growth > limitBytes {
		t.Fatalf("heap grew by %.1f MiB over a %d-minute track, want under %d MiB",
			float64(growth)/(1<<20), minutes, limitBytes>>20)
	}
	if ring.capacityFrames() != ringCapacityFrames {
		t.Fatalf("ring capacity changed to %d", ring.capacityFrames())
	}
}
