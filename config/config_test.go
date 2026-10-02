package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKeepsDefaultsForOmittedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[theme]\ncyan = \"#00ffff\"\n[keybindings]\nnext = \"j\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MusicDir != Defaults().MusicDir || cfg.Theme.Magenta != Defaults().Theme.Magenta || cfg.Keybindings.Toggle != " " {
		t.Fatalf("omitted defaults were not preserved: %+v", cfg)
	}
	if cfg.Theme.Cyan != "#00ffff" || cfg.Keybindings.Next != "j" {
		t.Fatalf("configured values were not loaded: %+v", cfg)
	}
}

func TestDefaultsBindPlaybackControlsDistinctly(t *testing.T) {
	keys := Defaults().Keybindings
	bound := map[string]string{
		"toggle": keys.Toggle, "stop": keys.Stop, "next": keys.Next, "previous": keys.Prev,
		"seek_back": keys.SeekBack, "seek_forward": keys.SeekForward, "restart": keys.Restart,
		"volume_down": keys.VolumeDown, "volume_up": keys.VolumeUp, "mute": keys.Mute,
		"speed_down": keys.SpeedDown, "speed_up": keys.SpeedUp, "pitch_down": keys.PitchDown,
		"pitch_up": keys.PitchUp, "reset": keys.Reset, "vinyl": keys.Vinyl, "shuffle": keys.Shuffle,
		"repeat": keys.Repeat, "sort": keys.Sort, "search": keys.Search, "quit": keys.Quit,
		"rescan": keys.Rescan, "now_playing": keys.NowPlaying,
		"top": keys.Top, "bottom": keys.Bottom, "page_up": keys.PageUp, "page_down": keys.PageDown,
		"eq_low": keys.EQLow, "eq_mid": keys.EQMid, "eq_high": keys.EQHigh,
		"eq_gain_down": keys.EQGainDown, "eq_gain_up": keys.EQGainUp,
	}
	seen := map[string]string{}
	for action, key := range bound {
		if key == "" {
			t.Fatalf("action %q has an empty default binding", action)
		}
		if other, ok := seen[key]; ok {
			t.Fatalf("default key %q is bound to both %q and %q", key, other, action)
		}
		seen[key] = action
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	cases := map[string]string{
		"~":              home,
		"~/Music":        filepath.Join(home, "Music"),
		"~/a/b":          filepath.Join(home, "a", "b"),
		"/absolute/path": "/absolute/path",
		"relative/path":  "relative/path",
		"~user/music":    "~user/music",
	}
	for input, want := range cases {
		if got := ExpandHome(input); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLoadExpandsHomeInMusicDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("music_dir = \"~/Music\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Music"); cfg.MusicDir != want {
		t.Fatalf("music_dir = %q, want %q", cfg.MusicDir, want)
	}
}

func TestLoadStepsDefaultsAndOverrides(t *testing.T) {
	// Omitted steps keep their defaults.
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Steps != Defaults().Steps {
		t.Fatalf("default steps = %+v, want %+v", cfg.Steps, Defaults().Steps)
	}

	// Configured steps are loaded; a misconfigured non-positive value falls
	// back to the default rather than making a transport key a no-op.
	path := filepath.Join(t.TempDir(), "config.toml")
	data := "[steps]\nseek = 10.0\nvolume = 0.0\ntempo = 0.25\npitch = 2.0\neq = 0.2\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults().Steps
	want.Seek, want.Tempo, want.Pitch, want.EQ = 10, 0.25, 2, 0.2
	if cfg.Steps != want {
		t.Fatalf("loaded steps = %+v, want %+v", cfg.Steps, want)
	}
}

func TestLoadReadsEQAndThemeKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := "[theme]\nmuted = \"#111111\"\npink = \"#222222\"\nerror = \"#333333\"\n" +
		"[keybindings]\neq_low = \"q\"\neq_gain_up = \"p\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme.Muted != "#111111" || cfg.Theme.Pink != "#222222" || cfg.Theme.Error != "#333333" {
		t.Fatalf("theme colors not loaded: %+v", cfg.Theme)
	}
	if cfg.Keybindings.EQLow != "q" || cfg.Keybindings.EQGainUp != "p" {
		t.Fatalf("EQ keys not loaded: %+v", cfg.Keybindings)
	}
	if cfg.Keybindings.EQMid != "2" || cfg.Keybindings.EQGainDown != "-" {
		t.Fatalf("omitted EQ keys lost defaults: %+v", cfg.Keybindings)
	}
}

func TestLoadPlaybackReplayGain(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Playback.ReplayGain != "off" || cfg.Playback.PreampDB != 0 {
		t.Fatalf("default playback = %+v, want off/0", cfg.Playback)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[playback]\nreplaygain = \"album\"\npreamp_db = 3.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Playback.ReplayGain != "album" || cfg.Playback.PreampDB != 3.5 {
		t.Fatalf("loaded playback = %+v, want album/3.5", cfg.Playback)
	}
}

func TestPlaybackNormalization(t *testing.T) {
	cases := []struct {
		mode       string
		preamp     float64
		wantMode   string
		wantPreamp float64
	}{
		{"TRACK", 0, "track", 0},
		{" Album ", 0, "album", 0},
		{"bogus", 0, "off", 0},
		{"", 0, "off", 0},
		{"track", 99, "track", 12},
		{"track", -99, "track", -12},
	}
	for _, test := range cases {
		got := Playback{ReplayGain: test.mode, PreampDB: test.preamp}.normalized()
		if got.ReplayGain != test.wantMode || got.PreampDB != test.wantPreamp {
			t.Errorf("normalized(%q, %v) = %+v, want %s/%v", test.mode, test.preamp, got, test.wantMode, test.wantPreamp)
		}
	}
	if got := (Playback{ReplayGain: "track", PreampDB: math.NaN()}).normalized(); got.PreampDB != 0 {
		t.Fatalf("NaN preamp = %v, want 0", got.PreampDB)
	}
}

func TestLoadEQSettings(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EQ != (EQ{LowHz: 250, HighHz: 4000}) {
		t.Fatalf("default EQ = %+v, want 250/4000", cfg.EQ)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[eq]\nlow_hz = 300\nhigh_hz = 3500\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EQ.LowHz != 300 || cfg.EQ.HighHz != 3500 {
		t.Fatalf("loaded EQ = %+v, want 300/3500", cfg.EQ)
	}
}

func TestEQNormalization(t *testing.T) {
	cases := []struct {
		low, high         float64
		wantLow, wantHigh float64
	}{
		{300, 3500, 300, 3500},
		{0, 0, 250, 4000},
		{-5, 2000, 250, 2000},   // invalid low falls back, high honored
		{1000, -1, 1000, 4000},  // invalid high falls back
		{3000, 2000, 250, 4000}, // reversed/too close -> defaults
		{10, 4000, 20, 4000},    // low clamped up
		{1000, 30000, 1000, 20000},
	}
	for _, test := range cases {
		got := EQ{LowHz: test.low, HighHz: test.high}.normalized()
		if got.LowHz != test.wantLow || got.HighHz != test.wantHigh {
			t.Errorf("normalized(%v, %v) = %+v, want %v/%v", test.low, test.high, got, test.wantLow, test.wantHigh)
		}
	}
	if got := (EQ{LowHz: math.NaN(), HighHz: math.NaN()}).normalized(); got != (EQ{LowHz: 250, HighHz: 4000}) {
		t.Fatalf("NaN crossover = %+v, want defaults", got)
	}
}

func TestLoadPlaybackCrossfade(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Playback.CrossfadeMS != 0 {
		t.Fatalf("default crossfade = %d ms, want 0", cfg.Playback.CrossfadeMS)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[playback]\ncrossfade_ms = 4000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Playback.CrossfadeMS != 4000 {
		t.Fatalf("loaded crossfade = %d ms, want 4000", cfg.Playback.CrossfadeMS)
	}
	if got := (Playback{CrossfadeMS: -5}).normalized().CrossfadeMS; got != 0 {
		t.Fatalf("negative crossfade normalized to %d, want 0", got)
	}
	if got := (Playback{CrossfadeMS: 999999}).normalized().CrossfadeMS; got != maxCrossfadeMS {
		t.Fatalf("huge crossfade normalized to %d, want %d", got, maxCrossfadeMS)
	}
}

func TestLoadReadsNewPlaybackControlBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := "[keybindings]\nshuffle = \"j\"\nmute = \"x\"\nrestart = \"home\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Keybindings.Shuffle != "j" || cfg.Keybindings.Mute != "x" || cfg.Keybindings.Restart != "home" {
		t.Fatalf("configured controls were not loaded: %+v", cfg.Keybindings)
	}
	if cfg.Keybindings.Vinyl != "v" || cfg.Keybindings.Repeat != "c" {
		t.Fatalf("omitted controls lost their defaults: %+v", cfg.Keybindings)
	}
}
