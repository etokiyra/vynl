package player

import (
	"errors"
	"sync"
	"time"
)

// trackStream bundles one track's playback resources: its bounded decoded ring,
// the transport that reads it, and the background producer that decodes into
// it. The engine owns creation, configuration, and teardown; the audio callback
// only calls transport.Stream through the queueStreamer.
//
// Teardown is deterministic: close joins the producer's cancel (which stops it
// after its current decoder call), and closeWithTimeout bounds how long a
// caller waits for that join: a slow decoder call inside the producer (a
// seek-table rebuild in a FLAC decoder, a slow file) is handed off to a
// background janitor so the engine goroutine never stalls on teardown.
type trackStream struct {
	index       int
	path        string
	ring        *pcmRing
	transport   *transportStreamer
	sampleRate  int
	trackGainDB float64

	// Producer lifecycle. cancel is closed to stop the decoder goroutine; done
	// is closed once it has exited and released the decoder. A complete (short)
	// track fits entirely in the initial buffer and leaves both nil.
	cancel chan struct{}
	done   chan struct{}
	// closing guards the single close of cancel. close is not re-entrant (the
	// channel may only be closed once), and with the janitor path two callers
	// can race at close time.
	closing bool
	closeMu sync.Mutex
}

// openTrackStream opens path, decodes its initial buffer, builds the ring and
// transport, and starts the background producer when the file is longer than
// the initial buffer. It does not apply engine settings (volume/EQ/ReplayGain/
// tempo); the caller configures the transport before installing the stream.
func openTrackStream(index int, path string) (*trackStream, error) {
	decoder, err := openTrackDecoder(path)
	if err != nil {
		return nil, err
	}
	initial, more, err := decoder.decodeInitial(initialBufferFrames)
	if err != nil {
		decoder.close()
		return nil, err
	}
	if len(initial) == 0 {
		decoder.close()
		return nil, errors.New("audio file contains no samples")
	}

	stream := &trackStream{index: index, path: path, sampleRate: int(decoder.inRate)}
	if more {
		// Long track: bounded ring plus a background producer. The first
		// second is written synchronously so playback can start immediately.
		stream.ring = newPCMRing(ringCapacityFrames)
		stream.ring.setTotal(int64(decoder.totalFrames))
		stream.ring.tryWrite(initial)
	} else {
		// Whole track already fits in the initial buffer: keep it complete and
		// static, no producer needed (and seeks stay instant).
		stream.ring = newCompletePCMRing(initial)
	}
	stream.transport = newStreamingTransport(stream.ring, outputRate)
	if more {
		stream.startProducer(decoder)
	} else {
		decoder.close()
	}
	return stream, nil
}

// startProducer runs the flow-controlled decoder that fills the ring. It is the
// old Engine.startDecoder loop, now owned by the stream so multiple tracks can
// have independent producers during a gapless prefetch.
func (t *trackStream) startProducer(decoder *trackDecoder) {
	t.cancel = make(chan struct{})
	t.done = make(chan struct{})
	ring := t.ring
	cancel := t.cancel
	go func() {
		defer close(t.done)
		defer decoder.close()
		decoded := make([]sample, ringChunkFrames)
		chunk := make([]pcmSample, 0, ringChunkFrames)
		for {
			// The wake channel is only a hint: notify is dropped when its
			// buffer is full. Re-checking here on *every* iteration (and again
			// after every wait in tryWrite) guarantees a seek is processed even
			// if a wake is lost, so progress never depends on receiving one.
			if gen, frame, ok := ring.pendingSeek(); ok {
				t.repositionSeek(gen, frame, decoder, &decoded, &chunk)
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
			n, more := decoder.resampled.Stream(decoded)
			if err := decoder.resampled.Err(); err != nil {
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

// maxRepositionDiscard bounds how far a consumer seek may be served by
// decoding ahead instead of repositioning the decoder.
//
// Repositioning costs a decoder Seek, which is not always cheap: a FLAC
// decoder without a seek table decodes the entire stream just to index its
// frames, so one forward skip can buffer for most of the song before a single
// sample plays. Decoding ahead keeps the (already sequential) decoder running,
// is bounded by the jump distance rather than the file size, and leaves the
// ring's continuity (and the resampler's state) untouched. The ring must also
// be able to hold the target, so a hop beyond these bounds falls back to the
// real reposition.
const maxRepositionDiscard = 60 * outputRate

// repositionSeek serves a pending consumer seek. A target at or ahead of the
// decoded extent, small enough for the ring to hold, is served without
// touching the decoder: the sequential decode continues under normal flow
// control until the requested frame is covered, then the seek is acked with
// the window intact. Everything else (backward hops, targets beyond the
// resident capacity) uses the real reposition. In-flight cancel and
// newer-seek supersession are respected on both paths. The decoded/chunk
// slices are the producer loop's own buffers, reused to stay allocation-free.
func (t *trackStream) repositionSeek(gen uint64, frame int64, decoder *trackDecoder, decoded *[]sample, chunk *[]pcmSample) {
	ring := t.ring
	cancel := t.cancel

	// A newer consumer seek supersedes this one: leave it pending so the next
	// producer iteration processes the newest request instead.
	if g, _, ok := ring.pendingSeek(); !ok || g != gen {
		return
	}

	select {
	case <-cancel:
		ring.finish(nil)
		return
	default:
	}

	ring.mu.Lock()
	extent := ring.frameEndLocked()
	space := int64(ring.capacity) - (frame - ring.base) - int64(ring.ahead)
	ring.mu.Unlock()

	if frame >= extent && space >= 0 && frame-extent <= maxRepositionDiscard {
		// Decode ahead under normal flow control until the ring covers the
		// requested frame; the window stays continuous throughout.
		for {
			if g, _, ok := ring.pendingSeek(); !ok || g != gen {
				return
			}
			select {
			case <-cancel:
				ring.finish(nil)
				return
			default:
			}
			ring.mu.Lock()
			covered := ring.frameEndLocked() >= frame
			ring.mu.Unlock()
			if covered {
				ring.commitAfterDecode(gen)
				return
			}
			n, more := decoder.resampled.Stream(*decoded)
			if err := decoder.resampled.Err(); err != nil {
				ring.finish(err)
				return
			}
			if n == 0 && more {
				ring.finish(errors.New("audio decoder made no progress"))
				return
			}
			*chunk = appendPCM((*chunk)[:0], (*decoded)[:n])
			for !ring.tryWrite(*chunk) {
				if !ring.wait(cancel) {
					ring.finish(nil)
					return
				}
				if g, _, ok := ring.pendingSeek(); ok && g != gen {
					return
				}
			}
			if !more {
				// EOF before the target: the container length over-declared the
				// real content. Ack anyway; the transport sees a finished ring
				// and ends the track through its past-end path.
				ring.commitAfterDecode(gen)
				return
			}
		}
	}

	// Real reposition: reseek the decoder and reset the window to the frame.
	if err := decoder.seekToOutputFrame(frame); err != nil {
		ring.finish(err)
		return
	}
	ring.commitSeek(gen, frame)
}

// close stops the producer (if any) and waits for it to release the decoder.
// Safe to call more than once; a second call is a no-op.
func (t *trackStream) close() {
	t.closeWithTimeout(0)
}

// closeWithTimeout closes the producer's cancel and waits for the producer to
// exit. A zero (or negative) timeout joins synchronously: the classic close.
// A nonzero timeout means wait up to that long for the join, then hand the
// remaining join to a background janitor and return, so callers (the engine
// goroutine) are never blocked behind a slow decoder call — a seek-table
// rebuild, a slow file — that ignores the cancel until it returns.
func (t *trackStream) closeWithTimeout(timeout time.Duration) {
	t.closeMu.Lock()
	if t.cancel == nil || t.closing {
		// Already joined, or another caller is joining/abandoned; either way
		// done closes eventually and no more handling is needed here.
		t.closeMu.Unlock()
		return
	}
	t.closing = true
	cancel, done := t.cancel, t.done
	t.closeMu.Unlock()

	close(cancel)
	t.finishJoin(done, timeout)
}

// finishJoin waits for the producer to exit (bounded, with a janitor fallback
// on timeout) and clears the stream's handles.
func (t *trackStream) finishJoin(done chan struct{}, timeout time.Duration) {
	if timeout > 0 {
		select {
		case <-done:
		case <-time.After(timeout):
			go func() {
				<-done
				t.closeMu.Lock()
				t.cancel, t.done = nil, nil
				t.closeMu.Unlock()
			}()
			return
		}
	} else {
		<-done
	}
	t.closeMu.Lock()
	t.cancel, t.done = nil, nil
	t.closeMu.Unlock()
}
