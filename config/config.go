package config

import (
	"errors"
	"math"
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
	Playback    Playback    `toml:"playback"`
	EQ          EQ          `toml:"eq"`
}

// EQ configures the 3-band tone control's crossover points. LowHz is the
// low/mid boundary and HighHz is the mid/high boundary.
type EQ struct {
	LowHz  float64 `toml:"low_hz"`
	HighHz float64 `toml:"high_hz"`
}

// Playback holds optional audio adjustments. ReplayGain is a loudness
// normalization mode: "off" (default), "track", or "album" (prefer the album
// gain tag, falling back to the track gain). PreampDB is applied on top of the
// chosen gain. CrossfadeMS is the overlap between consecutive tracks (0
// disables crossfade and keeps only the gapless join).
type Playback struct {
	ReplayGain  string  `toml:"replaygain"`
	PreampDB    float64 `toml:"preamp_db"`
	CrossfadeMS int     `toml:"crossfade_ms"`
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
	// Crossfade is the runtime crossfade step in milliseconds; Preamp is the
	// ReplayGain preamp step in dB.
	Crossfade float64 `toml:"crossfade"`
	Preamp    float64 `toml:"preamp"`
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
	Top         string `toml:"top"`
	Bottom      string `toml:"bottom"`
	PageUp      string `toml:"page_up"`
	PageDown    string `toml:"page_down"`
	Quit        string `toml:"quit"`
	EQLow       string `toml:"eq_low"`
	EQMid       string `toml:"eq_mid"`
	EQHigh      string `toml:"eq_high"`
	EQGainDown  string `toml:"eq_gain_down"`
	EQGainUp    string `toml:"eq_gain_up"`
	// Runtime controls and playlist actions. These act on live engine state
	// (or the playlist browser) and are not persisted to state.toml.
	CrossfadeToggle     string `toml:"crossfade_toggle"`
	CrossfadeDown       string `toml:"crossfade_down"`
	CrossfadeUp         string `toml:"crossfade_up"`
	ReplayGainCycle     string `toml:"replaygain_cycle"`
	PreampDown          string `toml:"preamp_down"`
	PreampUp            string `toml:"preamp_up"`
	Playlists           string `toml:"playlists"`
	PlaylistNew         string `toml:"playlist_new"`
	PlaylistRename      string `toml:"playlist_rename"`
	PlaylistDelete      string `toml:"playlist_delete"`
	PlaylistAdd         string `toml:"playlist_add"`
	PlaylistAddSelected string `toml:"playlist_add_selected"`
	PlaylistAddAll      string `toml:"playlist_add_all"`
	PlaylistMoveUp      string `toml:"playlist_move_up"`
	PlaylistMoveDown    string `toml:"playlist_move_down"`
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
			Top: "home", Bottom: "end", PageUp: "pgup", PageDown: "pgdown",
			EQLow: "1", EQMid: "2", EQHigh: "3", EQGainDown: "-", EQGainUp: "+",
			CrossfadeToggle: "x", CrossfadeDown: "<", CrossfadeUp: ">",
			ReplayGainCycle: "j", PreampDown: ",", PreampUp: ".",
			Playlists: "l", PlaylistNew: "ctrl+n", PlaylistRename: "ctrl+r",
			PlaylistDelete: "d", PlaylistAdd: "a", PlaylistAddSelected: "A",
			PlaylistAddAll: "ctrl+a", PlaylistMoveUp: "shift+up", PlaylistMoveDown: "shift+down",
		},
		Steps:    Steps{Seek: 5, Volume: 0.05, Tempo: 0.05, Pitch: 1, EQ: 0.1, Crossfade: 500, Preamp: 1},
		Playback: Playback{ReplayGain: "off"},
		EQ:       EQ{LowHz: 250, HighHz: 4000},
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
	cfg.Playback = cfg.Playback.normalized()
	cfg.EQ = cfg.EQ.normalized()
	return cfg, nil
}

// normalized clamps the crossover points into the audible band and keeps a
// sensible gap between them. Anything invalid (non-positive, NaN, reversed, or
// too close together) falls back to the defaults.
func (e EQ) normalized() EQ {
	defaults := Defaults().EQ
	if math.IsNaN(e.LowHz) || e.LowHz <= 0 {
		e.LowHz = defaults.LowHz
	}
	if math.IsNaN(e.HighHz) || e.HighHz <= 0 {
		e.HighHz = defaults.HighHz
	}
	e.LowHz = math.Max(20, math.Min(4000, e.LowHz))
	e.HighHz = math.Max(200, math.Min(20000, e.HighHz))
	if e.LowHz*2 > e.HighHz {
		return defaults
	}
	return e
}

// normalized canonicalizes the ReplayGain mode and clamps the preamp, so a
// mistyped mode silently disables normalization and an extreme preamp cannot
// dominate the output.
func (p Playback) normalized() Playback {
	switch strings.ToLower(strings.TrimSpace(p.ReplayGain)) {
	case "track":
		p.ReplayGain = "track"
	case "album":
		p.ReplayGain = "album"
	default:
		p.ReplayGain = "off"
	}
	if math.IsNaN(p.PreampDB) {
		p.PreampDB = 0
	}
	p.PreampDB = math.Max(-12, math.Min(12, p.PreampDB))
	if p.CrossfadeMS < 0 {
		p.CrossfadeMS = 0
	}
	if p.CrossfadeMS > maxCrossfadeMS {
		p.CrossfadeMS = maxCrossfadeMS
	}
	return p
}

// maxCrossfadeMS bounds the crossfade so a typo cannot create an hour-long
// overlap. It also bounds the extra memory a fade holds (the outgoing and
// incoming rings).
const maxCrossfadeMS = 30000

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
	if !(s.Crossfade > 0) {
		s.Crossfade = defaults.Crossfade
	}
	if !(s.Preamp > 0) {
		s.Preamp = defaults.Preamp
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
