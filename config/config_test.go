package config

import (
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
