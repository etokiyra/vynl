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
