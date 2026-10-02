<div align="center">

# 🎛️ VYNL

**A terminal music player styled like an analog DJ console.**

*Local audio playback, live tempo & pitch control, real-time visualization — all in your terminal.*

![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?style=flat-square&logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue?style=flat-square)
![Status](https://img.shields.io/badge/status-active-brightgreen?style=flat-square)

</div>

---

## Overview

**VYNL** turns your terminal into a live audio console. It scans a local music
folder, loads MP3/FLAC/WAV/OGG files, and gives you a deck-style interface with
a spinning vinyl, a live spectrum analyzer, live tempo/pitch/volume control, and
a 3-band EQ — no GUI, no browser, no bloat. Built with
[Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[beep](https://github.com/gopxl/beep), and designed to look right at home in
transparent terminals like kitty, foot, or Alacritty.

<div align="center">

</div>

---

## Features

- 🎚️ **Live tempo & pitch control** — independent speed and pitch shifting,
  with an optional "vinyl mode" that couples them for a classic
  slow-down/speed-up effect
- 💿 **Animated vinyl deck** — a genuinely circular, rotating record rendered
  from real coordinate geometry, not ASCII art templates
- 📊 **Live spectrum analyzer** — a real 1024-point FFT of the output
  (Hann-windowed, mapped to 48 log-spaced bands) with peak-hold markers drives
  the visualizer; the stereo L/R VU meters and numeric level readouts are
  signal-derived separately
- 🎛️ **3-band EQ** — low / mid / high gain control built from lightweight
  single-pole filters, with a soft limiter so EQ/volume boosts can't hard-clip
- 🔀 **Shuffle, repeat & transport** — shuffle the play order, cycle repeat
  off / all / one, and mute, restart, or seek from the keyboard
- 📚 **Local library browser** — scans a folder for supported formats, reads
  tags, fuzzy search, sortable browsing, and a preview of the next track
- 🔊 **Stereo L/R level meters** and live track metadata (format, bitrate,
  sample rate, file size)
- ⌨️ **Discoverable keymap** — context-sensitive footer hints plus a full-screen
  `?` keybinding reference
- 🖥️ **Responsive layout** — side-by-side deck and library on wide terminals, a
  stacked layout when narrow, and a compact view on tiny terminals
- 🖤 **Terminal-native transparency** — no hardcoded backgrounds; your
  terminal's own theme and opacity show through
- ⚙️ **Configurable** — music directory, foreground/accent colors, and
  keybindings via a TOML config file

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

VYNL requires a Go toolchain matching the `go` directive in `go.mod`.

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
| `←` / `→`     | Seek back / forward 5s           |
| `↑` / `↓`     | Volume down / up 5% *(deck focus; browses the library otherwise)* |
| `[` / `]`     | Decrease / increase tempo 0.05   |
| `{` / `}`     | Decrease / increase pitch 1 semitone |
| `R`           | Reset tempo & pitch              |
| `V`           | Toggle vinyl mode                |
| `0`           | Restart current track from start |
| `M`           | Mute / unmute                    |
| `Z`           | Toggle shuffle                   |
| `C`           | Cycle repeat: off → all → one    |
| `1` / `2` / `3` | Select EQ band (low/mid/high)  |
| `+` / `-`     | Adjust selected EQ band gain (0.1); `=` also boosts |
| `Tab`         | Switch focus: library ↔ deck     |
| `/`           | Search library                   |
| `O`           | Cycle sort: path → title → artist → album |
| `?`           | Show the full keybinding help    |
| `Q`           | Quit                              |

> `↑` / `↓` change volume while the deck has focus and browse the library while
> it has focus; `←` / `→` always seek. While the search box is open, `↑` / `↓`
> move through the matching tracks. Tempo moves in 0.05× steps; pitch moves one
> semitone at a time; seek jumps 5 seconds; volume moves 5%.

---

## Configuration

VYNL reads an optional TOML config from `$XDG_CONFIG_HOME/vynl/config.toml`
or `~/.config/vynl/config.toml`. A leading `~` in `music_dir` is expanded to
your home directory, so `music_dir = "~/Music"` works as expected.

```toml
music_dir = "~/Music"

[theme]
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
restart       = "0"
volume_down   = "down"
volume_up     = "up"
mute          = "m"
speed_down    = "["
speed_up      = "]"
pitch_down    = "{"
pitch_up      = "}"
reset         = "r"
vinyl         = "v"
shuffle       = "z"
repeat        = "c"
sort          = "o"
search        = "/"
quit          = "q"
```

Only `foreground`, `cyan`, `magenta`, and `green` are used for theming. VYNL
never paints a background (transparency is intentional), some greys/pinks are
fixed, and keys that VYNL does not recognize are accepted but ignored. Every
`[keybindings]` entry above is honored except the fixed EQ keys (`1`/`2`/`3` and
`+`/`-`/`=`).

---

## How it works

VYNL is built around a few core pieces:

- **`player/`** — the audio engine. Wraps format-specific decoders
  (`mp3`/`flac`/`wav`/`vorbis` via `beep`) behind a custom `transportStreamer`
  that applies live speed, pitch, volume, and EQ per sample and ends in a soft
  limiter. `pcm_buffer.go` is the thread-safe PCM store, `order.go` owns
  shuffle/repeat, and `spectrum.go` is the FFT analyzer.
- **`library/`** — scans the configured music directory, reads tags in
  parallel, and builds the track list the UI browses.
- **`ui/`** — the Bubble Tea model driving the deck, visualizer, vinyl
  animation, and library browser, all synced to a single tick loop.
- **`config/`** — TOML config loading with sensible defaults when no config
  file is present.

Tempo changes preserve pitch by default; vinyl mode couples the two for a
classic turntable effect. The transport streamer uses overlapping grains with
a short correlation search at grain joins to reduce artifacts — extreme
tempo/pitch combinations on transient-heavy material may still introduce
some coloration, which is an inherent tradeoff of this technique rather than
a bug. A self-contained FFT analyzer turns the output into the visualizer
spectrum, and the engine and UI communicate only through non-blocking
command/status channels.

---

## Known limitations

- The full decoded audio for the current track is kept in memory, so very long
  files (multi-hour mixes) use a significant amount of RAM
- The 3-band EQ is two one-pole low-passes (with a soft limiter); it is
  effective but gentle, and gentler than biquad shelving filters would be
- Volume, mute, EQ, shuffle, vinyl, and repeat are not persisted between runs
- Startup tag scanning runs before the UI appears; it is parallelized, but
  extremely large libraries may still pause briefly
- Unreadable folders inside the music directory are skipped during the scan;
  the rest of the library still loads
- Extreme speed/pitch settings can introduce minor artifacts on
  percussion-heavy material, due to the grain-based resampling approach
- The EQ band keys (`1`/`2`/`3`) and gain keys (`+`/`-`/`=`) are hardcoded; the
  rest of the keymap is configurable

---

## Roadmap

VYNL plays today; these are the next meaningful steps, roughly in priority
order.

### Near term

- [ ] **Bound the PCM memory** — replace the unbounded per-track buffer with a
      seekable ring that re-seeks the decoder on backward jumps, so multi-hour
      files no longer use gigabytes of RAM
- [ ] **Persist playback state** — remember volume, mute, EQ, shuffle, vinyl,
      and repeat across runs
- [ ] **Async library scan** — read tags off the UI thread with a progress
      indicator instead of blocking startup
- [ ] **Rescan library** — reload the track list without restarting
- [ ] **Auto-skip corrupt tracks** — advance past files that fail to decode
      instead of stalling

### Quality

- [ ] **Biquad/shelving EQ** — replace the two one-pole filters with proper
      shelving filters for more musical tone control
- [ ] **Loudness normalization** — optional ReplayGain-style per-track gain for
      consistent levels
- [ ] **Fully configurable keys and steps** — expose the EQ keys and the
      seek/volume/tempo/pitch step sizes
- [ ] **Complete theming** — wire up (or remove) the currently ignored
      `background` key and make the remaining fixed greys/pinks themeable

### Features

- [ ] Crossfade / gapless transitions between tracks
- [ ] Playlist support
- [ ] Per-track duration in the library list
- [ ] AUR / Homebrew packaging

---

## Development

```bash
go build -o vynl .      # build the binary
go test ./...           # unit tests for every package
go test -race ./...     # required when touching audio/buffer code
go vet ./...
```

There is no CI or Makefile; each package owns its tests. The transport is
required to stay allocation-free in steady-state playback, which a test
enforces. See [`AGENTS.md`](AGENTS.md) for the architecture and concurrency
rules.

---

## Contributing

Issues and PRs are welcome. If you're proposing a UI change, a screenshot or
terminal recording helps a lot given how much of this project lives in
rendering details.

---

## License

GPLv3 — see [LICENSE](LICENSE).

<div align="center">

*Built for terminals, not for windows.*

</div>
