package config

import (
	"errors"
	"os"
	"path/filepath"

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
	VolumeDown  string `toml:"volume_down"`
	VolumeUp    string `toml:"volume_up"`
	SpeedDown   string `toml:"speed_down"`
	SpeedUp     string `toml:"speed_up"`
	PitchDown   string `toml:"pitch_down"`
	PitchUp     string `toml:"pitch_up"`
	Reset       string `toml:"reset"`
	Search      string `toml:"search"`
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
			VolumeDown: "down", VolumeUp: "up", SpeedDown: "[", SpeedUp: "]",
			PitchDown: "{", PitchUp: "}", Reset: "r", Search: "/", Quit: "q",
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
	if cfg.MusicDir == "" {
		cfg.MusicDir = Defaults().MusicDir
	}
	return cfg, nil
}
