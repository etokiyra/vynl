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
)

type Command struct {
	Action Action
	Value  float64
	Band   int
}

type Status struct {
	Track      library.Track
	Index      int
	NextIndex  int
	Count      int
	Playing    bool
	Loading    bool
	Buffering  bool
	Position   float64
	Duration   float64
	Speed      float64
	Pitch      float64
	Volume     float64
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
}

type Engine struct {
	tracks       []library.Track
	commands     chan Command
	updates      chan Status
	loading      bool
	control      *beep.Ctrl
	stream       *transportStreamer
	ring         *pcmRing
	decodeCancel chan struct{}
	decodeDone   chan struct{}
	index        int
	order        playOrder
	speed        float64
	pitch        float64
	volume       float64
	muted        bool
	vinyl        bool
	shuffle      bool
	repeat       RepeatMode
	eq           [3]float64
	playing      bool
	errText      string
	analyzer     *spectrumAnalyzer
	waveform     [analyzerSize]float32
	spectrum     [SpectrumBands]float64
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once

	// persistMu guards the race-free snapshot a non-engine goroutine (main, at
	// shutdown) reads. It is never held while taking speaker.Lock or ring.mu,
	// so it cannot affect the audio lock order.
	persistMu sync.Mutex
	persist   PersistState
}

// InitialState carries the playback settings restored from the persisted state
// file. It is expected to be fully populated; config.DefaultState()/LoadState
// provide sane values.
type InitialState struct {
	Volume  float64
	Muted   bool
	EQ      [3]float64
	Shuffle bool
	Vinyl   bool
	Repeat  RepeatMode
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
		tracks: tracks, commands: make(chan Command, 32), updates: make(chan Status, 1),
		speed:    1,
		analyzer: newSpectrumAnalyzer(analyzerSize, outputRate),
		stop:     make(chan struct{}), done: make(chan struct{}),
	}
	e.applyInitial(initial)
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
	if e.speed == 0 {
		e.speed = 1
	}
	e.order = newPlayOrder(len(e.tracks))
	if e.shuffle {
		e.order.shuffle()
	}
	e.updatePersist()
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
		e.stopDecoder()
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
			done := false
			if e.stream != nil {
				speaker.Lock()
				done = e.stream.Done()
				speaker.Unlock()
			}
			if e.stream != nil && e.playing && done {
				e.advanceTrack()
			}
			e.publish()
		}
	}
}

func (e *Engine) handle(command Command) {
	switch command.Action {
	case Toggle:
		if e.control != nil {
			speaker.Lock()
			e.control.Paused = !e.control.Paused
			e.playing = !e.control.Paused
			speaker.Unlock()
		}
	case Stop:
		if e.control != nil {
			speaker.Lock()
			e.control.Paused = true
			e.stream.Seek(0)
			e.playing = false
			speaker.Unlock()
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
	case Seek:
		if e.stream != nil {
			speaker.Lock()
			e.stream.Seek(e.stream.PositionSeconds() + command.Value)
			speaker.Unlock()
		}
	case Restart:
		if e.stream != nil {
			speaker.Lock()
			e.stream.Seek(0)
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
	case Repeat:
		e.repeat = e.repeat.next()
	case EQ:
		if command.Band >= 0 && command.Band < len(e.eq) {
			e.eq[command.Band] = clamp(e.eq[command.Band]+command.Value, 0, 2)
			if e.stream != nil {
				speaker.Lock()
				e.stream.SetEQ(command.Band, e.eq[command.Band])
				speaker.Unlock()
			}
		}
	}
	e.publish()
}

func (e *Engine) selectTrack(index int, autoplay bool) {
	if e.control != nil {
		speaker.Lock()
		e.control.Paused = true
		e.playing = false
		speaker.Unlock()
		speaker.Clear()
	}
	e.stopDecoder()
	e.index = index
	e.order.setCurrent(index)
	e.errText = ""
	e.playing = false
	e.loading = true
	e.control = nil
	e.stream = nil
	e.ring = nil
	e.publish()
	track, err := openTrackDecoder(e.tracks[index].Path)
	if err != nil {
		e.errText = err.Error()
		e.loading = false
		e.publish()
		return
	}
	initial, more, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		track.close()
		e.errText = err.Error()
		e.loading = false
		e.publish()
		return
	}
	if len(initial) == 0 {
		track.close()
		e.errText = "audio file contains no samples"
		e.loading = false
		e.publish()
		return
	}
	var ring *pcmRing
	if more {
		// Long track: bounded ring plus a background producer. The first
		// second is written synchronously so playback can start immediately.
		ring = newPCMRing(ringCapacityFrames)
		ring.setTotal(int64(track.totalFrames))
		ring.tryWrite(initial)
	} else {
		// Whole track already fits in the initial buffer: keep it complete and
		// static, no producer needed (and seeks stay instant).
		ring = newCompletePCMRing(initial)
	}
	e.ring = ring
	e.stream = newStreamingTransport(ring, outputRate)
	e.stream.SetVolume(e.effectiveVolume())
	for band, value := range e.eq {
		e.stream.SetEQ(band, value)
	}
	e.configureTransport()
	if more {
		e.startDecoder(track)
	} else {
		track.close()
	}
	e.control = &beep.Ctrl{Streamer: e.stream, Paused: !autoplay}
	speaker.Play(e.control)
	e.playing = autoplay
	e.loading = false
	e.publish()
}

func (e *Engine) configureTransport() {
	if e.stream == nil {
		return
	}
	pitch := math.Pow(2, e.pitch/12)
	if e.vinyl {
		pitch *= e.speed
	}
	speaker.Lock()
	e.stream.Configure(e.speed, pitch)
	speaker.Unlock()
}

// effectiveVolume collapses the stored volume with the mute state. Muting is
// non-destructive: the volume setting is preserved so unmuting restores it.
func (e *Engine) effectiveVolume() float64 {
	if e.muted {
		return 0
	}
	return e.volume
}

func (e *Engine) applyVolume() {
	if e.stream == nil {
		return
	}
	speaker.Lock()
	e.stream.SetVolume(e.effectiveVolume())
	speaker.Unlock()
}

// advanceTrack selects what plays next when the current track ends, honoring
// repeat and shuffle. With repeat off at the end of the order it rewinds and
// pauses instead of looping.
func (e *Engine) advanceTrack() {
	next, stop := repeatAdvance(&e.order, e.index, e.repeat, e.shuffle)
	if stop {
		e.playing = false
		if e.control != nil {
			speaker.Lock()
			e.control.Paused = true
			e.stream.Seek(0)
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
		EQ: e.eq, Err: e.errText,
	}
	if len(e.tracks) > 0 {
		status.Track = e.tracks[e.index]
		if len(e.tracks) > 1 {
			status.NextIndex, _ = e.order.peek(1)
		} else {
			status.NextIndex = e.index
		}
	}
	if e.stream != nil {
		speaker.Lock()
		status.Position = e.stream.PositionSeconds()
		status.Duration = e.stream.DurationSeconds()
		status.Peak = e.stream.Peak()
		status.RMS = e.stream.RMS()
		status.ChannelRMS = e.stream.ChannelRMS()
		status.BassRMS = e.stream.BassRMS()
		status.Buffering = e.playing && e.stream.Buffering()
		if e.playing {
			e.stream.CopyWaveform(e.waveform[:])
		}
		speaker.Unlock()
		if e.playing {
			// Analyze after releasing the speaker lock so the FFT never delays
			// the real-time output callback.
			e.analyzer.analyze(e.waveform[:], &e.spectrum)
			status.Spectrum = e.spectrum
		}
	}
	if e.ring != nil {
		if ringErr := e.ring.errValue(); ringErr != nil {
			status.Err = ringErr.Error()
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

func (e *Engine) startDecoder(track *trackDecoder) {
	e.stopDecoder()
	cancel := make(chan struct{})
	done := make(chan struct{})
	ring := e.ring
	e.decodeCancel = cancel
	e.decodeDone = done
	go func() {
		defer close(done)
		defer track.close()
		decoded := make([]sample, ringChunkFrames)
		chunk := make([]pcmSample, 0, ringChunkFrames)
		for {
			// The wake channel is only a hint: notify is dropped when its
			// buffer is full. Re-checking here on *every* iteration (and again
			// after every wait in tryWrite) guarantees a seek is processed even
			// if a wake is lost, so progress never depends on receiving one.
			if gen, frame, ok := ring.pendingSeek(); ok {
				if err := track.seekToOutputFrame(frame); err != nil {
					ring.finish(err)
				} else {
					ring.commitSeek(gen, frame)
				}
				continue
			}
			select {
			case <-cancel:
				ring.finish(nil)
				return
			default:
			}
			if ring.isFinished() {
				// Stay alive after EOF (or error) so Restart and backward seeks
				// can resume decoding, then keep draining until we are stopped.
				ring.wait(cancel)
				continue
			}
			n, more := track.resampled.Stream(decoded)
			if err := track.resampled.Err(); err != nil {
				ring.finish(err)
				continue
			}
			if n == 0 && more {
				ring.finish(errors.New("audio decoder made no progress"))
				continue
			}
			chunk = appendPCM(chunk[:0], decoded[:n])
			for !ring.tryWrite(chunk) {
				if !ring.wait(cancel) {
					ring.finish(nil)
					return
				}
				if _, _, ok := ring.pendingSeek(); ok {
					break
				}
			}
			if _, _, ok := ring.pendingSeek(); ok {
				continue
			}
			if !more {
				ring.finish(nil)
			}
		}
	}()
}

func (e *Engine) stopDecoder() {
	if e.decodeCancel == nil {
		return
	}
	close(e.decodeCancel)
	<-e.decodeDone
	e.decodeCancel = nil
	e.decodeDone = nil
}

func (s Status) Remaining() float64 { return math.Max(0, s.Duration-s.Position) }
