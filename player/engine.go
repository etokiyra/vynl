package player

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/gopxl/beep"
	"github.com/gopxl/beep/flac"
	"github.com/gopxl/beep/mp3"
	"github.com/gopxl/beep/speaker"
	"github.com/gopxl/beep/vorbis"
	"github.com/gopxl/beep/wav"
)

const outputRate = 44100

type Action string

const (
	Toggle  Action = "toggle"
	Stop    Action = "stop"
	Next    Action = "next"
	Prev    Action = "previous"
	Seek    Action = "seek"
	Restart Action = "restart"
	Volume  Action = "volume"
	Mute    Action = "mute"
	Speed   Action = "speed"
	Pitch   Action = "pitch"
	Reset   Action = "reset"
	Vinyl   Action = "vinyl"
	Shuffle Action = "shuffle"
	Repeat  Action = "repeat"
	EQ      Action = "eq"
	Select  Action = "select"
	// SetTracks replaces the engine's complete track list in one shot. It is
	// the only mutation of the list after construction; the replacement is
	// carried on the Command (there is no append/incremental path), and
	// Revision is echoed in every subsequent Status so the UI can tell when the
	// swap has landed and only then trust index-based fields.
	SetTracks Action = "settracks"
)

type Command struct {
	Action Action
	Value  float64
	Band   int
	// Tracks is the complete replacement list for SetTracks; Revision is a
	// caller-assigned generation token echoed back in Status.
	Tracks   []library.Track
	Revision int
}

type Status struct {
	Track      library.Track
	NextTrack  library.Track
	Index      int
	NextIndex  int
	Count      int
	Revision   int
	Playing    bool
	Loading    bool
	Buffering  bool
	Position   float64
	Duration   float64
	Speed      float64
	Pitch      float64
	Volume     float64
	SampleRate int
	Muted      bool
	Vinyl      bool
	Shuffle    bool
	Repeat     RepeatMode
	EQ         [3]float64
	Peak       float64
	RMS        float64
	ChannelRMS [2]float64
	BassRMS    float64
	Spectrum   [SpectrumBands]float64
	Err        string
	// ReplayGain is the active loudness-normalization mode ("off"/"track"/
	// "album") and TrackGainDB is the gain actually applied to the loaded
	// track (0 when normalization is off or the track has no metadata).
	ReplayGain  string
	TrackGainDB float64
	// Skipped is the number of unplayable tracks auto-skipped to reach the
	// loaded one (0 when the loaded track opened cleanly). It is informational:
	// the deck surfaces it so a silent jump over a corrupt file is explained.
	Skipped int
}

type Engine struct {
	tracks   []library.Track
	commands chan Command
	updates  chan Status
	loading  bool
	control  *beep.Ctrl
	// queue is the persistent top-level streamer; current is the trackStream
	// the engine believes is playing. The audio callback may promote the queue's
	// pending stream ahead of the engine; reconcile() adopts that promotion.
	queue       *queueStreamer
	current     *trackStream
	index       int
	order       playOrder
	revision    int
	speed       float64
	pitch       float64
	volume      float64
	muted       bool
	vinyl       bool
	shuffle     bool
	repeat      RepeatMode
	eq          [3]float64
	playing     bool
	errText     string
	skipped     int
	sampleRate  int
	replayGain  string
	preampDB    float64
	trackGainDB float64
	eqLowHz     float64
	eqHighHz    float64
	// crossfadeFrames is the crossfade overlap in output frames; the queue
	// holds the live copy (q.crossfade) that the callback reads.
	crossfadeFrames int
	analyzer        *spectrumAnalyzer
	waveform        [analyzerSize]float32
	spectrum        [SpectrumBands]float64
	stop            chan struct{}
	done            chan struct{}
	closeOnce       sync.Once

	// persistMu guards the race-free snapshot a non-engine goroutine (main, at
	// shutdown) reads. It is never held while taking speaker.Lock or ring.mu,
	// so it cannot affect the audio lock order.
	persistMu sync.Mutex
	persist   PersistState
}

// InitialState carries the playback settings applied when the engine starts.
// Most come from the persisted state file (config.DefaultState/LoadState); the
// ReplayGain mode and preamp come from the config file's [playback] table. It is
// expected to be fully populated.
type InitialState struct {
	Volume  float64
	Muted   bool
	EQ      [3]float64
	Shuffle bool
	Vinyl   bool
	Repeat  RepeatMode
	// ReplayGain is the loudness-normalization mode ("off"/"track"/"album")
	// and PreampDB is added to the selected gain.
	ReplayGain string
	PreampDB   float64
	// EQLowHz/EQHighHz are the 3-band EQ's crossover frequencies. Zero values
	// (e.g. from tests) leave the transport's defaults in place.
	EQLowHz  float64
	EQHighHz float64
	// CrossfadeMS is the overlap between consecutive tracks in milliseconds (0
	// keeps the gapless join without an overlap).
	CrossfadeMS int
}

// PersistState is the subset of engine state that is remembered across runs.
// It intentionally excludes the current track and playback position.
type PersistState struct {
	Volume  float64
	Muted   bool
	EQ      [3]float64
	Shuffle bool
	Vinyl   bool
	Repeat  RepeatMode
}

func NewEngine(tracks []library.Track, initial InitialState) (*Engine, error) {
	if len(tracks) == 0 {
		return nil, errors.New("music library is empty")
	}
	if err := speaker.Init(beep.SampleRate(outputRate), 2048); err != nil {
		return nil, fmt.Errorf("initialize audio output: %w", err)
	}
	e := &Engine{
		tracks: cloneTracks(tracks), commands: make(chan Command, 32), updates: make(chan Status, 1),
		speed: 1,
		queue: newQueueStreamer(),
		// The queue is played once and never restarts; track changes swap its
		// primary/pending streams rather than the device's streamer list, which
		// is what makes a track boundary gapless.
		analyzer: newSpectrumAnalyzer(analyzerSize, outputRate),
		stop:     make(chan struct{}), done: make(chan struct{}),
	}
	e.applyInitial(initial)
	e.queue.crossfade = e.crossfadeFrames
	e.control = &beep.Ctrl{Streamer: e.queue, Paused: true}
	speaker.Play(e.control)
	go e.run()
	e.Send(Command{Action: Select, Value: 0})
	return e, nil
}

// applyInitial sets the persisted settings and rebuilds the play order to match.
// It runs before the engine goroutine starts, so it needs no locking.
func (e *Engine) applyInitial(initial InitialState) {
	e.volume = clamp(initial.Volume, 0, 1)
	e.muted = initial.Muted
	for i := range e.eq {
		e.eq[i] = clamp(initial.EQ[i], 0, 2)
	}
	e.shuffle = initial.Shuffle
	e.vinyl = initial.Vinyl
	e.repeat = initial.Repeat
	if e.repeat != RepeatOff && e.repeat != RepeatAll && e.repeat != RepeatOne {
		e.repeat = RepeatAll
	}
	e.replayGain = normalizeReplayGain(initial.ReplayGain)
	e.preampDB = clamp(initial.PreampDB, -12, 12)
	e.eqLowHz = initial.EQLowHz
	e.eqHighHz = initial.EQHighHz
	e.crossfadeFrames = crossfadeFramesFor(initial.CrossfadeMS)
	if e.speed == 0 {
		e.speed = 1
	}
	e.rebuildOrder(0)
	e.updatePersist()
}

// rebuildOrder rebuilds the play order for the current track list, honoring the
// shuffle setting, and points the order at index. It is used when the list size
// changes (startup and rescan); it never touches the decode/transport state.
func (e *Engine) rebuildOrder(index int) {
	e.order = newPlayOrder(len(e.tracks))
	if e.shuffle {
		e.order.shuffle()
	}
	e.order.setCurrent(index)
}

// updatePersist refreshes the cross-goroutine snapshot. Called on the engine
// goroutine whenever persisted settings can change.
func (e *Engine) updatePersist() {
	e.persistMu.Lock()
	e.persist = PersistState{
		Volume: e.volume, Muted: e.muted, EQ: e.eq,
		Shuffle: e.shuffle, Vinyl: e.vinyl, Repeat: e.repeat,
	}
	e.persistMu.Unlock()
}

// PersistState returns the latest persisted settings. Safe to call from any
// goroutine; used by main on exit.
func (e *Engine) PersistState() PersistState {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	return e.persist
}

func (e *Engine) Send(command Command) {
	select {
	case e.commands <- command:
	default:
	}
}

func (e *Engine) Updates() <-chan Status { return e.updates }

func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		close(e.stop)
		<-e.done
		speaker.Clear()
		speaker.Close()
	})
}

func (e *Engine) run() {
	defer func() {
		e.shutdownStreams()
		close(e.done)
	}()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case command := <-e.commands:
			e.handle(command)
		case <-e.stop:
			return
		case <-ticker.C:
			// The queue promotes the prefetched track from inside the audio
			// callback; reconcile adopts that promotion and prefetches onward.
			e.reconcile()
			e.publish()
		}
	}
}

func (e *Engine) handle(command Command) {
	// Adopt any promotion the audio callback performed while this command was
	// queued, so commands act on the track that is actually playing.
	e.reconcile()
	switch command.Action {
	case Toggle:
		if e.current != nil {
			speaker.Lock()
			e.control.Paused = !e.control.Paused
			e.playing = !e.control.Paused
			speaker.Unlock()
		}
	case Stop:
		if e.current != nil {
			e.cancelFade()
			speaker.Lock()
			e.control.Paused = true
			e.current.transport.Seek(0)
			speaker.Unlock()
			e.playing = false
		}
	case Next:
		next, _ := e.order.advance(1)
		e.selectTrack(next, true)
	case Prev:
		prev, _ := e.order.advance(-1)
		e.selectTrack(prev, true)
	case Select:
		index := int(command.Value)
		if index >= 0 && index < len(e.tracks) {
			e.selectTrack(index, true)
		}
	case SetTracks:
		e.replaceTracks(command.Tracks, command.Revision)
	case Seek:
		if e.current != nil {
			e.cancelFade()
			speaker.Lock()
			e.current.transport.Seek(e.current.transport.PositionSeconds() + command.Value)
			speaker.Unlock()
		}
	case Restart:
		if e.current != nil {
			e.cancelFade()
			speaker.Lock()
			e.current.transport.Seek(0)
			speaker.Unlock()
		}
	case Volume:
		e.volume = clamp(e.volume+command.Value, 0, 1)
		e.applyVolume()
	case Mute:
		e.muted = !e.muted
		e.applyVolume()
	case Speed:
		e.speed = clamp(e.speed+command.Value, 0.5, 2)
		e.configureTransport()
	case Pitch:
		e.pitch = clamp(e.pitch+command.Value, -12, 12)
		e.configureTransport()
	case Reset:
		e.speed, e.pitch = 1, 0
		e.configureTransport()
	case Vinyl:
		e.vinyl = !e.vinyl
		e.configureTransport()
	case Shuffle:
		e.shuffle = !e.shuffle
		if e.shuffle {
			e.order.shuffle()
		} else {
			e.order.reset(len(e.tracks))
			e.order.setCurrent(e.index)
		}
		e.reprefetch()
	case Repeat:
		e.repeat = e.repeat.next()
		e.reprefetch()
	case EQ:
		if command.Band >= 0 && command.Band < len(e.eq) {
			e.eq[command.Band] = clamp(e.eq[command.Band]+command.Value, 0, 2)
			e.applyEQ(command.Band)
		}
	}
	e.publish()
}

// cloneTracks returns an independent copy of tracks. The engine must own its
// slice: the UI mutates its track list in place as tag metadata streams in on
// the Bubble Tea event loop, and the engine copies the current track every tick
// (publish), so sharing the backing array would be a data race. The engine only
// needs paths for identity and titles as a display fallback; the UI renders its
// own tagged copy.
func cloneTracks(tracks []library.Track) []library.Track {
	return append([]library.Track(nil), tracks...)
}

// planTrackSwap decides how a replacement track list affects the loaded track.
// It matches by path (the stable identity across a rescan); when the current
// path is empty or absent it returns (0, false), meaning playback should stop
// and the deck should settle on the first track.
func planTrackSwap(currentPath string, tracks []library.Track) (index int, keep bool) {
	if currentPath == "" {
		return 0, false
	}
	for i, track := range tracks {
		if track.Path == currentPath {
			return i, true
		}
	}
	return 0, false
}

// replaceTracks swaps in a complete new track list. It is the engine's only
// list mutation and runs on the engine goroutine via handle(SetTracks).
//
// If the currently loaded track is still present (by path) it keeps playing
// untouched, so the swap never touches the audio path; only the index and the
// play order change. If it is gone, playback stops and the deck parks on the
// first track paused rather than silently jumping into a different song.
func (e *Engine) replaceTracks(tracks []library.Track, revision int) {
	if len(tracks) == 0 {
		// The engine contract is a non-empty list; a rescan that finds nothing
		// keeps the current library rather than leaving the engine in a state
		// selectTrack/publish assume cannot happen.
		return
	}
	currentPath := ""
	if e.index >= 0 && e.index < len(e.tracks) {
		currentPath = e.tracks[e.index].Path
	}
	index, keep := planTrackSwap(currentPath, tracks)
	e.tracks = cloneTracks(tracks)
	e.revision = revision
	if keep {
		e.rebuildOrder(index)
		e.index = index
		// The prefetched next track may no longer be the right one.
		e.reprefetch()
		e.publish()
		return
	}
	if e.current == nil {
		// Nothing is loaded yet (a rescan before the initial Select runs):
		// there is no playback to stop, so just repoint the deck.
		e.rebuildOrder(0)
		e.index = 0
		e.publish()
		return
	}
	e.selectTrack(0, false)
}

// selectTrack performs a hard (non-gapless) track change: it retires the
// current streams, then opens and installs the requested track. When autoplay
// is set it skips forward over any track that fails to open or decode, bounded
// by the library size, so a single unplayable file cannot stall playback; a
// non-autoplay load (a rescan that removed the current track) never skips.
//
// Normal gapless transitions do not go through here: the queue promotes a
// prefetched pending stream instead. This path serves explicit user selection,
// Next/Prev, startup, and the exhaustion / failed-prefetch fallback.
func (e *Engine) selectTrack(index int, autoplay bool) {
	e.skipped = 0
	e.stopCurrent()
	// Surface a loading state for the requested track while it is opened. It is
	// brief (the build is synchronous on this goroutine), but keeps the deck
	// from showing the previous track during a hard change.
	e.index = index
	e.order.setCurrent(index)
	e.loading = true
	e.publish()
	skipped := 0
	lastIndex, lastErr := index, error(nil)
	for {
		stream, err := e.buildStream(index)
		if err == nil {
			e.installPrimary(stream, autoplay)
			e.skipped = skipped
			break
		}
		lastIndex, lastErr = index, err
		if !autoplay || skipped+1 >= len(e.tracks) {
			e.park(lastIndex, lastErr)
			break
		}
		next, _ := e.order.advance(1)
		if next == index {
			e.park(lastIndex, lastErr)
			break
		}
		skipped++
		index = next
	}
	e.publish()
	if e.current != nil {
		e.ensurePending()
		e.reconcileRetired()
	}
}

// buildStream opens a track and applies the engine's current settings to its
// transport. It performs file I/O and a one-second decode, so it runs on the
// engine goroutine, never the audio callback.
func (e *Engine) buildStream(index int) (*trackStream, error) {
	stream, err := openTrackStream(index, e.tracks[index].Path)
	if err != nil {
		return nil, err
	}
	e.configureStream(stream)
	return stream, nil
}

// configureStream applies the current volume, EQ, crossover, ReplayGain, and
// tempo/pitch settings to a freshly opened (or prefetched) stream.
func (e *Engine) configureStream(stream *trackStream) {
	stream.transport.SetCrossover(e.eqLowHz, e.eqHighHz)
	stream.transport.SetVolume(e.effectiveVolume())
	for band, value := range e.eq {
		stream.transport.SetEQ(band, value)
	}
	stream.trackGainDB = 0
	// ReplayGain metadata is read on the engine side (the UI's tagged list
	// lives on the event loop and the engine owns a clone), and only when
	// enabled so the default "off" mode adds no I/O.
	if e.replayGain != "off" {
		if gain := e.trackGain(library.ReadReplayGain(stream.path)); gain > 0 {
			stream.transport.SetTrackGain(gain)
			stream.trackGainDB = 20 * math.Log10(gain)
		}
	}
	stream.transport.Configure(e.speed, e.effectivePitch())
}

// installPrimary makes stream the current track and starts or pauses playback.
func (e *Engine) installPrimary(stream *trackStream, autoplay bool) {
	speaker.Lock()
	e.queue.setPrimary(stream)
	e.control.Paused = !autoplay
	speaker.Unlock()
	e.current = stream
	e.index = stream.index
	e.order.setCurrent(stream.index)
	e.errText = ""
	e.sampleRate = stream.sampleRate
	e.trackGainDB = stream.trackGainDB
	e.playing = autoplay
	e.loading = false
}

// stopCurrent retires every stream and silences the queue, which stays
// installed. Used for hard track changes.
func (e *Engine) stopCurrent() {
	speaker.Lock()
	e.queue.clear()
	e.control.Paused = true
	speaker.Unlock()
	e.current = nil
	e.reconcileRetired()
}

// park settles the deck on a track that could not be opened, with no audio.
func (e *Engine) park(index int, reason error) {
	e.index = index
	e.order.setCurrent(index)
	if reason != nil {
		e.errText = reason.Error()
	}
	e.playing = false
	e.loading = false
	e.sampleRate = 0
	e.trackGainDB = 0
	speaker.Lock()
	e.control.Paused = true
	speaker.Unlock()
}

// reconcileRetired closes the streams the audio callback finished with. Closing
// joins a decoder, so it runs on the engine goroutine.
func (e *Engine) reconcileRetired() {
	if e.queue == nil {
		return
	}
	speaker.Lock()
	retired := e.queue.drainRetired()
	speaker.Unlock()
	for _, stream := range retired {
		stream.close()
	}
}

// cancelFade abandons an in-progress crossfade so a seek, restart, or stop takes
// effect immediately, and closes the retired outgoing stream.
func (e *Engine) cancelFade() {
	if e.queue == nil {
		return
	}
	speaker.Lock()
	e.queue.cancelFade()
	speaker.Unlock()
	e.reconcileRetired()
}

// reprefetch drops the prefetched next track and prepares a fresh one. Used when
// the play order changes (shuffle/repeat/rescan) so a stale prefetch cannot play.
func (e *Engine) reprefetch() {
	e.dropPending()
	e.ensurePending()
}

func (e *Engine) dropPending() {
	if e.queue == nil {
		return
	}
	speaker.Lock()
	e.queue.clearPending()
	speaker.Unlock()
	e.reconcileRetired()
}

// ensurePending prefetches the track that will follow the current one, so the
// audio callback can promote it without a gap. A failure leaves no pending; the
// queue then exhausts at the end of the current track and the engine falls back
// to the bounded skip path in advanceTrack.
func (e *Engine) ensurePending() {
	if e.queue == nil || e.current == nil {
		return
	}
	speaker.Lock()
	hasPending := e.queue.pending != nil
	primary := e.queue.primary
	speaker.Unlock()
	if hasPending || primary != e.current {
		return
	}
	next, stop := plannedNext(&e.order, e.index, e.repeat, e.shuffle)
	if stop {
		return
	}
	stream, err := e.buildStream(next)
	if err != nil {
		return
	}
	speaker.Lock()
	if e.queue.primary == e.current && e.queue.pending == nil {
		e.queue.setPending(stream)
		speaker.Unlock()
		return
	}
	speaker.Unlock()
	stream.close()
}

// reconcile adopts a promotion performed inside the audio callback (a gapless
// boundary), closes retired streams, and prefetches onward. It runs on the
// engine goroutine before publishing and before handling a command.
func (e *Engine) reconcile() {
	if e.queue == nil {
		return
	}
	speaker.Lock()
	primary := e.queue.primary
	exhausted := e.queue.exhausted
	if exhausted {
		e.queue.exhausted = false
	}
	speaker.Unlock()

	e.reconcileRetired()

	if primary != nil && primary != e.current {
		e.current = primary
		e.index = primary.index
		e.order.setCurrent(primary.index)
		e.sampleRate = primary.sampleRate
		e.trackGainDB = primary.trackGainDB
		e.errText = ""
		e.playing = true
		e.ensurePending()
		return
	}
	if exhausted {
		e.advanceTrack()
	}
}

// effectivePitch folds the pitch semitones into a ratio, coupling speed in
// vinyl mode (turntable behavior).
func (e *Engine) effectivePitch() float64 {
	pitch := math.Pow(2, e.pitch/12)
	if e.vinyl {
		pitch *= e.speed
	}
	return pitch
}

func (e *Engine) configureTransport() {
	pitch := e.effectivePitch()
	speaker.Lock()
	defer speaker.Unlock()
	if e.queue == nil {
		return
	}
	if e.queue.primary != nil {
		e.queue.primary.transport.Configure(e.speed, pitch)
	}
	if e.queue.pending != nil {
		e.queue.pending.transport.Configure(e.speed, pitch)
	}
	if e.queue.fading != nil {
		e.queue.fading.transport.Configure(e.speed, pitch)
	}
}

// effectiveVolume collapses the stored volume with the mute state. Muting is
// non-destructive: the volume setting is preserved so unmuting restores it.
func (e *Engine) effectiveVolume() float64 {
	if e.muted {
		return 0
	}
	return e.volume
}

// crossfadeFramesFor converts a crossfade duration into output frames, clamped
// to [0, 30 s] so a typo cannot create an hour-long overlap.
func crossfadeFramesFor(ms int) int {
	if ms < 0 {
		ms = 0
	}
	frames := ms * outputRate / 1000
	const maxFrames = 30 * outputRate
	if frames > maxFrames {
		frames = maxFrames
	}
	return frames
}

// normalizeReplayGain canonicalizes the configured mode; anything unrecognized
// disables normalization so a mistyped value is predictable.
func normalizeReplayGain(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "track":
		return "track"
	case "album":
		return "album"
	default:
		return "off"
	}
}

// trackGain returns the linear ReplayGain gain to apply to a track, combining
// the configured mode and preamp. It is exactly 1 when normalization is off or
// the track has no usable gain metadata. Album mode prefers the album gain tag
// and falls back to the track gain.
func (e *Engine) trackGain(rg library.ReplayGain) float64 {
	if e.replayGain == "off" {
		return 1
	}
	db, ok := rg.TrackDB, rg.HasTrack
	if e.replayGain == "album" && rg.HasAlbum {
		db, ok = rg.AlbumDB, true
	}
	if !ok {
		return 1
	}
	// Clamp to a musical range; the soft limiter handles any residual peaks.
	return math.Pow(10, clamp(db+e.preampDB, -24, 12)/20)
}

func (e *Engine) applyVolume() {
	speaker.Lock()
	defer speaker.Unlock()
	if e.queue == nil {
		return
	}
	volume := e.effectiveVolume()
	if e.queue.primary != nil {
		e.queue.primary.transport.SetVolume(volume)
	}
	if e.queue.pending != nil {
		e.queue.pending.transport.SetVolume(volume)
	}
	if e.queue.fading != nil {
		e.queue.fading.transport.SetVolume(volume)
	}
}

func (e *Engine) applyEQ(band int) {
	speaker.Lock()
	defer speaker.Unlock()
	if e.queue == nil {
		return
	}
	if e.queue.primary != nil {
		e.queue.primary.transport.SetEQ(band, e.eq[band])
	}
	if e.queue.pending != nil {
		e.queue.pending.transport.SetEQ(band, e.eq[band])
	}
	if e.queue.fading != nil {
		e.queue.fading.transport.SetEQ(band, e.eq[band])
	}
}

// advanceTrack handles the paths the gapless queue cannot: the current track
// ended with no prefetched next (every candidate failed to open, or repeat-off
// reached the end of the order). It uses the bounded skip path, so a corrupt
// next track is skipped rather than stalling, at the cost of a short gap.
func (e *Engine) advanceTrack() {
	next, stop := repeatAdvance(&e.order, e.index, e.repeat, e.shuffle)
	if stop {
		e.playing = false
		if e.current != nil {
			speaker.Lock()
			e.control.Paused = true
			e.current.transport.Seek(0)
			speaker.Unlock()
		}
		e.publish()
		return
	}
	e.selectTrack(next, true)
}

func (e *Engine) publish() {
	e.updatePersist()
	status := Status{
		Index: e.index, Count: len(e.tracks), Playing: e.playing,
		Loading: e.loading, Speed: e.speed, Pitch: e.pitch, Volume: e.volume,
		Muted: e.muted, Vinyl: e.vinyl, Shuffle: e.shuffle, Repeat: e.repeat,
		EQ: e.eq, Err: e.errText, Revision: e.revision, Skipped: e.skipped,
		SampleRate: e.sampleRate, ReplayGain: e.replayGain, TrackGainDB: e.trackGainDB,
	}
	if len(e.tracks) > 0 {
		status.Track = e.tracks[e.index]
		if len(e.tracks) > 1 {
			// NEXT reflects what will actually play: repeat-one replays the
			// current track, and repeat-off at the end of the order stops, so
			// NextTrack is left empty rather than wrapping to a track that will
			// not play.
			if next, stop := plannedNext(&e.order, e.index, e.repeat, e.shuffle); !stop {
				status.NextIndex = next
				status.NextTrack = e.tracks[next]
			}
		} else {
			status.NextIndex = e.index
			status.NextTrack = e.tracks[e.index]
		}
	}
	if e.current != nil {
		transport := e.current.transport
		speaker.Lock()
		status.Position = transport.PositionSeconds()
		status.Duration = transport.DurationSeconds()
		status.Peak = transport.Peak()
		status.RMS = transport.RMS()
		status.ChannelRMS = transport.ChannelRMS()
		status.BassRMS = transport.BassRMS()
		status.Buffering = e.playing && transport.Buffering()
		if e.playing {
			transport.CopyWaveform(e.waveform[:])
		}
		speaker.Unlock()
		if e.playing {
			// Analyze after releasing the speaker lock so the FFT never delays
			// the real-time output callback.
			e.analyzer.analyze(e.waveform[:], &e.spectrum)
			status.Spectrum = e.spectrum
		}
		if e.current.ring != nil {
			if ringErr := e.current.ring.errValue(); ringErr != nil {
				status.Err = ringErr.Error()
			}
		}
	}
	select {
	case e.updates <- status:
	default:
		select {
		case <-e.updates:
		default:
		}
		select {
		case e.updates <- status:
		default:
		}
	}
}

const initialBufferFrames = outputRate

type trackDecoder struct {
	file        *os.File
	stream      beep.StreamSeekCloser
	resampled   *beep.Resampler
	inRate      beep.SampleRate
	totalFrames int
}

func openTrackDecoder(path string) (*trackDecoder, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var stream beep.StreamSeekCloser
	var format beep.Format
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		stream, format, err = mp3.Decode(file)
	case ".flac":
		stream, format, err = flac.Decode(file)
	case ".wav":
		stream, format, err = wav.Decode(file)
	case ".ogg":
		stream, format, err = vorbis.Decode(file)
	default:
		err = fmt.Errorf("unsupported audio format: %s", filepath.Ext(path))
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	if format.SampleRate <= 0 {
		stream.Close()
		file.Close()
		return nil, errors.New("audio stream has invalid sample rate")
	}
	totalFrames := int(math.Round(float64(stream.Len()) * float64(outputRate) / float64(format.SampleRate)))
	return &trackDecoder{
		file: file, stream: stream,
		resampled:   beep.Resample(4, format.SampleRate, beep.SampleRate(outputRate), stream),
		inRate:      format.SampleRate,
		totalFrames: totalFrames,
	}, nil
}

func (d *trackDecoder) close() {
	if d.stream != nil {
		_ = d.stream.Close()
		d.stream = nil
	}
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}
}

func (d *trackDecoder) decodeInitial(target int) ([]pcmSample, bool, error) {
	decoded := make([]sample, 4096)
	var initial []pcmSample
	more := true
	for len(initial) < target && more {
		n, ok := d.resampled.Stream(decoded)
		initial = appendPCM(initial, decoded[:n])
		if err := d.resampled.Err(); err != nil {
			return nil, false, err
		}
		more = ok
		if n == 0 && ok {
			return nil, false, errors.New("audio decoder made no progress")
		}
	}
	return initial, more, nil
}

// seekToOutputFrame repositions the decoder so the next decoded frame is the
// given output-rate frame. beep's Resampler has no reset, so it is rebuilt;
// this is the only place it is rebuilt, and only on a real decoder seek (never
// on a within-window head move, which never touches the decoder).
func (d *trackDecoder) seekToOutputFrame(frame int64) error {
	native := int64(math.Round(float64(frame) * float64(d.inRate) / float64(outputRate)))
	if native < 0 {
		native = 0
	}
	if err := d.stream.Seek(int(native)); err != nil {
		return err
	}
	d.resampled = beep.Resample(4, d.inRate, beep.SampleRate(outputRate), d.stream)
	return nil
}

func appendPCM(destination []pcmSample, decoded []sample) []pcmSample {
	for _, value := range decoded {
		destination = append(destination, pcmSample{float32(value[0]), float32(value[1])})
	}
	return destination
}

// shutdownStreams stops audio and joins every decoder before the engine exits.
// It pauses and clears the speaker first so no callback can be reading a stream
// while it is closed.
func (e *Engine) shutdownStreams() {
	speaker.Lock()
	if e.control != nil {
		e.control.Paused = true
	}
	if e.queue != nil {
		e.queue.clear()
	}
	speaker.Unlock()
	speaker.Clear()
	e.reconcileRetired()
	e.current = nil
}

func (s Status) Remaining() float64 { return math.Max(0, s.Duration-s.Position) }
