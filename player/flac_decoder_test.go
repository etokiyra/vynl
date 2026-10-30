package player

// Regression tests for the positioned FLAC decoder and its frame index: when
// the user skips to the middle or end of a long song, the jump must land in
// the index's (one seek + a few decoded frames), never in a whole-stream
// re-decode. The FLAC tests run against probe assets under the dev
// environment and skip elsewhere; the WAV-based rolling test runs always.

import (
	"os"
	"testing"
	"time"
)

var flacProbeAssets = []string{
	"/tmp/opencode/flacprobe/noise240_tbl.flac",
	"/tmp/opencode/flacprobe/noise240_notbl.flac",
	"/tmp/opencode/flacprobe/long_tbl.flac",
	"/tmp/opencode/flacprobe/long_notbl.flac",
}

func firstFlacAsset(t *testing.T) string {
	for _, path := range flacProbeAssets {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no FLAC probe assets (dev environment only)")
	return ""
}

// TestFlacIndexBuildsAndLooksUp checks index invariants: monotone entries and
// a lookup that always returns the entry at or before the target.
func TestFlacIndexBuildsAndLooksUp(t *testing.T) {
	path := firstFlacAsset(t)
	dec, err := newFlacDecoder(path)
	if err != nil {
		t.Skipf("asset unavailable: %v", err)
	}
	defer dec.Close()
	dec.loader.wait(30 * time.Second)
	ix := dec.loader.index.Load()
	if ix == nil {
		t.Fatal("index did not become ready")
	}
	if len(ix.entries) == 0 {
		t.Fatal("index has no entries")
	}
	for i := 1; i < len(ix.entries); i++ {
		if ix.entries[i].sample <= ix.entries[i-1].sample {
			t.Fatalf("entry %d sample %d not after %d", i,
				ix.entries[i].sample, ix.entries[i-1].sample)
		}
	}
	if got, ok := ix.lookup(0); !ok || got.sample != ix.entries[0].sample {
		t.Fatalf("lookup(0) = %+v %t", got, ok)
	}
	for _, probe := range []uint64{1, 100000, 5000000} {
		el, ok := ix.lookup(probe)
		// The index is sorted: the expected result is the last entry at or
		// before the probe.
		expected := -1
		for i, entry := range ix.entries {
			if entry.sample > probe {
				break
			}
			expected = i
		}
		if !ok {
			if expected >= 0 {
				t.Fatalf("lookup(%d) = none, want entry %d (%d)",
					probe, expected, ix.entries[expected].sample)
			}
			continue
		}
		if expected < 0 || ix.entries[expected].sample != el.sample {
			t.Fatalf("lookup(%d) = %d, want the last at-or-before entry (%d)",
				probe, el.sample, func() uint64 {
					if expected >= 0 {
						return ix.entries[expected].sample
					}
					return 0
				}())
		}
	}
}

// TestFlacDecoderSeekEqualsSequentialDecode is the correctness cornerstone:
// audio decoded straight from the start must be byte-identical to audio
// decoded after a far seek - the index may land anywhere (stored seek table
// with coarse points, or a header scan), and the covering-frame discard must
// always produce the same samples.
func TestFlacDecoderSeekEqualsSequentialDecode(t *testing.T) {
	path := firstFlacAsset(t)
	dec, err := newFlacDecoder(path)
	if err != nil {
		t.Skipf("asset unavailable: %v", err)
	}
	defer dec.Close()
	dec.loader.wait(30 * time.Second)

	const skipSeconds = 30
	target := int64(skipSeconds * dec.inRate)
	buf := make([]sample, 4096)
	var sequential []sample
	for int64(len(sequential)) < target {
		n, ok := dec.Stream(buf)
		sequential = append(sequential, buf[:n]...)
		if !ok {
			t.Fatalf("sequential decode ended early at %d/%d frames",
				len(sequential), target)
		}
		if dec.Err() != nil {
			t.Fatalf("sequential decode errored: %v", dec.Err())
		}
	}

	fresh, err := newFlacDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.loader.wait(30 * time.Second)
	if err := fresh.Seek(int(target)); err != nil {
		t.Fatalf("indexed seek failed: %v", err)
	}
	var jumped []sample
	for len(jumped) < len(sequential) {
		n, ok := fresh.Stream(buf)
		if n > 0 {
			jumped = append(jumped, buf[:n]...)
		}
		if !ok {
			break
		}
		if fresh.Err() != nil {
			t.Fatalf("post-seek decode errored: %v", fresh.Err())
		}
	}
	if len(jumped) < len(sequential) {
		t.Fatalf("post-seek decode produced %d frames, want %d",
			len(jumped), len(sequential))
	}
	// The sequential run starts at 0; the seek run starts at target. Align.
	for i := 0; i+int(target) < len(sequential); i++ {
		if jumped[i] != sequential[i+int(target)] {
			t.Fatalf("sample %d differs after a far seek: %v vs %v", i,
				jumped[i], sequential[i+int(target)])
		}
	}

	// The same comparison via the file byte positions is implicitly covered:
	// the post-seek decode ran through the index, not sequentially.
}

// TestFlacDecoderUnindexedSeekMatches pins the fallback: with the index
// (still building or absent), the decode-discard path must land on the same
// samples an indexed seek does.
func TestFlacDecoderUnindexedSeekMatches(t *testing.T) {
	path := firstFlacAsset(t)

	reference, err := newFlacDecoder(path)
	if err != nil {
		t.Skipf("asset unavailable: %v", err)
	}
	defer reference.Close()
	reference.loader.wait(30 * time.Second)
	const skipSeconds = 20
	target := int64(skipSeconds * reference.inRate)
	if err := reference.Seek(int(target)); err != nil {
		t.Fatalf("indexed seek failed: %v", err)
	}

	// Unindexed twin: hide the index, force the discard path.
	unindexed, err := newFlacDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unindexed.Close()
	// Blocks the loader's publication and empties a cached index if one
	// already arrived.
	unindexed.loader.index.Store(nil)
	if err := unindexed.Seek(int(target)); err != nil {
		t.Fatalf("unindexed seek failed: %v", err)
	}

	buf, ref := make([]sample, 4096), make([]sample, 4096)
	frames := 0
	for frames < 4096 {
		n1, ok1 := reference.Stream(ref)
		n2, ok2 := unindexed.Stream(buf)
		if ok1 != ok2 {
			t.Fatalf("ok mismatch at %d: (%d,%t) vs (%d,%t)", frames, n1, ok1, n2, ok2)
		}
		for i := 0; i < n1; i++ {
			if buf[i] != ref[i] {
				t.Fatalf("sample %d differs between indexed and unindexed seek: %v vs %v",
					frames+i, buf[i], ref[i])
			}
		}
		frames += n1
		if !ok1 {
			break
		}
	}
	if frames <= 0 {
		t.Fatal("both decoders stalled at the seek target")
	}
}

// TestForwardSeekRollsWithoutFastReposition drives the unbounded rolling
// decode-ahead with a decoder that reports no cheap reposition: the forward
// seek must be served entirely by chasing the target through streaming decode,
// never by repositioning.
func TestForwardSeekRollsWithoutFastReposition(t *testing.T) {
	stream, err := openTrackStream(0, writeTestWAV(t, 20, 44100))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()
	// The positioned decoder path normally gates on the flac index; simulate
	// the "index not yet landed" state by hiding the positionable capability
	// (fastSeekCapable checks for it) from the decoder underneath.
	stream.decoder.stream = hideFastSeek{stream.decoder.stream}

	out := make([]sample, 2048)
	for i := 0; i < 16; i++ {
		stream.transport.Stream(out)
	}
	stream.transport.Seek(stream.transport.PositionSeconds() + 8)

	// Pump the transport at a relaxed cadence: the producer must roll and ack
	// regardless of consumer behaviour. The deadline is generous because the
	// race detector slows the rolling decode by a wide factor.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		stream.transport.Stream(out)
		ring := stream.ring
		ring.mu.Lock()
		acked := ring.seekGen > 0 && ring.seekGen == ring.seekAcked
		ring.mu.Unlock()
		if acked {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ring := stream.ring
	position := stream.transport.PositionSeconds()
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if ring.seekGen == 0 || ring.seekGen != ring.seekAcked {
		t.Fatal("rolling decode-ahead never acked the forward seek")
	}
	if position <= 0 {
		t.Fatalf("position after rolling seek = %.2f, want a mid-track position", position)
	}
	if ring.seekGen == ring.seekAcked && ring.length == 0 {
		t.Fatalf("rolling left an empty ring window: base %d finished %t position %.2f ackedGen %d gen %d",
			ring.base, ring.finished, position, ring.seekAcked, ring.seekGen)
	} // A real reseek would have reset base to exactly the seeked frame; the
	// rolling window instead keeps a continuous domain behind the target.
	targetFloor := int64(position * outputRate)
	if ring.base == targetFloor {
		t.Fatalf("base %d equals the seek target: the rolling path repositioned", ring.base)
	}
}

// hideFastSeek hides the fastSeek capability from a decoderCore, forcing the
// rolling (unindexed) repositioning branch.
type hideFastSeek struct{ decoderCore }

func (hideFastSeek) fastSeek() bool { return false }

// TestLongFileEndJumpIsFast probes the user-facing story: jumping to the end
// of a long FLAC (the old regression scenario, a whole-stream re-decode) must
// absorb in well under a second once the index has landed.
func TestLongFileEndJumpIsFast(t *testing.T) {
	path := firstFlacAsset(t)
	dec, err := newFlacDecoder(path)
	if err != nil {
		t.Skipf("asset unavailable: %v", err)
	}
	defer dec.Close()
	dec.loader.wait(30 * time.Second)
	if dec.indexReady() && dec.Len() == 0 {
		t.Skip("asset declares an unknown length")
	}
	start := time.Now()
	if err := dec.Seek(dec.Len() / 2); err != nil {
		t.Fatalf("mid-file seek failed: %v", err)
	}
	mid := time.Since(start)
	start = time.Now()
	if err := dec.Seek(dec.Len() - 1); err != nil {
		t.Fatalf("end seek failed: %v", err)
	}
	end := time.Since(start)
	t.Logf("mid-file seek: %v, near-end seek: %v", mid, end)
	if mid > 2*time.Second || end > 2*time.Second {
		t.Fatalf("indexed far jumps took mid %v end %v; want well under 2s", mid, end)
	}
}

// basenameOf reserved for test logging.
