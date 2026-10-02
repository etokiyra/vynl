package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	MusicDir    string      `toml:"music_dir"`
	Theme       Theme       `toml:"theme"`
	Keybindings Keybindings `toml:"keybindings"`
	Steps       Steps       `toml:"steps"`
}

type Theme struct {
	Foreground string `toml:"foreground"`
	Cyan       string `toml:"cyan"`
	Magenta    string `toml:"magenta"`
	Green      string `toml:"green"`
	Muted      string `toml:"muted"`
	Pink       string `toml:"pink"`
	Error      string `toml:"error"`
}

// Steps holds the increment each transport key applies. Values are in the
// action's own unit (seconds for seek, a 0–1 fraction for volume, a speed
// multiple for tempo, semitones for pitch, and EQ gain for the EQ keys).
type Steps struct {
	Seek   float64 `toml:"seek"`
	Volume float64 `toml:"volume"`
	Tempo  float64 `toml:"tempo"`
	Pitch  float64 `toml:"pitch"`
	EQ     float64 `toml:"eq"`
}

type Keybindings struct {
	Toggle      string `toml:"toggle"`
	Stop        string `toml:"stop"`
	Next        string `toml:"next"`
	Prev        string `toml:"previous"`
	SeekBack    string `toml:"seek_back"`
	SeekForward string `toml:"seek_forward"`
	Restart     string `toml:"restart"`
	VolumeDown  string `toml:"volume_down"`
	VolumeUp    string `toml:"volume_up"`
	Mute        string `toml:"mute"`
	SpeedDown   string `toml:"speed_down"`
	SpeedUp     string `toml:"speed_up"`
	PitchDown   string `toml:"pitch_down"`
	PitchUp     string `toml:"pitch_up"`
	Reset       string `toml:"reset"`
	Vinyl       string `toml:"vinyl"`
	Shuffle     string `toml:"shuffle"`
	Repeat      string `toml:"repeat"`
	Sort        string `toml:"sort"`
	Search      string `toml:"search"`
	Rescan      string `toml:"rescan"`
	NowPlaying  string `toml:"now_playing"`
	Quit        string `toml:"quit"`
	EQLow       string `toml:"eq_low"`
	EQMid       string `toml:"eq_mid"`
	EQHigh      string `toml:"eq_high"`
	EQGainDown  string `toml:"eq_gain_down"`
	EQGainUp    string `toml:"eq_gain_up"`
}

func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		MusicDir: filepath.Join(home, "Music"),
		Theme: Theme{
			Foreground: "#e8edf2",
			Cyan:       "#45e6dc",
			Magenta:    "#f05bd5",
			Green:      "#b7f36b",
			Muted:      "#78828e",
			Pink:       "#ff77c8",
			Error:      "#ff6b6b",
		},
		Keybindings: Keybindings{
			Toggle: " ", Stop: "s", Next: "n", Prev: "p", SeekBack: "left", SeekForward: "right",
			Restart: "0", VolumeDown: "down", VolumeUp: "up", Mute: "m", SpeedDown: "[", SpeedUp: "]",
			PitchDown: "{", PitchUp: "}", Reset: "r", Vinyl: "v", Shuffle: "z", Repeat: "c",
			Sort: "o", Search: "/", Rescan: "f5", NowPlaying: "g", Quit: "q",
			EQLow: "1", EQMid: "2", EQHigh: "3", EQGainDown: "-", EQGainUp: "+",
		},
		Steps: Steps{Seek: 5, Volume: 0.05, Tempo: 0.05, Pitch: 1, EQ: 0.1},
	}
}

func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "vynl", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vynl", "config.toml")
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.MusicDir = ExpandHome(cfg.MusicDir)
	if cfg.MusicDir == "" {
		cfg.MusicDir = Defaults().MusicDir
	}
	cfg.Steps = cfg.Steps.normalized()
	return cfg, nil
}

// normalized replaces a non-positive (or NaN) step with its default, so a
// misconfigured or omitted value can never make a transport key a no-op.
func (s Steps) normalized() Steps {
	defaults := Defaults().Steps
	if !(s.Seek > 0) {
		s.Seek = defaults.Seek
	}
	if !(s.Volume > 0) {
		s.Volume = defaults.Volume
	}
	if !(s.Tempo > 0) {
		s.Tempo = defaults.Tempo
	}
	if !(s.Pitch > 0) {
		s.Pitch = defaults.Pitch
	}
	if !(s.EQ > 0) {
		s.EQ = defaults.EQ
	}
	return s
}

// ExpandHome expands a leading "~" or "~/" to the user's home directory. Paths
// that do not start with "~" are returned unchanged.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
