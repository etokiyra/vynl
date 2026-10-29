# AGENTS.md

Guidelines for AI agents (and humans) working on VYNL, a terminal music player
styled like an analog DJ console. Read this before touching `player/`.

## What VYNL is

Go module `github.com/etokiyra/vynl`. Bubble Tea TUI on top of a custom audio
engine that wraps [beep](https://github.com/gopxl/beep) decoders. See
[README.md](README.md) for user-facing features, keybindings and configuration.

```
main.go        wiring: config/state load, library scan, engine start, TUI run
config/        TOML config + persisted state.toml
library/       music-folder walk, parallel async tag reading, ReplayGain tags
playlist/      M3U8 load/save (atomic, bounded)
player/        the audio engine — the concurrency-sensitive core
ui/            Bubble Tea model (deck, library, playlists, visualizer)
```

## The audio engine (player/)

Data flows: decoder → `pcmRing` (bounded decoded-PCM ring) →
`transportStreamer` (grain-based speed/pitch + EQ + ReplayGain + volume, ends
in a soft limiter) → `queueStreamer` (persistent top-level streamer, promotes
consecutive tracks for gapless joins) → `beep.Ctrl` → output.

- `engine.go` — the engine goroutine owns all mutable engine state. Commands
  arrive on `commands` (non-blocking `Send`, buffered); status leaves on
  `updates` (single slot, latest wins). A tick (80 ms) runs `reconcile` +
  `publish`; commands do the same first.
- `queue.go` — `queueStreamer`: `primary`/`pending`/`fading` streams. The audio
  callback promotes `pending` at primary end (gapless) or overlaps it after a
  `crossfade` window. The engine learns about promotions via `reconcile`, which
  adopts the promoted stream, reprefetches, and drains `retired`.
- `pcm_ring.go` — bounded SPSC ring of decoded frames in *absolute output-frame
  coordinates*. Producer = background decoder goroutine per track; consumer =
  audio callback. Seek protocol: consumer bumps `seekGen` (in `ensure`), the
  producer repositions the decoder and commits with `commitSeek`; the consumer
  refuses to read while `seekAcked < seekGen` (this is the buffer-during-seek
  path).
- `track_stream.go` — per-track lifecycle: initial synchronous 1 s decode, then
  the producer goroutine decodes ahead under flow control. `close()` joins the
  producer and is the only place decoder/file teardown happens.
- `transport.go` — grain resampler (2048-frame grains, 512 overlap), 3-way
  Linkwitz-Riley EQ, ReplayGain gain, soft limiter, per-call RMS/peak/analyzer.
- `order.go` — shuffle/repeat play order; `plannedNext` is side-effect free.

### Concurrency rules (non-negotiable)

1. **`speaker.Lock` serializes queue and transport state.** The audio callback
   and the engine goroutine both mutate it; never touch
   `queueStreamer`/`transportStreamer`/`beep.Ctrl.Paused` fields outside the
   lock. `pcmRing` has its own `mu` and must never be locked under
   `speaker.Lock` order tricks — it is fine inside either, it just always
   serializes on `r.mu`.
2. **The audio callback never blocks and never joins a goroutine.** Anything it
   is done with (promoted primary, completed fade) is appended to `retired`;
   only `reconcileRetired` on the engine goroutine calls `stream.close()`,
   which joins the producer. Closing inside `Stream` would stall output.
3. **The engine goroutine owns teardown.** `close()` is not re-entrant-safe; it
   runs only from the engine goroutine, and engine teardown goes through
   `reconcileRetired` with a bounded join (`closeWithTimeout(slowCloseTimeout)`):
   a decoder stuck inside a slow call (seek-table rebuild, slow file) hands
   the join to a janitor goroutine so commands and switching never stall.
4. **Tag/file I/O never happens under `speaker.Lock`**
   (`applyReplayGain`, `configureStream`, `buildStream` pattern: choose
   streams under the lock, do the I/O without it, re-apply under the lock).
5. **`Enable`/`Paused` flips go through `e.control` under the lock**, so a
   simultaneous callback sees a coherent pause state.
6. **Steady-state playback is allocation-free.** `Stream` reuses
   `grainBuf`/`queue`/`pending`/`windowScratch` slices; tests enforce it
   (`TestStreamingTransportStreamsWithoutAllocating`). Do not introduce
   per-call allocations.
7. **`Engine.tracks` is a private clone** (`cloneTracks`); the UI mutates its
   own list asynchronously, so the engine must never share the backing array.
   Index-based fields in `Status` are only meaningful together with
   `Status.Revision`.

### Ring/seek invariants (regression-tested)

- `ringLookbackFrames > ringMargin` and
  `ringCapacity ≥ ringAheadWindow + ringMargin + ringChunkFrames`; both are
  panics at construction if violated.
- **Steady-state resident look-ahead is `capacity − lookback`
  (~11 s at the current 12 s/1 s constants) and must stay comfortably larger
  than the default skip step (`[steps] seek`, 5 s) plus `ringAheadWindow`.** A
  forward seek beyond the resident window cannot be served from the ring: it
  breaks into decoder reposition + refill buffering. `ensure()` decides this;
  `TestRingStaysResidentAcrossADefaultForwardSkip` guards it.
- **Forward repositions use decode-ahead, not `decoder.Seek`**
  (`trackStream.repositionSeek`): the sequential decoder keeps running under
  normal flow control until the ring covers the target, then `commitAfterDecode`
  acks with the window intact. Decoder repositioning is not uniformly cheap —
  a FLAC without a seek table decodes the whole file inside its `Seek` just to
  index frames (per seek, per fresh decoder) — so the producer decodes ahead
  for hops the ring can hold (`maxRepositionDiscard`, ~60 s), and falls back
  to the real reposition for backward or out-of-capacity targets.
  `TestForwardSeekRepositionsViaDecodeAhead` and
  `TestCommitAfterDecodeAckKeepsWindow` pin this.
- `freeSpaceLocked` is monotonic in `readHead`: a backward readHead move can
  never let the producer overwrite frames the consumer still needs.
- A seek requested by the consumer is answered by `commitSeek` or
  `commitAfterDecode` with the same generation; a newer `seekGen` means a
  stale commit is discarded and the consumer keeps waiting. The producer
  re-reads `pendingSeek` on every loop iteration, so progress never depends on
  receiving a wake.
- After EOF the producer stays alive (draining wakes) so backward/restart
  seeks can re-enter decoding. `ringView.pastEnd` (finished ring, window
  beyond its extent) tells the transport a buffering wait is really track end;
  an *empty* ring is not past end — it is a fresh/seeked window the producer
  can still fill.

### Gapless/crossfade rules

- Only a successfully *prefetched* `pending` is gapless (or crossfaded);
  anything else falls back to `advanceTrack`'s bounded skip path (hard load).
  Do not remove the fallback; it is what a corrupt next track needs.
- `streamGapless` must clear the stale `exhausted` flag when it promotes, and
  `advanceTrack` must re-check `queue.pending` before hard-loading: the
  callback can set exhausted while a prefetch is still being built, and a hard
  load then would discard already-decoded audio and break the join.
  `TestEngineAdvanceTrackDoesNotDiscardArrivedPrefetch` guards this.
- Crossfade state (`fading`, `fadePos`, `fadeTotal`) lives in the queue and is
  reset by `cancelFade`/`setPrimary`/`clear`; the engine's runtime crossfade
  knobs mirror into `q.crossfade` under the lock (`setCrossfadeMS`).

## Conventions

- Comments explain *why* and state the invariant or contract; public behavior
  is documented in README.md, not duplicated here.
- Every package owns its tests; name tests after the behavior they pin.
- Run the full check before finishing work that touches `player/`:

```bash
go build ./...
go vet ./...
go test -race ./...
```

`-race` is mandatory for audio/buffer code. staticcheck/govulncheck are
documented in README.md under Development.

## Known sharp objects

- `Engine.Send` drops commands when the 32-slot buffer is full (by design,
  non-blocking); long engine-goroutine work (hard track switch decode)
  therefore makes rapid key repeats lossy. If you lengthen any synchronous
  engine-goroutine operation, re-check this.
- `Status.Position`/`Duration`/`Buffering` are zero/undefined while
  `Loading` is true (nothing is decoded yet); the UI must not extrapolate
  position across a load (`playbackPosition` guards this).
- Playlist paths and tag metadata are untrusted input: keep the extension
  allowlist in `openTrackDecoder` and the sanitizers in `library/`.
