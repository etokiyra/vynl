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
		"repeat": keys.Repeat, "search": keys.Search, "quit": keys.Quit,
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
