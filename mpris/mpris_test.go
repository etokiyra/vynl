package mpris

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/godbus/dbus/v5/prop"
)

type recordedCommands struct {
	commands []player.Command
}

func (r *recordedCommands) Send(command player.Command) {
	r.commands = append(r.commands, command)
}

func newTestService(engine Controller) *Service {
	s := &Service{engine: engine, updates: make(chan update, 1)}
	s.current = snapshot{trackID: noTrack, status: player.Status{Speed: 1, Volume: 1}}
	return s
}

func TestPlaybackStatusMapsEngineState(t *testing.T) {
	cases := []struct {
		name   string
		status player.Status
		want   string
	}{
		{"playing", player.Status{Playing: true, Track: library.Track{Path: "/a.flac"}}, "Playing"},
		{"loaded but paused", player.Status{Track: library.Track{Path: "/a.flac"}}, "Paused"},
		{"nothing loaded", player.Status{}, "Stopped"},
	}
	for _, test := range cases {
		if got := playbackStatus(test.status); got != test.want {
			t.Errorf("%s: playbackStatus = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestLoopStatusMapsRepeatModes(t *testing.T) {
	cases := map[player.RepeatMode]string{
		player.RepeatOff: "None",
		player.RepeatAll: "Playlist",
		player.RepeatOne: "Track",
	}
	for mode, want := range cases {
		if got := loopStatus(mode); got != want {
			t.Errorf("loopStatus(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestTrackIDIsStablePerPath(t *testing.T) {
	first := trackIDFor("/music/a.flac")
	if first != trackIDFor("/music/a.flac") {
		t.Fatal("track ID changed for the same path")
	}
	if first == trackIDFor("/music/b.flac") {
		t.Fatal("different paths share a track ID")
	}
	if !strings.HasPrefix(string(first), "/org/vynl/vynl/track/") {
		t.Fatalf("track ID %q is not under vynl's namespace", first)
	}
	if got := trackIDFor(""); got != noTrack {
		t.Fatalf("empty path track ID = %q, want %q", got, noTrack)
	}
}

func TestMetadataCarriesTaggedFieldsAndLength(t *testing.T) {
	track := library.Track{
		Path: "/music/artist - album - song.flac", Title: "Song",
		Artist: "Artist", Album: "Album",
	}
	metadata := metadataFor(track, trackIDFor(track.Path), 3_500_000)
	want := map[string]any{
		"mpris:trackid": trackIDFor(track.Path),
		"mpris:length":  int64(3_500_000),
		"xesam:title":   "Song",
		"xesam:album":   "Album",
		"xesam:url":     "file:///music/artist%20-%20album%20-%20song.flac",
	}
	for key, value := range want {
		variant, ok := metadata[key]
		if !ok {
			t.Fatalf("metadata is missing %s", key)
		}
		if got := variant.Value(); got != value {
			t.Errorf("metadata[%s] = %#v, want %#v", key, got, value)
		}
	}
	artists, ok := metadata["xesam:artist"].Value().([]string)
	if !ok || len(artists) != 1 || artists[0] != "Artist" {
		t.Fatalf("xesam:artist = %#v, want []string{\"Artist\"}", metadata["xesam:artist"].Value())
	}
}

func TestMetadataSanitizesUntrustedTags(t *testing.T) {
	track := library.Track{
		Path:   "/music/x.flac",
		Title:  "bad\x1b[31m title\x00",
		Artist: "artist\nwith\x7fcontrols",
		Album:  "album",
	}
	metadata := metadataFor(track, trackIDFor(track.Path), 0)
	title := metadata["xesam:title"].Value().(string)
	if strings.ContainsAny(title, "\x1b\x00\n\x7f") {
		t.Fatalf("title retained control characters: %q", title)
	}
	artist := metadata["xesam:artist"].Value().([]string)[0]
	if strings.ContainsAny(artist, "\n\x7f") {
		t.Fatalf("artist retained control characters: %q", artist)
	}
}

func TestMetadataOmitsArtWithoutCover(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(track, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := metadataFor(library.Track{Path: track, Title: "Song"}, trackIDFor(track), 0)["mpris:artUrl"]; got.Value() != nil {
		t.Fatalf("metadata has artwork without a cover file: %#v", got.Value())
	}
}

func TestMetadataFindsFolderCover(t *testing.T) {
	dir := t.TempDir()
	track := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(track, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(cover, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := metadataFor(library.Track{Path: track, Title: "Song"}, trackIDFor(track), 0)["mpris:artUrl"].Value()
	want := "file://" + cover
	if got != want {
		t.Fatalf("mpris:artUrl = %q, want %q", got, want)
	}
}

func TestDetectedSeekIgnoresNormalProgression(t *testing.T) {
	base := time.Now()
	previous := snapshot{
		track:      library.Track{Path: "/music/a.flac"},
		positionUS: 1_000_000,
		at:         base,
		status:     player.Status{Playing: true, Speed: 1, Duration: 10},
	}
	next := update{
		track:  previous.track,
		at:     base.Add(80 * time.Millisecond),
		status: player.Status{Playing: true, Speed: 1, Duration: 10, Position: 1.08},
	}
	if _, ok := detectedSeek(previous, next, 1_080_000); ok {
		t.Fatal("normal progression was reported as a seek")
	}
	if _, ok := detectedSeek(previous, next, 6_080_000); !ok {
		t.Fatal("a five-second position jump was not reported as a seek")
	}
}

func TestPublishKeepsOnlyTheLatestStatus(t *testing.T) {
	s := newTestService(&recordedCommands{})
	for i := 0; i < 3; i++ {
		s.Publish(player.Status{Position: float64(i)}, library.Track{Path: "/x.flac"})
	}
	select {
	case got := <-s.updates:
		if got.status.Position != 2 {
			t.Fatalf("mailbox held position %.0f, want the latest (2)", got.status.Position)
		}
	default:
		t.Fatal("publish did not leave an update in the mailbox")
	}
}

func TestTransportMethodsTranslateToEngineCommands(t *testing.T) {
	engine := &recordedCommands{}
	s := newTestService(engine)

	s.Next()
	s.Previous()
	s.PlayPause()
	s.Stop()
	s.Play()
	if len(engine.commands) != 5 {
		t.Fatalf("commands = %d, want 5", len(engine.commands))
	}
	want := []player.Action{player.Next, player.Prev, player.Toggle, player.Stop, player.Play}
	for i, action := range want {
		if engine.commands[i].Action != action {
			t.Errorf("command %d = %q, want %q", i, engine.commands[i].Action, action)
		}
	}

	engine.commands = nil
	s.current.status.Playing = true
	s.Play()
	s.Pause()
	want = []player.Action{player.Play, player.Pause}
	if len(engine.commands) != len(want) {
		t.Fatalf("explicit playback commands = %+v, want %v", engine.commands, want)
	}
	for i, action := range want {
		if engine.commands[i].Action != action {
			t.Errorf("explicit command %d = %q, want %q", i, engine.commands[i].Action, action)
		}
	}
}

func TestSeekMethodsUseAbsoluteAndRelativeTargets(t *testing.T) {
	engine := &recordedCommands{}
	s := newTestService(engine)
	id := trackIDFor("/music/a.flac")
	s.current = snapshot{
		trackID: id, positionUS: 1_000_000, durationUS: 5_000_000,
		status: player.Status{Track: library.Track{Path: "/music/a.flac"}, Duration: 5},
	}

	s.SeekRelative(2_000_000)
	if got := engine.commands[0]; got.Action != player.Seek || math.Abs(got.Value-2) > 1e-9 {
		t.Fatalf("relative seek command = %+v, want Seek(2s)", got)
	}

	engine.commands = nil
	s.SetPosition(id, 3_000_000)
	if got := engine.commands[0]; got.Action != player.SeekTo || math.Abs(got.Value-3) > 1e-9 {
		t.Fatalf("absolute seek command = %+v, want SeekTo(3s)", got)
	}

	engine.commands = nil
	s.SetPosition(id, 99_000_000)
	if got := engine.commands[0]; got.Action != player.SeekTo || math.Abs(got.Value-5) > 1e-9 {
		t.Fatalf("clamped absolute seek = %+v, want SeekTo(5s)", got)
	}

	engine.commands = nil
	s.SetPosition(trackIDFor("/music/other.flac"), 3_000_000)
	if len(engine.commands) != 0 {
		t.Fatalf("stale track ID produced commands: %+v", engine.commands)
	}
}

func TestWritablePropertiesTranslateToEngineCommands(t *testing.T) {
	engine := &recordedCommands{}
	s := newTestService(engine)
	s.current.status.Volume = 0.5
	s.current.status.Speed = 1
	s.current.status.Repeat = player.RepeatOff
	s.current.status.Shuffle = false

	if err := s.onSetVolume(&prop.Change{Value: 0.2}); err != nil {
		t.Fatal(err)
	}
	if got := engine.commands[0]; got.Action != player.Volume || math.Abs(got.Value-(-0.3)) > 1e-9 {
		t.Fatalf("volume command = %+v, want Volume(-0.3)", got)
	}

	engine.commands = nil
	if err := s.onSetRate(&prop.Change{Value: 1.5}); err != nil {
		t.Fatal(err)
	}
	if got := engine.commands[0]; got.Action != player.Speed || math.Abs(got.Value-0.5) > 1e-9 {
		t.Fatalf("rate command = %+v, want Speed(+0.5)", got)
	}

	engine.commands = nil
	if err := s.onSetLoopStatus(&prop.Change{Value: "Track"}); err != nil {
		t.Fatal(err)
	}
	if len(engine.commands) != 2 || engine.commands[0].Action != player.Repeat || engine.commands[1].Action != player.Repeat {
		t.Fatalf("loop command = %+v, want two Repeat cycles to reach Track", engine.commands)
	}

	engine.commands = nil
	if err := s.onSetShuffle(&prop.Change{Value: true}); err != nil {
		t.Fatal(err)
	}
	if len(engine.commands) != 1 || engine.commands[0].Action != player.Shuffle {
		t.Fatalf("shuffle command = %+v, want one Shuffle toggle", engine.commands)
	}

	engine.commands = nil
	if err := s.onSetShuffle(&prop.Change{Value: false}); err != nil {
		t.Fatal(err)
	}
	if len(engine.commands) != 0 {
		t.Fatalf("setting shuffle to its current value produced commands: %+v", engine.commands)
	}
}

func TestPropertySettersRejectInvalidValues(t *testing.T) {
	engine := &recordedCommands{}
	s := newTestService(engine)
	if err := s.onSetVolume(&prop.Change{Value: math.NaN()}); err == nil {
		t.Error("NaN volume was accepted")
	}
	if err := s.onSetRate(&prop.Change{Value: math.Inf(1)}); err == nil {
		t.Error("infinite rate was accepted")
	}
	if err := s.onSetLoopStatus(&prop.Change{Value: "Everywhere"}); err == nil {
		t.Error("invalid loop status was accepted")
	}
	if len(engine.commands) != 0 {
		t.Fatalf("invalid property values produced commands: %+v", engine.commands)
	}
}

func TestMicrosecondsRejectsUnusableDurations(t *testing.T) {
	cases := []struct {
		seconds float64
		want    int64
	}{
		{0, 0},
		{-1, 0},
		{math.NaN(), 0},
		{math.Inf(1), 0},
		{1.5, 1_500_000},
	}
	for _, test := range cases {
		if got := microseconds(test.seconds); got != test.want {
			t.Errorf("microseconds(%v) = %d, want %d", test.seconds, got, test.want)
		}
	}
}
