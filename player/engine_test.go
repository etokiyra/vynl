package player

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/gopxl/beep"
	"github.com/gopxl/beep/speaker"
)

func TestOpenTrackStreamPrebuffersThenCompletesInBackground(t *testing.T) {
	const seconds = 3
	const sampleRate = 44100
	path := writeTestWAV(t, seconds, sampleRate)

	// The initial buffer is one second: enough to start immediately, less than
	// the whole track.
	decoder, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, more, err := decoder.decodeInitial(initialBufferFrames)
	if err != nil {
		decoder.close()
		t.Fatal(err)
	}
	if !more {
		decoder.close()
		t.Fatal("decoder unexpectedly reached EOF during the initial buffer")
	}
	if len(initial) < initialBufferFrames || len(initial) >= seconds*outputRate {
		decoder.close()
		t.Fatalf("initial sample count = %d, want at least %d and less than full track", len(initial), initialBufferFrames)
	}
	decoder.close()

	stream, err := openTrackStream(0, path)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()
	if got := stream.transport.DurationSeconds(); got != seconds {
		t.Fatalf("known duration = %.2f seconds, want %d", got, seconds)
	}
	if stream.ring.capacityFrames() != ringCapacityFrames {
		t.Fatalf("ring capacity = %d, want %d", stream.ring.capacityFrames(), ringCapacityFrames)
	}
	waitForRing(t, stream.ring)
	if _, total := stream.ring.status(); total != seconds*outputRate {
		t.Fatalf("background decode ended with total %d, want %d", total, seconds*outputRate)
	}
}

func TestTrackStreamCloseJoinsProducer(t *testing.T) {
	path := writeTestWAV(t, 20, 44100)
	stream, err := openTrackStream(0, path)
	if err != nil {
		t.Fatal(err)
	}
	stream.close()
	if stream.cancel != nil || stream.done != nil {
		t.Fatal("producer handles remain set after close")
	}
	if !stream.ring.isFinished() {
		t.Fatal("closed stream did not finish its ring")
	}
	if stream.ring.capacityFrames() != ringCapacityFrames {
		t.Fatalf("ring capacity changed to %d after close", stream.ring.capacityFrames())
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

// TestStreamSeeksAndResumesThroughRing plays far enough that the producer wraps
// the bounded ring, then seeks back to the start (beyond the retained window).
// That must trigger a real decoder re-seek and route through the buffering path
// until the producer acks, after which playback resumes.
func TestStreamSeeksAndResumesThroughRing(t *testing.T) {
	const seconds = 6 // 264,600 frames, larger than the 176,400-frame ring
	path := writeTestWAV(t, seconds, 44100)
	stream, err := openTrackStream(0, path)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()
	ring := stream.ring
	streamer := stream.transport
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

// TestOpenTrackDecoderValidatesExtensionFirst ensures a path sourced from a
// playlist cannot make VYNL open an arbitrary file: an unsupported extension is
// rejected before the filesystem is touched.
func TestOpenTrackDecoderValidatesExtensionFirst(t *testing.T) {
	_, err := openTrackDecoder("/definitely/missing/secret.txt")
	if err == nil || !strings.Contains(err.Error(), "unsupported audio format") {
		t.Fatalf("err = %v, want an unsupported-format error before opening", err)
	}
}

// TestSelectTrackSkipsUnplayableTracks is the core auto-skip guarantee: a
// corrupt file followed by a good one must not stall playback on the corrupt
// file. The engine opens the good track, marks it playing, and reports how many
// tracks it skipped so the UI can explain the jump.
func TestSelectTrackSkipsUnplayableTracks(t *testing.T) {
	corrupt := writeCorruptFile(t, "bad.mp3")
	good := writeTestWAV(t, 3, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: corrupt}, {Path: good}})
	t.Cleanup(func() { speaker.Clear() })

	engine.selectTrack(0, true)

	if engine.index != 1 {
		t.Fatalf("index after skip = %d, want 1 (the playable track)", engine.index)
	}
	if !engine.playing {
		t.Fatalf("playback not started on the playable track (err %q)", engine.errText)
	}
	if engine.errText != "" {
		t.Fatalf("errText after a successful skip = %q, want empty", engine.errText)
	}
	if engine.skipped != 1 {
		t.Fatalf("skipped = %d, want 1", engine.skipped)
	}
	status := drainStatus(engine)
	if !status.Playing || status.Skipped != 1 || status.Index != 1 {
		t.Fatalf("published status = playing %t skipped %d index %d, want playing true skipped 1 index 1",
			status.Playing, status.Skipped, status.Index)
	}
	if status.SampleRate != 44100 {
		t.Fatalf("status sample rate = %d, want 44100 from the loaded file", status.SampleRate)
	}
	engine.shutdownStreams()
}

// TestSelectTrackParksWhenEveryTrackFails checks the bound: an all-corrupt
// library ends parked on a failure with an error rather than looping forever.
func TestSelectTrackParksWhenEveryTrackFails(t *testing.T) {
	first := writeCorruptFile(t, "one.mp3")
	second := writeCorruptFile(t, "two.flac")
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(func() { speaker.Clear() })

	done := make(chan struct{})
	go func() {
		engine.selectTrack(0, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("selectTrack did not terminate on an all-corrupt library")
	}

	if engine.playing {
		t.Fatal("played a track even though every file was corrupt")
	}
	if engine.errText == "" {
		t.Fatal("no error surfaced after every track failed")
	}
	if engine.current != nil || engine.queue.primary != nil {
		t.Fatal("engine left audio state set after total load failure")
	}
	if engine.skipped != 0 {
		t.Fatalf("skipped = %d with no successful landing, want 0", engine.skipped)
	}
}

// TestSelectTrackDoesNotSkipWithoutAutoplay covers the rescan path: a removed
// current track parks paused on a corrupt replacement instead of auto-playing
// something the user did not ask for.
func TestSelectTrackDoesNotSkipWithoutAutoplay(t *testing.T) {
	corrupt := writeCorruptFile(t, "bad.mp3")
	good := writeTestWAV(t, 3, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: corrupt}, {Path: good}})
	t.Cleanup(func() { speaker.Clear() })

	engine.selectTrack(0, false)

	if engine.index != 0 {
		t.Fatalf("non-autoplay load skipped to index %d, want to stay on 0", engine.index)
	}
	if engine.playing {
		t.Fatal("non-autoplay load started playback")
	}
	if engine.errText == "" {
		t.Fatal("failed non-autoplay load did not surface an error")
	}
}

// engineWithTracks builds a minimal, actor-free engine for load-path tests. Its
// play order is initialized and its queue/control are in place because
// selectTrack drives them directly.
func engineWithTracks(t *testing.T, tracks []library.Track) *Engine {
	t.Helper()
	engine := &Engine{
		tracks:   tracks,
		updates:  make(chan Status, 1),
		speed:    1,
		analyzer: newSpectrumAnalyzer(analyzerSize, outputRate),
	}
	engine.queue = newQueueStreamer()
	engine.control = &beep.Ctrl{Streamer: engine.queue}
	engine.order = newPlayOrder(len(tracks))
	return engine
}

// drainStatus returns the most recently published status, draining any older
// single-slot value first.
func drainStatus(engine *Engine) Status {
	var status Status
	for {
		select {
		case status = <-engine.updates:
		default:
			return status
		}
	}
}

func writeCorruptFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("this is definitely not a decodable audio file"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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

// TestReplaceTracksCopiesInput guards against sharing the track-slice backing
// array with the UI. The UI updates display metadata in place as tags stream in,
// so a shared array would race the engine's per-tick publish.
func TestReplaceTracksCopiesInput(t *testing.T) {
	replacement := []library.Track{{Path: "a"}, {Path: "b"}}
	engine := &Engine{
		tracks:  []library.Track{{Path: "old"}},
		updates: make(chan Status, 1),
	}
	engine.order = newPlayOrder(1)

	engine.replaceTracks(replacement, 1)
	if len(engine.tracks) != 2 || engine.tracks[1].Path != "b" {
		t.Fatalf("replacement not applied: %+v", engine.tracks)
	}

	// Simulate a UI tag update mutating the caller's slice in place.
	replacement[0].Title = "tagged in the UI"
	replacement[1].Path = "changed"
	if engine.tracks[0].Title != "" || engine.tracks[1].Path != "b" {
		t.Fatalf("engine track slice shares backing with the caller: %+v", engine.tracks)
	}
}

func TestCloneTracksIsIndependent(t *testing.T) {
	source := []library.Track{{Path: "a"}, {Path: "b"}}
	copied := cloneTracks(source)
	source[0].Path = "mutated"
	if copied[0].Path != "a" || len(copied) != 2 {
		t.Fatalf("cloneTracks shares backing: %+v", copied)
	}
}

// TestReplaceTracksConcurrentTagUpdatesAreRaceFree exercises the exact race the
// clone prevents: the UI mutating its list in place while the engine publishes.
// Run under -race, this fails loudly if the engine ever shares the backing array.
func TestReplaceTracksConcurrentTagUpdatesAreRaceFree(t *testing.T) {
	replacement := []library.Track{{Path: "a"}, {Path: "b"}}
	engine := &Engine{
		tracks:  []library.Track{{Path: "old"}},
		updates: make(chan Status, 1),
	}
	engine.order = newPlayOrder(1)
	engine.replaceTracks(replacement, 1)

	const iterations = 2000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			engine.publish()
		}
	}()
	for i := 0; i < iterations; i++ {
		replacement[0].Title = "tagged"
		replacement[1].Path = "b"
	}
	<-done
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

// TestPublishNextTrackHonorsStopAndRepeat checks that NEXT agrees with what
// playback will actually do: repeat-one shows the current track, and repeat-off
// at the end of the order reports no next track (playback stops).
func TestPublishNextTrackHonorsStopAndRepeat(t *testing.T) {
	newEngine := func(repeat RepeatMode, index int) *Engine {
		engine := &Engine{
			tracks:  []library.Track{{Path: "a"}, {Path: "b"}, {Path: "c"}},
			updates: make(chan Status, 1),
			index:   index,
			repeat:  repeat,
		}
		engine.order = newPlayOrder(3)
		engine.order.setCurrent(index)
		return engine
	}

	cases := []struct {
		name   string
		repeat RepeatMode
		index  int
		want   string
	}{
		{"repeat-off at the end stops", RepeatOff, 2, ""},
		{"repeat-all wraps", RepeatAll, 2, "a"},
		{"repeat-one replays current", RepeatOne, 1, "b"},
		{"mid-queue advances", RepeatOff, 0, "b"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			engine := newEngine(test.repeat, test.index)
			engine.publish()
			if got := (<-engine.updates).NextTrack.Path; got != test.want {
				t.Fatalf("NEXT = %q, want %q", got, test.want)
			}
		})
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

// TestTrackGainModes covers the ReplayGain policy: mode selection, album
// fallback, preamp, missing metadata, and the off switch.
func TestTrackGainModes(t *testing.T) {
	tag := library.ReplayGain{TrackDB: -6, AlbumDB: -2, HasTrack: true, HasAlbum: true}

	engine := &Engine{replayGain: "off"}
	if got := engine.trackGain(tag); got != 1 {
		t.Fatalf("off gain = %v, want 1", got)
	}

	engine.replayGain = "track"
	if got := engine.trackGain(tag); math.Abs(got-0.501187) > 1e-5 {
		t.Fatalf("track gain = %v, want 10^(-6/20)", got)
	}
	engine.replayGain = "album"
	if got := engine.trackGain(tag); math.Abs(got-0.794328) > 1e-5 {
		t.Fatalf("album gain = %v, want 10^(-2/20)", got)
	}

	// Album mode falls back to the track gain when no album tag is present.
	if got := engine.trackGain(library.ReplayGain{TrackDB: -6, HasTrack: true}); math.Abs(got-0.501187) > 1e-5 {
		t.Fatalf("album fallback gain = %v, want 10^(-6/20)", got)
	}
	if got := engine.trackGain(library.ReplayGain{}); got != 1 {
		t.Fatalf("untagged gain = %v, want 1", got)
	}

	// Preamp is added before conversion; -6 + 6 = 0 dB.
	engine.replayGain = "track"
	engine.preampDB = 6
	if got := engine.trackGain(tag); math.Abs(got-1) > 1e-5 {
		t.Fatalf("preamp gain = %v, want 1 after +6 dB preamp", got)
	}
}

func TestApplyInitialNormalizesReplayGain(t *testing.T) {
	engine := &Engine{tracks: make([]library.Track, 2)}
	engine.applyInitial(InitialState{ReplayGain: "ALBUM", PreampDB: 99})
	if engine.replayGain != "album" || engine.preampDB != 12 {
		t.Fatalf("replaygain = %q preamp %v, want album/12", engine.replayGain, engine.preampDB)
	}
	engine.applyInitial(InitialState{ReplayGain: "bogus"})
	if engine.replayGain != "off" {
		t.Fatalf("bogus mode = %q, want off", engine.replayGain)
	}
}

func TestApplyInitialStoresEQCrossover(t *testing.T) {
	engine := &Engine{tracks: make([]library.Track, 2)}
	engine.applyInitial(InitialState{EQLowHz: 300, EQHighHz: 3000})
	if engine.eqLowHz != 300 || engine.eqHighHz != 3000 {
		t.Fatalf("crossover = %v/%v, want 300/3000", engine.eqLowHz, engine.eqHighHz)
	}
}

// driveQueue streams the queue until cond is true, failing on a timeout. It
// simulates the audio callback without a device.
func driveQueue(t *testing.T, engine *Engine, cond func() bool) {
	t.Helper()
	out := make([]sample, 1024)
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		engine.queue.Stream(out)
		if time.Now().After(deadline) {
			t.Fatal("timed out driving the queue")
		}
	}
}

// TestEngineGaplessPromotionAdvancesState checks the engine side of a gapless
// boundary: when the queue promotes the prefetched track, reconcile adopts it,
// updates the index and NEXT, and prefetches onward.
func TestEngineGaplessPromotionAdvancesState(t *testing.T) {
	first := writeTestWAV(t, 1, 44100)
	second := writeTestWAV(t, 2, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll

	engine.selectTrack(0, true)
	if engine.current == nil || engine.current.index != 0 {
		t.Fatalf("current = %+v, want track 0", engine.current)
	}
	if engine.queue.pending == nil || engine.queue.pending.index != 1 {
		t.Fatalf("pending = %+v, want a prefetch of track 1", engine.queue.pending)
	}

	driveQueue(t, engine, func() bool { return engine.queue.primary != engine.current })
	if engine.queue.primary.index != 1 {
		t.Fatalf("promoted primary = %+v, want track 1", engine.queue.primary)
	}
	engine.reconcile()
	engine.publish()
	if engine.index != 1 || engine.current == nil || engine.current.index != 1 {
		t.Fatalf("after reconcile: index %d current %+v", engine.index, engine.current)
	}
	if engine.queue.pending == nil || engine.queue.pending.index != 0 {
		t.Fatalf("pending after promotion = %+v, want wrapped track 0", engine.queue.pending)
	}
	status := drainStatus(engine)
	if status.Track.Path != second || status.NextTrack.Path != first {
		t.Fatalf("status after promotion = track %q next %q, want second / first",
			status.Track.Path, status.NextTrack.Path)
	}
}

// TestEngineRepeatOnePrefetchesSameTrack confirms repeat-one is gapless: the
// engine prefetches a fresh copy of the current track rather than the next one.
func TestEngineRepeatOnePrefetchesSameTrack(t *testing.T) {
	path := writeTestWAV(t, 1, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: path}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatOne

	engine.selectTrack(0, true)
	if engine.queue.pending == nil || engine.queue.pending.index != 0 {
		t.Fatalf("repeat-one pending = %+v, want a prefetch of track 0", engine.queue.pending)
	}
	driveQueue(t, engine, func() bool { return engine.queue.primary != engine.current })
	engine.reconcile()
	if engine.index != 0 || engine.current == nil || engine.current.index != 0 {
		t.Fatalf("repeat-one current after promotion = %+v, want track 0", engine.current)
	}
	if engine.queue.pending == nil {
		t.Fatal("repeat-one did not re-prefetch after promotion")
	}
}

// TestEngineRepeatOffStopsAtEnd checks the genuine end-of-playback path: a
// repeat-off library with no next track stops and parks on the ended track.
func TestEngineRepeatOffStopsAtEnd(t *testing.T) {
	path := writeTestWAV(t, 1, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: path}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatOff

	engine.selectTrack(0, true)
	if engine.queue.pending != nil {
		t.Fatalf("pending = %+v, want none for a repeat-off single track", engine.queue.pending)
	}
	driveQueue(t, engine, func() bool { return engine.queue.exhausted })
	engine.reconcile()
	if engine.playing {
		t.Fatal("repeat-off at the end did not stop playback")
	}
	if engine.current == nil || engine.index != 0 {
		t.Fatalf("deck should park on the ended track: index %d current %+v", engine.index, engine.current)
	}
}

// TestEnginePrefetchFailureFallsBackToSkip verifies that a corrupt next track
// does not stall the queue: no pending is installed, and at end-of-track the
// bounded skip path advances to the next playable track.
func TestEnginePrefetchFailureFallsBackToSkip(t *testing.T) {
	good := writeTestWAV(t, 1, 44100)
	corrupt := writeCorruptFile(t, "bad.mp3")
	good2 := writeTestWAV(t, 1, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: good}, {Path: corrupt}, {Path: good2}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll

	engine.selectTrack(0, true)
	if engine.queue.pending != nil {
		t.Fatalf("pending = %+v, want none because the next track is corrupt", engine.queue.pending)
	}
	driveQueue(t, engine, func() bool { return engine.queue.exhausted })
	engine.reconcile()
	engine.publish()
	if engine.current == nil || engine.current.index != 2 {
		t.Fatalf("fallback landed on %+v, want track 2", engine.current)
	}
	if engine.skipped != 1 {
		t.Fatalf("skipped = %d, want 1", engine.skipped)
	}
}

// TestEngineSimulatedCallbackReconcileIsRaceFree runs the audio callback and the
// engine reconcile/prefetch concurrently under -race, exercising the
// speaker.Lock discipline that guards the queue and transports.
func TestEngineSimulatedCallbackReconcileIsRaceFree(t *testing.T) {
	first := writeTestWAV(t, 1, 44100)
	second := writeTestWAV(t, 1, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll
	engine.selectTrack(0, true)

	stop := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		out := make([]sample, 1024)
		for {
			select {
			case <-stop:
				return
			default:
			}
			speaker.Lock()
			engine.queue.Stream(out)
			speaker.Unlock()
		}
	}()
	for i := 0; i < 10; i++ {
		engine.reconcile()
		engine.publish()
	}
	close(stop)
	group.Wait()
}

// TestEngineCrossfadePromotesIncomingEarly checks that with a crossfade the
// incoming track becomes current (and NEXT updates) while the outgoing is still
// fading, and that the outgoing is retired once the fade completes.
func TestEngineCrossfadePromotesIncomingEarly(t *testing.T) {
	first := writeTestWAV(t, 2, 44100)
	second := writeTestWAV(t, 2, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll
	engine.crossfadeFrames = outputRate
	engine.queue.crossfade = engine.crossfadeFrames

	engine.selectTrack(0, true)
	driveQueue(t, engine, func() bool { return engine.queue.fading != nil })
	if engine.queue.primary == nil || engine.queue.primary.index != 1 {
		t.Fatalf("incoming stream is not primary during the fade: %+v", engine.queue.primary)
	}
	engine.reconcile()
	engine.publish()
	if engine.index != 1 || engine.current == nil || engine.current.index != 1 {
		t.Fatalf("reconcile did not adopt the incoming track: index %d current %+v", engine.index, engine.current)
	}

	driveQueue(t, engine, func() bool { return engine.queue.fading == nil })
	engine.reconcileRetired()
	if len(engine.queue.retired) != 0 {
		t.Fatalf("retired streams were not drained after the fade: %v", engine.queue.retired)
	}
	status := drainStatus(engine)
	if status.Track.Path != second || status.NextTrack.Path != first {
		t.Fatalf("status during crossfade = track %q next %q, want second / first",
			status.Track.Path, status.NextTrack.Path)
	}
}

// TestEngineSeekCancelsCrossfade checks that a seek abandons an active fade so
// it takes effect immediately.
func TestEngineSeekCancelsCrossfade(t *testing.T) {
	first := writeTestWAV(t, 2, 44100)
	second := writeTestWAV(t, 2, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll
	engine.crossfadeFrames = outputRate
	engine.queue.crossfade = engine.crossfadeFrames
	engine.selectTrack(0, true)

	driveQueue(t, engine, func() bool { return engine.queue.fading != nil })
	engine.reconcile()
	engine.handle(Command{Action: Seek, Value: 1})
	if engine.queue.fading != nil {
		t.Fatal("seek did not cancel the active crossfade")
	}
}

// TestEngineNextDuringCrossfade checks the deterministic behavior of a manual
// skip during an active fade: both the incoming and outgoing streams are
// abandoned and the requested track is installed immediately.
func TestEngineNextDuringCrossfade(t *testing.T) {
	first := writeTestWAV(t, 2, 44100)
	second := writeTestWAV(t, 2, 44100)
	third := writeTestWAV(t, 2, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}, {Path: third}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll
	engine.crossfadeFrames = outputRate
	engine.queue.crossfade = engine.crossfadeFrames
	engine.selectTrack(0, true)

	driveQueue(t, engine, func() bool { return engine.queue.fading != nil })
	engine.reconcile() // adopt the incoming (track 1)
	engine.handle(Command{Action: Next})

	if engine.current == nil || engine.current.index != 2 {
		t.Fatalf("manual next during fade landed on %+v, want track 2", engine.current)
	}
	if engine.queue.fading != nil {
		t.Fatal("manual next left an outgoing stream fading")
	}
	// The hard change drops the old prefetch and installs a fresh one.
	engine.reconcileRetired()
	if len(engine.queue.retired) != 0 {
		t.Fatalf("retired streams were not drained: %v", engine.queue.retired)
	}
	if engine.queue.pending == nil {
		t.Fatal("manual next did not prefetch the following track")
	}
}

// TestEngineStopCancelsCrossfade checks that stopping abandons an active fade
// and parks the incoming track at its start.
func TestEngineStopCancelsCrossfade(t *testing.T) {
	first := writeTestWAV(t, 2, 44100)
	second := writeTestWAV(t, 2, 44100)
	engine := engineWithTracks(t, []library.Track{{Path: first}, {Path: second}})
	t.Cleanup(engine.shutdownStreams)
	engine.repeat = RepeatAll
	engine.crossfadeFrames = outputRate
	engine.queue.crossfade = engine.crossfadeFrames
	engine.selectTrack(0, true)

	driveQueue(t, engine, func() bool { return engine.queue.fading != nil })
	engine.reconcile()
	engine.handle(Command{Action: Stop})
	if engine.queue.fading != nil {
		t.Fatal("stop did not cancel the active crossfade")
	}
	if engine.playing {
		t.Fatal("stop left playback running")
	}
	if engine.current == nil || engine.current.transport.PositionSeconds() != 0 {
		t.Fatalf("stop did not rewind the incoming track: %+v", engine.current)
	}
}

func TestCrossfadeFramesFor(t *testing.T) {
	if got := crossfadeFramesFor(0); got != 0 {
		t.Fatalf("crossfadeFramesFor(0) = %d, want 0", got)
	}
	if got := crossfadeFramesFor(1000); got != outputRate {
		t.Fatalf("crossfadeFramesFor(1000) = %d, want %d", got, outputRate)
	}
	if got := crossfadeFramesFor(-5); got != 0 {
		t.Fatalf("crossfadeFramesFor(-5) = %d, want 0", got)
	}
	if got := crossfadeFramesFor(999999); got != 30*outputRate {
		t.Fatalf("crossfadeFramesFor(huge) = %d, want %d", got, 30*outputRate)
	}
}

// TestEngineCrossfadeRuntimeControls covers adjusting the crossfade while VYNL
// runs: stepping, clamping at zero, and toggling back to the remembered value.
func TestEngineCrossfadeRuntimeControls(t *testing.T) {
	engine := engineWithTracks(t, []library.Track{{Path: "a.wav"}})

	engine.handle(Command{Action: Crossfade, Value: 500})
	if got := engine.crossfadeMS(); got != 500 {
		t.Fatalf("crossfade after +500 = %d, want 500", got)
	}
	if engine.queue.crossfade != crossfadeFramesFor(500) {
		t.Fatalf("queue crossfade = %d, want %d", engine.queue.crossfade, crossfadeFramesFor(500))
	}

	engine.handle(Command{Action: Crossfade, Value: 500})
	if got := engine.crossfadeMS(); got != 1000 {
		t.Fatalf("crossfade after second +500 = %d, want 1000", got)
	}

	engine.handle(Command{Action: Crossfade, Value: -99999})
	if got := engine.crossfadeMS(); got != 0 {
		t.Fatalf("crossfade did not clamp to 0: %d", got)
	}

	engine.handle(Command{Action: CrossfadeToggle})
	if got := engine.crossfadeMS(); got != 1000 {
		t.Fatalf("toggle-on restored %d, want 1000", got)
	}
	engine.handle(Command{Action: CrossfadeToggle})
	if got := engine.crossfadeMS(); got != 0 {
		t.Fatalf("toggle-off = %d, want 0", got)
	}
	if engine.crossfadeRestoreMS != 1000 {
		t.Fatalf("restore value = %d, want 1000", engine.crossfadeRestoreMS)
	}

	status := drainStatus(engine)
	if status.CrossfadeMS != 0 {
		t.Fatalf("status crossfade = %d, want the runtime value 0", status.CrossfadeMS)
	}
}

func TestEngineCrossfadeRuntimeClampsToMax(t *testing.T) {
	engine := engineWithTracks(t, []library.Track{{Path: "a.wav"}})
	engine.handle(Command{Action: Crossfade, Value: 1_000_000})
	if got := engine.crossfadeMS(); got != maxCrossfadeMS {
		t.Fatalf("crossfade = %d, want %d", got, maxCrossfadeMS)
	}
}

func TestNextReplayGainCyclesOffTrackAlbum(t *testing.T) {
	cases := []struct{ in, want string }{
		{"off", "track"},
		{"track", "album"},
		{"album", "off"},
		{"", "track"},
	}
	for _, test := range cases {
		if got := nextReplayGain(test.in); got != test.want {
			t.Errorf("nextReplayGain(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestEngineReplayGainAndPreampRuntimeControls(t *testing.T) {
	engine := engineWithTracks(t, []library.Track{{Path: "a.wav"}})

	engine.handle(Command{Action: ReplayGain})
	if engine.replayGain != "track" {
		t.Fatalf("replaygain mode = %q, want track", engine.replayGain)
	}
	engine.handle(Command{Action: ReplayGain})
	if engine.replayGain != "album" {
		t.Fatalf("replaygain mode = %q, want album", engine.replayGain)
	}
	engine.handle(Command{Action: ReplayGain})
	if engine.replayGain != "off" {
		t.Fatalf("replaygain mode = %q, want off", engine.replayGain)
	}

	engine.handle(Command{Action: Preamp, Value: 5})
	if engine.preampDB != 5 {
		t.Fatalf("preamp = %.1f, want 5", engine.preampDB)
	}
	engine.handle(Command{Action: Preamp, Value: 100})
	if engine.preampDB != 12 {
		t.Fatalf("preamp did not clamp high: %.1f", engine.preampDB)
	}
	engine.handle(Command{Action: Preamp, Value: -100})
	if engine.preampDB != -12 {
		t.Fatalf("preamp did not clamp low: %.1f", engine.preampDB)
	}
	status := drainStatus(engine)
	if status.PreampDB != -12 || status.ReplayGain != "off" {
		t.Fatalf("status preamp/replaygain = %.1f/%q, want -12/off", status.PreampDB, status.ReplayGain)
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
