package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.toml")
	want := State{
		Volume:  0.37,
		Muted:   true,
		EQ:      [3]float64{0.4, 1.3, 1.9},
		Shuffle: true,
		Vinyl:   true,
		Repeat:  "one",
	}
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

// TestStateEachFieldSurvivesSeparately changes one setting at a time so a
// regression in any single field is unambiguous.
func TestStateEachFieldSurvivesSeparately(t *testing.T) {
	base := DefaultState()
	cases := []struct {
		name   string
		mutate func(*State)
	}{
		{"volume", func(s *State) { s.Volume = 0.21 }},
		{"muted", func(s *State) { s.Muted = true }},
		{"eq", func(s *State) { s.EQ = [3]float64{0.3, 1.4, 2.0} }},
		{"shuffle", func(s *State) { s.Shuffle = true }},
		{"vinyl", func(s *State) { s.Vinyl = true }},
		{"repeat", func(s *State) { s.Repeat = "off" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := base
			tc.mutate(&want)
			path := filepath.Join(t.TempDir(), "state.toml")
			if err := SaveState(path, want); err != nil {
				t.Fatal(err)
			}
			got, err := LoadState(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("field %s did not survive: got %+v, want %+v", tc.name, got, want)
			}
		})
	}
}

func TestLoadStateMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "state.toml")
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("missing state returned error: %v", err)
	}
	if got != DefaultState() {
		t.Fatalf("missing state = %+v, want defaults %+v", got, DefaultState())
	}
}

func TestLoadStateCorruptFileReturnsDefaultsAndError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.toml")
	if err := os.WriteFile(path, []byte("volume = \"loud\"\neq = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err == nil {
		t.Fatal("corrupt state did not return an error")
	}
	if got != DefaultState() {
		t.Fatalf("corrupt state = %+v, want defaults %+v", got, DefaultState())
	}
}

func TestLoadStateNormalizesOutOfRangeValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.toml")
	data := "volume = 5.0\neq = [9.0, -1.0, 1.0]\nrepeat = \"bogus\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultState()
	want.Volume = 1
	want.EQ = [3]float64{2, 0, 1}
	want.Repeat = "all"
	if got != want {
		t.Fatalf("normalized state = %+v, want %+v", got, want)
	}
}

func TestStatePathSitsBesideConfig(t *testing.T) {
	got := StatePath("/home/user/.config/vynl/config.toml")
	want := "/home/user/.config/vynl/state.toml"
	if got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
}

func TestSaveStateCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "state.toml")
	if err := SaveState(path, DefaultState()); err != nil {
		t.Fatalf("SaveState did not create the parent directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not written: %v", err)
	}
}
