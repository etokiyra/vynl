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
}

type Theme struct {
	Background string `toml:"background"`
	Foreground string `toml:"foreground"`
	Cyan       string `toml:"cyan"`
	Magenta    string `toml:"magenta"`
	Green      string `toml:"green"`
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
	Quit        string `toml:"quit"`
}

func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		MusicDir: filepath.Join(home, "Music"),
		Theme: Theme{
			Background: "#101318",
			Foreground: "#e8edf2",
			Cyan:       "#45e6dc",
			Magenta:    "#f05bd5",
			Green:      "#b7f36b",
		},
		Keybindings: Keybindings{
			Toggle: " ", Stop: "s", Next: "n", Prev: "p", SeekBack: "left", SeekForward: "right",
			Restart: "0", VolumeDown: "down", VolumeUp: "up", Mute: "m", SpeedDown: "[", SpeedUp: "]",
			PitchDown: "{", PitchUp: "}", Reset: "r", Vinyl: "v", Shuffle: "z", Repeat: "c",
			Sort: "o", Search: "/", Rescan: "f5", Quit: "q",
		},
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
	return cfg, nil
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
