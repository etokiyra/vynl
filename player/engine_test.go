package player

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/gopxl/beep"
)

func TestDecoderPrebuffersThenCompletesTrackInBackground(t *testing.T) {
	const seconds = 3
	const sampleRate = 44100
	path := writeTestWAV(t, seconds, sampleRate)
	track, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, more, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		track.close()
		t.Fatal(err)
	}
	if !more {
		track.close()
		t.Fatal("decoder unexpectedly reached EOF during the initial buffer")
	}
	if len(initial) < initialBufferFrames || len(initial) >= seconds*outputRate {
		track.close()
		t.Fatalf("initial sample count = %d, want at least %d and less than full track", len(initial), initialBufferFrames)
	}
	ring := newPCMRing(ringCapacityFrames)
	ring.setTotal(int64(track.totalFrames))
	if !ring.tryWrite(initial) {
		track.close()
		t.Fatal("initial write rejected")
	}
	streamer := newStreamingTransport(ring, outputRate)
	if got := streamer.DurationSeconds(); got != seconds {
		track.close()
		t.Fatalf("known duration = %.2f seconds, want %d", got, seconds)
	}
	engine := &Engine{ring: ring}
	engine.startDecoder(track)
	waitForRing(t, ring)
	engine.stopDecoder()
	if _, total := ring.status(); total != seconds*outputRate {
		t.Fatalf("background decode ended with total %d, want %d", total, seconds*outputRate)
	}
	if ring.capacityFrames() != ringCapacityFrames {
		t.Fatalf("ring capacity = %d, want %d", ring.capacityFrames(), ringCapacityFrames)
	}
}

func TestDecoderWorkerIsJoinedBeforeTrackReplacement(t *testing.T) {
	path := writeTestWAV(t, 20, 44100)
	track, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		track.close()
		t.Fatal(err)
	}
	ring := newPCMRing(ringCapacityFrames)
	ring.setTotal(int64(track.totalFrames))
	ring.tryWrite(initial)
	engine := &Engine{ring: ring}
	engine.startDecoder(track)
	engine.stopDecoder()
	if engine.decodeCancel != nil || engine.decodeDone != nil {
		t.Fatal("decoder handles remain set after cancel-and-join")
	}
	if !ring.isFinished() {
		t.Fatal("cancelled decoder did not finish its ring")
	}
	if ring.capacityFrames() != ringCapacityFrames {
		t.Fatalf("old track ring changed capacity to %d after worker join", ring.capacityFrames())
	}
}

func waitForRing(t *testing.T, ring *pcmRing) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ring.isFinished() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the background decoder")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestEngineDecoderSeeksAndResumesThroughRing plays far enough that the
// producer wraps the bounded ring, then seeks back to the start (beyond the
// retained window). That must trigger a real decoder re-seek and route through
// the buffering path until the producer acks, after which playback resumes.
func TestEngineDecoderSeeksAndResumesThroughRing(t *testing.T) {
	const seconds = 6 // 264,600 frames, larger than the 176,400-frame ring
	path := writeTestWAV(t, seconds, 44100)
	track, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, more, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Fatal("decoder unexpectedly reached EOF during the initial buffer")
	}
	ring := newPCMRing(ringCapacityFrames)
	ring.setTotal(int64(track.totalFrames))
	ring.tryWrite(initial)
	engine := &Engine{ring: ring}
	engine.startDecoder(track)
	defer engine.stopDecoder()

	streamer := newStreamingTransport(ring, outputRate)
	output := make([]sample, 2048)

	// Drive the whole track through the ring. The producer can only finish
	// (and therefore the ring can only hold its final window) after the
	// consumer has advanced readHead past the lookback, so completion
	// deterministically evicts the start of the track.
	deadline := time.Now().Add(30 * time.Second)
	for !ring.isFinished() {
		streamer.Stream(output)
		if time.Now().After(deadline) {
			t.Fatal("timed out decoding the track through the ring")
		}
	}
	if before := streamer.PositionSeconds(); before < 3 {
		t.Fatalf("position advanced too little before seek: %.2f", before)
	}

	streamer.Seek(0)
	if got := streamer.PositionSeconds(); got != 0 {
		t.Fatalf("position after seek = %.3f, want 0", got)
	}
	streamer.Stream(output)
	if !streamer.Buffering() {
		t.Fatal("seek to the evicted start should buffer until the decoder re-seeks")
	}

	deadline = time.Now().Add(10 * time.Second)
	for streamer.Buffering() && time.Now().Before(deadline) {
		streamer.Stream(output)
		time.Sleep(time.Millisecond)
	}
	if streamer.Buffering() {
		t.Fatal("decoder did not resume after the seek")
	}
	if got := streamer.PositionSeconds(); got <= 0 {
		t.Fatalf("position did not advance after resuming from seek: %.3f", got)
	}
	if err := ring.errValue(); err != nil {
		t.Fatalf("decoder error during seek: %v", err)
	}
}

func TestNewEngineRejectsEmptyLibrary(t *testing.T) {
	if _, err := NewEngine(nil, InitialState{}); err == nil {
		t.Fatal("NewEngine accepted an empty library")
	}
}

func TestPlanTrackSwapKeepsOrFallsBack(t *testing.T) {
	tracks := []library.Track{{Path: "a"}, {Path: "b"}}
	if index, keep := planTrackSwap("b", tracks); !keep || index != 1 {
		t.Fatalf("present path = (%d, %t), want (1, true)", index, keep)
	}
	if index, keep := planTrackSwap("missing", tracks); keep || index != 0 {
		t.Fatalf("absent path = (%d, %t), want (0, false)", index, keep)
	}
	if index, keep := planTrackSwap("", tracks); keep || index != 0 {
		t.Fatalf("empty path = (%d, %t), want (0, false)", index, keep)
	}
}

// TestReplaceTracksKeepsCurrentByPath covers the kept case: the list is
// replaced, the current track is still present (at a different index), and the
// live control is left untouched so playback is not interrupted.
func TestReplaceTracksKeepsCurrentByPath(t *testing.T) {
	control := &beep.Ctrl{}
	engine := &Engine{
		tracks:  []library.Track{{Path: "a"}, {Path: "b"}},
		index:   1,
		control: control,
	}
	engine.order = newPlayOrder(2)
	engine.order.setCurrent(1)

	engine.replaceTracks([]library.Track{{Path: "c"}, {Path: "b"}}, 7)

	if len(engine.tracks) != 2 || engine.tracks[1].Path != "b" {
		t.Fatalf("tracks after kept swap = %+v", engine.tracks)
	}
	if engine.index != 1 {
		t.Fatalf("index after kept swap = %d, want 1", engine.index)
	}
	if engine.revision != 7 {
		t.Fatalf("revision after kept swap = %d, want 7", engine.revision)
	}
	if engine.control != control {
		t.Fatal("kept swap replaced the control; playback would be interrupted")
	}
	if got := engine.order.current(); got != 1 {
		t.Fatalf("play order current = %d, want 1", got)
	}
}

// TestHandleSetTracksActionDispatches ensures the replacement goes through the
// normal actor command path rather than a side channel.
func TestHandleSetTracksActionDispatches(t *testing.T) {
	engine := &Engine{tracks: []library.Track{{Path: "a"}, {Path: "b"}}, index: 1}
	engine.order = newPlayOrder(2)
	engine.order.setCurrent(1)

	engine.handle(Command{
		Action:   SetTracks,
		Tracks:   []library.Track{{Path: "c"}, {Path: "b"}},
		Revision: 7,
	})

	if engine.index != 1 || engine.tracks[1].Path != "b" || engine.revision != 7 {
		t.Fatalf("handle(SetTracks) = index %d, tracks %+v, revision %d", engine.index, engine.tracks, engine.revision)
	}
}

// TestReplaceTracksRebuildsShuffleOrder confirms a swap before playback keeps a
// shuffled order valid and pointed at the current track.
func TestReplaceTracksRebuildsShuffleOrder(t *testing.T) {
	engine := &Engine{
		tracks:  []library.Track{{Path: "a"}, {Path: "b"}, {Path: "c"}},
		index:   2,
		shuffle: true,
	}
	engine.order = newPlayOrder(3)

	engine.replaceTracks([]library.Track{{Path: "c"}, {Path: "b"}, {Path: "a"}}, 1)

	if engine.index != 0 {
		t.Fatalf("index after shuffled swap = %d, want 0", engine.index)
	}
	if got := engine.order.current(); got != engine.index {
		t.Fatalf("order current = %d, want %d", got, engine.index)
	}
	seen := make([]bool, len(engine.tracks))
	for _, index := range engine.order.indices {
		if index < 0 || index >= len(engine.tracks) || seen[index] {
			t.Fatalf("shuffled order is not a permutation: %v", engine.order.indices)
		}
		seen[index] = true
	}
}

// TestReplaceTracksRemovedTrackResetsWithoutAudio covers the removed case when
// nothing is loaded yet (control == nil): the deck is repointed at index 0
// without trying to open a decoder.
func TestReplaceTracksRemovedTrackResetsWithoutAudio(t *testing.T) {
	engine := &Engine{tracks: []library.Track{{Path: "a"}, {Path: "b"}}, index: 1}
	engine.order = newPlayOrder(2)
	engine.order.setCurrent(1)

	engine.replaceTracks([]library.Track{{Path: "c"}, {Path: "d"}}, 3)

	if engine.index != 0 || engine.tracks[0].Path != "c" {
		t.Fatalf("removed swap = index %d, tracks %+v", engine.index, engine.tracks)
	}
	if engine.revision != 3 {
		t.Fatalf("revision after removed swap = %d, want 3", engine.revision)
	}
	if got := engine.order.current(); got != 0 {
		t.Fatalf("order current after removed swap = %d, want 0", got)
	}
}

// TestReplaceTracksRemovedBeforePlaybackKeepsShuffleOrder confirms the
// control == nil reset branch rebuilds the order through rebuildOrder, so a
// shuffle user who rescans before pressing play keeps a shuffled order.
func TestReplaceTracksRemovedBeforePlaybackKeepsShuffleOrder(t *testing.T) {
	engine := &Engine{
		tracks:  []library.Track{{Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"}},
		index:   0,
		shuffle: true,
	}
	engine.order = newPlayOrder(4)

	// The current path is absent, so the control == nil branch resets to 0.
	engine.replaceTracks([]library.Track{{Path: "w"}, {Path: "x"}, {Path: "y"}, {Path: "z"}}, 1)

	if engine.index != 0 || engine.order.current() != 0 {
		t.Fatalf("index/current = %d/%d, want 0/0", engine.index, engine.order.current())
	}
	seen := make([]bool, len(engine.tracks))
	for _, index := range engine.order.indices {
		if index < 0 || index >= len(engine.tracks) || seen[index] {
			t.Fatalf("reset order is not a permutation: %v", engine.order.indices)
		}
		seen[index] = true
	}
}

func TestReplaceTracksIgnoresEmptyList(t *testing.T) {
	engine := &Engine{tracks: []library.Track{{Path: "a"}}, index: 0}
	engine.order = newPlayOrder(1)

	engine.replaceTracks(nil, 9)

	if len(engine.tracks) != 1 || engine.revision != 0 {
		t.Fatalf("empty replacement changed the engine: tracks %+v, revision %d", engine.tracks, engine.revision)
	}
}

func TestPublishCarriesRevisionAndPathKeyedNextTrack(t *testing.T) {
	engine := &Engine{
		tracks:   []library.Track{{Path: "a", Title: "A"}, {Path: "b", Title: "B"}},
		updates:  make(chan Status, 1),
		revision: 5,
	}
	engine.order = newPlayOrder(2)

	engine.publish()
	status := <-engine.updates

	if status.Revision != 5 {
		t.Fatalf("status revision = %d, want 5", status.Revision)
	}
	if status.Track.Path != "a" || status.NextTrack.Path != "b" {
		t.Fatalf("status tracks = %+v / next %+v, want a / b", status.Track, status.NextTrack)
	}
}

func TestApplyInitialRestoresEveryPersistedSetting(t *testing.T) {
	engine := &Engine{tracks: make([]library.Track, 4)}
	initial := InitialState{
		Volume: 0.33, Muted: true, EQ: [3]float64{0.4, 1.4, 2},
		Shuffle: true, Vinyl: true, Repeat: RepeatOne,
	}
	engine.applyInitial(initial)

	if engine.volume != 0.33 || !engine.muted || engine.eq != [3]float64{0.4, 1.4, 2} ||
		!engine.shuffle || !engine.vinyl || engine.repeat != RepeatOne {
		t.Fatalf("initial state not applied: %+v", engine.PersistState())
	}
	if got := engine.PersistState(); got != (PersistState{
		Volume: 0.33, Muted: true, EQ: [3]float64{0.4, 1.4, 2},
		Shuffle: true, Vinyl: true, Repeat: RepeatOne,
	}) {
		t.Fatalf("persisted snapshot = %+v", got)
	}
}

func TestApplyInitialClampsAndDefaultsInvalidValues(t *testing.T) {
	engine := &Engine{tracks: make([]library.Track, 2)}
	engine.applyInitial(InitialState{
		Volume: 5, EQ: [3]float64{9, -1, 1}, Repeat: "bogus",
	})
	got := engine.PersistState()
	want := PersistState{Volume: 1, EQ: [3]float64{2, 0, 1}, Repeat: RepeatAll}
	if got != want {
		t.Fatalf("clamped state = %+v, want %+v", got, want)
	}
}

func TestEffectiveVolumeRespectsMute(t *testing.T) {
	engine := &Engine{volume: 0.8}
	if got := engine.effectiveVolume(); got != 0.8 {
		t.Fatalf("effective volume = %.2f, want 0.8", got)
	}
	engine.muted = true
	if got := engine.effectiveVolume(); got != 0 {
		t.Fatalf("muted effective volume = %.2f, want 0", got)
	}
	engine.volume = 0.4
	if got := engine.effectiveVolume(); got != 0 {
		t.Fatalf("muted effective volume changed with volume = %.2f, want 0", got)
	}
	engine.muted = false
	if got := engine.effectiveVolume(); got != 0.4 {
		t.Fatalf("unmuted effective volume = %.2f, want 0.4", got)
	}
}

func writeTestWAV(t *testing.T, seconds, sampleRate int) string {
	t.Helper()
	dataSize := seconds * sampleRate * 2 * 2
	data := make([]byte, 44+dataSize)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 2)
	binary.LittleEndian.PutUint32(data[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(data[28:32], uint32(sampleRate*4))
	binary.LittleEndian.PutUint16(data[32:34], 4)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(dataSize))
	path := filepath.Join(t.TempDir(), "progressive.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
