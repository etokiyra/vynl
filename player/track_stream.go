package player

import "errors"

// trackStream bundles one track's playback resources: its bounded decoded ring,
// the transport that reads it, and the background producer that decodes into
// it. The engine owns creation, configuration, and teardown; the audio callback
// only calls transport.Stream through the queueStreamer.
//
// Teardown is deterministic: close() stops and joins the producer, which closes
// the underlying decoder, so no decoder or goroutine outlives the stream. It
// must run on the engine goroutine, never inside the audio callback.
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
				if err := decoder.seekToOutputFrame(frame); err != nil {
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

// close stops the producer (if any) and waits for it to release the decoder.
// Safe to call more than once; a second call is a no-op.
func (t *trackStream) close() {
	if t.cancel == nil {
		return
	}
	close(t.cancel)
	<-t.done
	t.cancel = nil
	t.done = nil
}
