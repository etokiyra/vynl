# vynl

A terminal music player with a compact DJ-deck interface. It scans MP3, FLAC, WAV, and OGG files, reads common tags, and uses beep for decoding and audio output.

Run with the default `~/Music` folder or pass a library path:

```sh
go run .
go run . -music-dir /path/to/music
go run . -config /path/to/config.toml
```

The optional TOML config is read from `$XDG_CONFIG_HOME/vynl/config.toml` or `~/.config/vynl/config.toml` by default:

```toml
music_dir = "/home/me/Music"

[theme]
background = "#101318"
foreground = "#e8edf2"
cyan = "#45e6dc"
magenta = "#f05bd5"
green = "#b7f36b"

[keybindings]
toggle = " "
stop = "s"
next = "n"
previous = "p"
seek_back = "left"
seek_forward = "right"
volume_down = "down"
volume_up = "up"
speed_down = "["
speed_up = "]"
pitch_down = "{"
pitch_up = "}"
reset = "r"
search = "/"
quit = "q"
```

Use Tab to switch between the library and deck. Arrow keys browse the library while it has focus; on the deck, Up/Down changes volume. Space toggles playback, S stops and returns to the start, N/P changes tracks, Left/Right seeks, `[`/`]` changes tempo, `{`/`}` changes pitch, R resets transport, and V toggles vinyl mode. Select an EQ band with 1/2/3 and change its gain with +/-.

Tempo changes preserve pitch by default. Vinyl mode couples pitch to tempo. The transport uses overlapping grains with a short correlation search at joins, so extreme tempo/pitch combinations can introduce artifacts on transient-heavy material.