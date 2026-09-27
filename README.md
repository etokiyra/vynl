<div align="center">

# 🎛️ VYNL

**A terminal music player styled like an analog DJ console.**

*Local audio playback, live tempo & pitch control, real-time visualization — all in your terminal.*

![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?style=flat-square&logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue?style=flat-square)
![License](https://img.shields.io/badge/license-MIT-lightgrey?style=flat-square)
![Status](https://img.shields.io/badge/status-active-brightgreen?style=flat-square)

</div>

---

## Overview

**VYNL** turns your terminal into a live audio console. It scans a local music
folder, loads MP3/FLAC/WAV/OGG files, and gives you a deck-style interface with
a spinning vinyl, a reactive amplitude visualizer, live tempo/pitch/volume
control, and a 3-band EQ — no GUI, no browser, no bloat. Built with
[Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[beep](https://github.com/gopxl/beep), and designed to look right at home in
transparent terminals like kitty, foot, or Alacritty.

<div align="center">

*[ screenshot / gif of the deck in action goes here ]*

</div>

---

## Features

- 🎚️ **Live tempo & pitch control** — independent speed and pitch shifting,
  with an optional "vinyl mode" that couples them for a classic
  slow-down/speed-up effect
- 💿 **Animated vinyl deck** — a genuinely circular, rotating record rendered
  from real coordinate geometry, not ASCII art templates
- 📊 **Reactive amplitude visualizer** — a dense, gradient bar spectrum that
  responds to the actual audio buffer in real time
- 🎛️ **3-band EQ** — low / mid / high gain control, driven by lightweight
  single-pole filters suited for real-time playback
- 📚 **Local library browser** — scans a folder for supported formats, reads
  tags, fuzzy search, instant load
- 🔊 **Stereo L/R level meters** and live track metadata (format, bitrate,
  sample rate, file size)
- 🖤 **Terminal-native transparency** — no hardcoded backgrounds; your
  terminal's own theme and opacity show through
- ⚙️ **Fully configurable** — theme colors and keybindings via a TOML config
  file

---

## Installation

```bash
git clone https://github.com/etokiyra/vynl.git
cd vynl
go build -o vynl .
```

Or run directly without building a binary:

```bash
go run .
```

---

## Usage

```bash
# Uses ~/Music by default
vynl

# Point at a specific library
vynl -music-dir /path/to/music

# Use a specific config file
vynl -config /path/to/config.toml
```

---

## Keybindings

| Key           | Action                          |
| ------------- | -------------------------------- |
| `Space`       | Play / pause                     |
| `S`           | Stop and return to start         |
| `N` / `P`     | Next / previous track            |
| `←` / `→`     | Seek backward / forward          |
| `↑` / `↓`     | Volume up / down *(deck focus)*  |
| `[` / `]`     | Decrease / increase tempo        |
| `{` / `}`     | Decrease / increase pitch        |
| `R`           | Reset transport to defaults      |
| `V`           | Toggle vinyl mode                |
| `1` / `2` / `3` | Select EQ band (low/mid/high)  |
| `+` / `-`     | Adjust gain on selected EQ band  |
| `Tab`         | Switch focus: library ↔ deck     |
| `/`           | Search library                   |
| `Q`           | Quit                              |

> Arrow keys browse the library list while it has focus; on the deck, the
> same keys control volume and seeking instead.

---

## Configuration

VYNL reads an optional TOML config from `$XDG_CONFIG_HOME/vynl/config.toml`
or `~/.config/vynl/config.toml`:

```toml
music_dir = "/home/me/Music"

[theme]
background = "#101318"
foreground = "#e8edf2"
cyan       = "#45e6dc"
magenta    = "#f05bd5"
green      = "#b7f36b"

[keybindings]
toggle        = " "
stop          = "s"
next          = "n"
previous      = "p"
seek_back     = "left"
seek_forward  = "right"
volume_down   = "down"
volume_up     = "up"
speed_down    = "["
speed_up      = "]"
pitch_down    = "{"
pitch_up      = "}"
reset         = "r"
search        = "/"
quit          = "q"
```

---

## How it works

VYNL is built around a few core pieces:

- **`player/`** — the audio engine. Wraps format-specific decoders
  (`mp3`/`flac`/`wav`/`vorbis` via `beep`) behind a custom
  `transportStreamer` that applies live speed, pitch, volume, and EQ
  per-sample during playback.
- **`library/`** — scans the configured music directory, reads tags, and
  builds the track list the UI browses.
- **`ui/`** — the Bubble Tea model driving the deck, visualizer, vinyl
  animation, and library browser, all synced to a single tick loop.
- **`config/`** — TOML config loading with sensible defaults when no config
  file is present.

Tempo changes preserve pitch by default; vinyl mode couples the two for a
classic turntable effect. The transport streamer uses overlapping grains with
a short correlation search at grain joins to reduce artifacts — extreme
tempo/pitch combinations on transient-heavy material may still introduce
some coloration, which is an inherent tradeoff of this technique rather than
a bug.

---

## Known limitations

- Very large libraries (hundreds+ of tracks) may cause a noticeable pause on
  startup during tag scanning
- Extreme speed/pitch settings can introduce minor artifacts on
  percussion-heavy material, due to the grain-based resampling approach

---

## Roadmap

- [ ] Crossfade / gapless transitions between tracks
- [ ] Playlist support
- [ ] Async library scanning with a progress indicator
- [ ] AUR / Homebrew packaging

---

## Contributing

Issues and PRs are welcome. If you're proposing a UI change, a screenshot or
terminal recording helps a lot given how much of this project lives in
rendering details.

---

## License

MIT — see [LICENSE](LICENSE).

<div align="center">

*Built for terminals, not for windows.*

</div>
