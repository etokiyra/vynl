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
- 🎛️ **3-band EQ** — low / mid / high gain from a 3-way Linkwitz-Riley
  crossover (4th-order biquad sections; configurable crossover frequencies),
  with a soft limiter so EQ/volume boosts
  can't hard-clip; a flat EQ stays exactly transparent
- 🔀 **Shuffle, repeat & transport** — shuffle the play order, cycle repeat
  off / all / one, and mute, restart, or seek from the keyboard
- 🔗 **Gapless playback** — consecutive tracks run on a persistent output stream,
  so the next track starts in the same audio callback with no device restart or
  inserted silence; an optional equal-power **crossfade** (`crossfade_ms`)
  overlaps the end of one track with the start of the next
- 📝 **Playlists** — create, rename, delete, browse, and play M3U8 playlists
  stored beside your config. Add the playing track, the highlighted library
  track, or the whole filtered library; reorder and remove entries; and play
  through a playlist with the same gapless/crossfade engine and Next/Prev order
- 🕹️ **Live playback controls** — toggle crossfade and change its duration, and
  cycle ReplayGain mode / adjust the preamp, all while VYNL is running; tempo,
  pitch, volume, and EQ were already live
- 🩹 **Resilient playback** — a corrupt or unplayable file is skipped
  automatically (bounded by the library size) and the deck shows how many were
  passed over, so one bad file never stalls a session
- 📚 **Local library browser** — scans a folder for supported formats, reads
  tags off the UI thread (so startup is not blocked), fuzzy search, sortable
  browsing, a preview of the next track, and rescan without restarting
- 🔄 **Rescan library** — `F5` re-walks the music folder and reloads the track
  list in place (the currently playing track keeps playing if it is still
  present), streaming the new list's tags in through the same async path
- 🔊 **Stereo L/R level meters** and live track metadata (format, bitrate,
  sample rate, file size)
- 📈 **Optional ReplayGain** — normalize loudness from `replaygain_track_gain`/
  `replaygain_album_gain` tags (Vorbis comments and ID3v2 TXXX), with track or
  album mode and a preamp; off by default
- ⌨️ **Discoverable keymap** — context-sensitive footer hints plus a full-screen
  `?` keybinding reference
- 🖥️ **Responsive layout** — side-by-side deck and library on wide terminals, a
  stacked layout when narrow, and a compact view on tiny terminals
- 🖤 **Terminal-native transparency** — no hardcoded backgrounds; your
  terminal's own theme and opacity show through
- ⚙️ **Configurable** — music directory, foreground/accent colors, and
  keybindings via a TOML config file
- 💾 **Remembers your settings** — volume, mute, EQ, shuffle, vinyl, and repeat
  are restored on the next run from a separate `state.toml`

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

# Print version, toolchain, and platform
vynl -version
```

---

## Keybindings

| Key           | Action                          |
| ------------- | -------------------------------- |
| `Space`       | Play / pause                     |
| `S`           | Stop and return to start         |
| `N` / `P`     | Next / previous track            |
| `←` / `→`     | Seek back / forward 5s           |
| `↑` / `↓`     | Volume down / up 5% *(deck focus; browses the list otherwise)* |
| `[` / `]`     | Decrease / increase tempo 0.05   |
| `{` / `}`     | Decrease / increase pitch 1 semitone |
| `R`           | Reset tempo & pitch              |
| `V`           | Toggle vinyl mode                |
| `0`           | Restart current track from start |
| `M`           | Mute / unmute                    |
| `Z`           | Toggle shuffle                   |
| `C`           | Cycle repeat: off → all → one    |
| `1` / `2` / `3` | Select EQ band (low/mid/high; configurable) |
| `+` / `-`     | Adjust selected EQ band gain; `=` also boosts (configurable) |
| `Tab`         | Switch focus: library ↔ deck     |
| `/`           | Search library                   |
| `O`           | Cycle sort: path → title → artist → album |
| `G`           | Jump the library cursor to the playing track |
| `Home` / `End` | Jump to the first / last track in the list |
| `PgUp` / `PgDn` | Page up / down the track list       |
| `X`           | Toggle crossfade on / off         |
| `<` / `>`     | Crossfade duration down / up      |
| `J`           | Cycle ReplayGain: off → track → album |
| `,` / `.`     | ReplayGain preamp down / up       |
| `L`           | Switch the browser between the library and playlists |
| `A` / `a` / `Ctrl+A` | Add the highlighted / playing / all filtered track(s) to the open playlist |
| `Ctrl+N` / `Ctrl+R` / `D` | New / rename / delete playlist (delete asks first) |
| `Shift+↑` / `Shift+↓` | Reorder the selected playlist entry |
| `Esc`         | Close the open playlist / leave the playlist browser |
| `F5`          | Rescan the music folder                   |
| `?`           | Show the full keybinding help    |
| `Ctrl+C`      | Quit (always, even while searching) |
| `Q`           | Quit                              |

> `↑` / `↓` change volume while the deck has focus and browse the library (or
> the playlist browser) while it has focus; `←` / `→` always seek. While the
> search box is open, `↑` / `↓` move through the matching tracks. Tempo moves in
> 0.05× steps; pitch moves one semitone at a time; seek jumps 5 seconds; volume
> moves 5%. The crossfade and ReplayGain keys adjust live settings for the
> current session (the config file provides the startup defaults).

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
muted      = "#78828e"
pink       = "#ff77c8"
error      = "#ff6b6b"

[steps]
seek   = 5.0
volume = 0.05
tempo  = 0.05
pitch  = 1.0
eq     = 0.1
crossfade = 500.0
preamp    = 1.0

[eq]
low_hz  = 250.0
high_hz = 4000.0

[playback]
replaygain = "off"
preamp_db  = 0.0
crossfade_ms = 0

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
rescan        = "f5"
now_playing   = "g"
top           = "home"
bottom        = "end"
page_up       = "pgup"
page_down     = "pgdown"
search        = "/"
quit          = "q"
eq_low        = "1"
eq_mid        = "2"
eq_high       = "3"
eq_gain_down  = "-"
eq_gain_up    = "+"
crossfade_toggle = "x"
crossfade_down   = "<"
crossfade_up     = ">"
replaygain_cycle = "j"
preamp_down      = ","
preamp_up        = "."
playlists          = "l"
playlist_new       = "ctrl+n"
playlist_rename    = "ctrl+r"
playlist_delete    = "d"
playlist_add          = "a"
playlist_add_selected = "A"
playlist_add_all      = "ctrl+a"
playlist_move_up      = "shift+up"
playlist_move_down    = "shift+down"
```

Every colour VYNL paints is themeable (`foreground`, `cyan`, `magenta`, `green`,
`muted`, `pink`, `error`), and every action in the `[keybindings]` table above is
honored — including the EQ bands and gain. The `[steps]` table controls how far
each key moves: seek in seconds, volume as a 0–1 fraction, tempo as a speed
multiple, pitch in semitones, EQ gain in band units, crossfade in milliseconds,
and preamp in dB. A non-positive step
silently falls back to its default. VYNL never paints a background colour, so
there is no `background` key — transparency is intentional.

The `[eq]` table sets the 3-band tone control's crossover points: `low_hz` is
the low/mid boundary and `high_hz` the mid/high boundary (defaults 250 and
4000 Hz). Invalid, reversed, or out-of-band values fall back to the defaults.

The `[playback]` table controls optional loudness normalization. Set
`replaygain = "track"` (or `"album"` to prefer each file's album gain, falling
back to track gain) to apply the ReplayGain values stored in the file's tags;
`preamp_db` adds an overall offset (clamped to ±12 dB). The default `"off"`
leaves playback untouched. A file with no ReplayGain tags, or with malformed
values, plays normally. While normalization is active the deck shows `RG TRACK`
or `RG ALBUM` with the applied gain.

`crossfade_ms` overlaps consecutive tracks by that many milliseconds (0–30000;
0 keeps the default gapless join with no overlap). The fade uses an equal-power
envelope, is capped so short tracks are safe, and is applied whenever the next
track was successfully prefetched. The deck shows `XFADE <n>s` while it is on.

VYNL also remembers your volume, mute, EQ, shuffle, vinyl, and repeat settings
across runs. It writes them to a separate `state.toml` beside the config file
(same directory), so your hand-edited `config.toml` is never rewritten. The
state file is small TOML:

```toml
volume = 0.8
muted = false
eq = [1.0, 1.0, 1.0]
shuffle = false
vinyl = false
repeat = 'all'
```

A missing state file just uses the defaults; a malformed one is ignored with a
one-line warning and the defaults are used.

### Runtime settings

Crossfade, ReplayGain mode, and the ReplayGain preamp can be changed while VYNL
runs. These are **session settings**: the config file supplies the values loaded
at startup, and `state.toml` (volume, mute, EQ, shuffle, vinyl, repeat) is
unchanged. Crossfade defaults to `crossfade_ms` and can be toggled/adjusted with
`X` and `<`/`>`; ReplayGain cycles with `J` and the preamp moves with `,`/`.`.
Tempo, pitch, volume, and the EQ bands were already live. Changing a setting
applies to the current track, the prefetched next track, and any in-progress
crossfade without restarting playback.

---

## Playlists

Playlists are stored as extended M3U/M3U8 files in
`$XDG_CONFIG_HOME/vynl/playlists/` (or `~/.config/vynl/playlists/`), beside
`config.toml`. Each file is plain UTF-8 text: an `#EXTM3U` header, optional
`#EXTINF`/comment lines, and one audio path per line. Relative paths are
resolved against the playlist file's own directory, so a self-contained playlist
folder can be moved as a unit; paths outside it are stored absolute. Saving is
atomic (a temp file is renamed over the old one), and VYNL never modifies or
deletes your audio files.

Playlist files are treated as untrusted input: the entry count and line length
are bounded, comments/directives are ignored, `file://` URLs are accepted, names
are stripped of path separators, and entries that no longer resolve are kept and
shown as unavailable (opening one lets the normal auto-skip move to the next
playable track). Spaces and non-ASCII names are handled as written.

Press `L` to open the playlist browser, `Ctrl+N` to create a playlist, and
`Enter` to open or play. `A`/`a`/`Ctrl+A` add the highlighted library track, the
playing track, or the whole filtered library. `Shift+↑`/`Shift+↓` reorder
entries, `D` removes an entry (or deletes a playlist, after a confirmation), and
`Esc` goes back. Playing a playlist hands its tracks to the same engine the
library uses, so Next/Prev, repeat, shuffle, auto-skip, gapless joins, and
crossfade all follow the playlist order. Editing a playlist writes the file
immediately but does not disturb the track already playing (or its prefetched
next); the new order applies the next time the playlist is started. A library
rescan refreshes the library and returns playback to it (the playing file keeps
playing if it is still present).

---

## How it works

VYNL is built around a few core pieces:

- **`player/`** — the audio engine. Wraps format-specific decoders
  (`mp3`/`flac`/`wav`/`vorbis` via `beep`) behind a custom `transportStreamer`
  that applies live speed, pitch, volume, and EQ per sample and ends in a soft
  limiter. `pcm_ring.go` is the bounded, thread-safe decoded-PCM ring,
  `track_stream.go` bundles one track's resources, `queue.go` is the persistent
  streamer that upgrades a track boundary into a gapless (or crossfaded)
  promotion, `order.go` owns shuffle/repeat, and `spectrum.go` is the FFT
  analyzer.
- **`library/`** — walks the configured music directory (fast, metadata only)
  and reads tags in parallel off the UI thread, streaming the track list in as
  results arrive. A rescan (`F5`) re-walks in the background and streams the new
  list's tags the same way.
- **`ui/`** — the Bubble Tea model driving the deck, visualizer, vinyl
  animation, library browser, and playlist browser, all synced to a single tick
  loop.
- **`playlist/`** — loads and saves extended M3U/M3U8 playlists (atomic writes,
  bounded parsing, relative-path handling) and validates entry paths; the UI
  owns the in-memory playlist state and hands a playlist to the engine as its
  track list.
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

- Gapless transitions are always on, but only a successfully prefetched next
  track is gapless (or crossfaded). A corrupt next track, or the end of the play
  order with repeat off, falls back to the hard track-load path and can insert a
  short gap. Crossfade is off by default (`crossfade_ms = 0`) and uses a fixed
  equal-power curve
- Decoded audio is held in a fixed ~4-second ring (about 1.35 MiB) instead of
  the whole track, so long files no longer use significant RAM. The tradeoff is
  that a distant seek waits for the decoder to reposition and refill, which
  shows as brief buffering; seeks inside the resident window are instant
- The 3-band EQ uses fixed-slope Linkwitz-Riley crossovers (the crossover
  frequencies are configurable via `[eq] low_hz`/`high_hz`, but the slope is
  fixed); it is not a parametric EQ
- ReplayGain is optional and off by default. It uses the ReplayGain gain tags
  (not EBU R128/Opus loudness tags), album mode reads each file's own album-gain
  tag rather than aggregating a library, and boosted tracks rely on the soft
  limiter rather than peak-based clipping prevention
- Playlists are M3U8 files played by handing their tracks to the engine as the
  active list, so a library rescan returns playback to the library. Entries are
  shown, not removed, when they go missing, and the same track may appear more
  than once on purpose. Crossfade and ReplayGain changes are session-only; only
  volume, mute, EQ, shuffle, vinyl, and repeat are persisted
- Untrusted tag metadata and filenames are stripped of control characters and
  length-bounded before display, so terminal escape sequences in tags or paths
  are neutralised rather than rendered
- Volume, mute, EQ, shuffle, vinyl, and repeat are saved to a separate
  `state.toml` and restored on the next run; a corrupt state file falls back to
  defaults with a warning. The current track and playback position are not
  restored
- The startup directory walk runs before the UI appears (metadata only, no file
  opens); tag reading is off the UI thread, so the list shows filename titles
  first and fills in metadata live with a `SCANNING` indicator. `F5` rescans
  with the walk off the UI thread and the tags streaming through the same async
  path. A rescan replaces the whole track list at once (it is not incremental);
  if it finds no files, or the walk fails, the current library and playback are
  kept and a notice is shown
- Unreadable folders inside the music directory are skipped during the scan;
  the rest of the library still loads
- Extreme speed/pitch settings can introduce minor artifacts on
  percussion-heavy material, due to the grain-based resampling approach

---

## Roadmap

VYNL plays today; these are the next meaningful steps, roughly in priority
order.

### Near term

- [x] **Bound the PCM memory** — decoded audio now lives in a fixed 4-second
      ring (~1.35 MiB); the decoder is a flow-controlled producer that re-seeks
      on out-of-window jumps, so multi-hour files no longer use gigabytes of RAM
- [x] **Persist playback state** — volume, mute, EQ, shuffle, vinyl, and repeat
      are saved to a separate `state.toml` and restored across runs
- [x] **Async library scan** — tag reading runs off the UI thread and streams
      into the list with a `SCANNING` progress indicator, so startup is not
      blocked by metadata parsing
- [x] **Rescan library** — `F5` re-walks the music folder and reloads the track
      list without restarting, keeping the current track playing and streaming
      the new list's tags in asynchronously
- [x] **Auto-skip corrupt tracks** — an unplayable file is skipped
      automatically (bounded by the library size) and the deck reports how many
      were passed over, so one bad file never stalls a session

### Quality

- [x] **Biquad/shelving EQ** — the 3-band EQ now uses a 3-way Linkwitz-Riley
      crossover (4th-order biquad sections) for much steeper, more musical tone
      control while keeping a flat EQ transparent
- [x] **Loudness normalization** — optional ReplayGain track/album gain from
      Vorbis/ID3 tags, with a preamp and off by default; malformed or missing
      tags are ignored per track
- [x] **Fully configurable keys and steps** — the EQ keys and the
      seek/volume/tempo/pitch/EQ step sizes are exposed via `[keybindings]` and
      `[steps]`
- [x] **Complete theming** — `muted`, `pink`, and `error` are themeable, so
      every colour VYNL paints is configurable; the ignored `background` key was
      removed (VYNL never paints a background)

### Features

- [x] **Gapless & crossfade** — consecutive tracks share a persistent output
      stream (no device restart or inserted silence); an optional equal-power
      crossfade overlaps tracks via `crossfade_ms`
- [x] **Playlists** — M3U8 playlists stored beside the config, with create /
      rename / delete / add / remove / reorder and playback through the same
      gapless/crossfade engine
- [x] **Runtime playback controls** — crossfade and ReplayGain can be changed
      without editing a file or restarting
- [ ] Per-track duration in the library list
- [ ] AUR / Homebrew packaging

---

## Development

```bash
go build -o vynl .      # build the binary
go test ./...           # unit tests for every package
go test -race ./...     # required when touching audio/buffer code
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...  # static analysis
go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # dependency audit
```

There is no CI or Makefile; each package owns its tests. The transport is
required to stay allocation-free in steady-state playback, which a test
enforces. `govulncheck` currently reports no vulnerabilities reachable from
VYNL's code (one module-level `golang.org/x/sys` advisory is Windows-only and
not imported). `vynl -version` reports the release string, Go toolchain, platform, and
VCS revision; packagers can stamp a release with
`go build -ldflags "-X main.version=v1.2.3"`. See [`AGENTS.md`](AGENTS.md) for
the architecture and concurrency rules.

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
