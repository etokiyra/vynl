package config

import (
	"errors"
	"math"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// State is the small set of playback settings VYNL remembers across runs. It
// is written to a separate file beside the user's hand-edited config so that
// saving never rewrites their comments or formatting. It deliberately excludes
// ephemeral state such as the current track and playback position.
type State struct {
	Volume  float64    `toml:"volume"`
	Muted   bool       `toml:"muted"`
	EQ      [3]float64 `toml:"eq"`
	Shuffle bool       `toml:"shuffle"`
	Vinyl   bool       `toml:"vinyl"`
	Repeat  string     `toml:"repeat"`
}

// DefaultState mirrors the engine's built-in defaults.
func DefaultState() State {
	return State{Volume: 0.8, EQ: [3]float64{1, 1, 1}, Repeat: "all"}
}

// StatePath returns the state file that sits beside the config file, in the
// same resolved directory ($XDG_CONFIG_HOME/vynl/ or ~/.config/vynl/).
func StatePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "state.toml")
}

// LoadState reads persisted playback state. A missing file yields the defaults
// with no error. A malformed or unreadable file yields the defaults plus an
// error so the caller can print a warning; it never panics. Out-of-range values
// are clamped and an unknown repeat mode falls back to the default.
func LoadState(path string) (State, error) {
	state := DefaultState()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if err := toml.Unmarshal(data, &state); err != nil {
		return DefaultState(), err
	}
	state.normalize()
	return state, nil
}

// SaveState writes state atomically (temp file plus rename) so a crash or
// signal cannot leave a half-written file behind. The directory is created if
// needed.
func SaveState(path string, state State) error {
	state.normalize()
	data, err := toml.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.toml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *State) normalize() {
	s.Volume = clampState(s.Volume, 0, 1, DefaultState().Volume)
	for i := range s.EQ {
		s.EQ[i] = clampState(s.EQ[i], 0, 2, 1)
	}
	switch s.Repeat {
	case "off", "all", "one":
	default:
		s.Repeat = DefaultState().Repeat
	}
}

// clampState keeps value in [low, high], substituting fallback for NaN.
func clampState(value, low, high, fallback float64) float64 {
	if math.IsNaN(value) {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
