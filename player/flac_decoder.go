package player

// flacDecoder is vynl's own positioned FLAC decoder. It replaces the beep FLAC
// wrapper, whose Seek re-decodes the entire stream when the file has no seek
// table (the scan that made far jumps buffer for seconds). This decoder uses
// the same underlying frame layer - mewkiz's public frame package, which is
// exactly what beep's wrapper calls - so decoded PCM is identical by
// construction; the difference is positioning:
//
//   - indexed seek: land on a frame boundary near the target and decode
//     forward a handful of frames at most, regardless of jump distance;
//   - unindexed seek (index still building, or unscannable file): decode
//     forward from the data start discarding, bounded by the jump. No path
//     re-decodes the whole stream a second time.
//
// The streamer matches the semantics of the wrapper it replaces: Stream
// copies converted samples, Err is sticky, Len reports native frames, Seek
// repositions by native frame number.

import (
	"errors"
	"io"
	"os"

	"github.com/mewkiz/flac/frame"
)

var errFlacSignature = errors.New("player: unrecognised FLAC stream")

// flacMaxFrameSamples is the largest legal FLAC block size (4096 in practice,
// 65535 per spec); the conversion buffer is sized once at this bound so
// steady-state decoding never allocates.
const flacMaxFrameSamples = 65535

type flacDecoder struct {
	file      *os.File
	dataStart int64
	inRate    int
	nSamples  int64 // native sample count, 0 when the container leaves it unknown
	channels  int
	bps       int

	loader *flacIndexLoader

	// Decode state (single consumer: the track's producer goroutine).
	buf      []sample // converted, undisclosed samples of the current frame
	bufPos   int
	base     int64 // native sample number of buf[0]
	eof      bool
	err      error
	position int64 // native sample number of the next sample to deliver
}

// newFlacDecoder opens path and builds a positioned decoder around it. The
// metadata walk is synchronous (a handful of seek+reads); the frame index
// builds in the background and lands within a fraction of a second.
func newFlacDecoder(path string) (*flacDecoder, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	md, err := readFlacMetadata(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &flacDecoder{
		file:      file,
		dataStart: md.dataStart,
		inRate:    int(md.info.SampleRate),
		nSamples:  int64(md.info.NSamples),
		channels:  int(md.info.NChannels),
		bps:       int(md.info.BitsPerSample),
		loader:    newFlacIndexLoader(path),
	}, nil
}

// indexReady reports whether the frame index has landed, which makes
// reseeking O(1). The producer's reposition policy gates on this through
// fastSeek().
func (s *flacDecoder) indexReady() bool { return s.loader.index.Load() != nil }

// fastSeek announces the decoder's reposition capability to the track stream:
// cheap only while the frame index is ready.
func (s *flacDecoder) fastSeek() bool { return s.indexReady() }

// flacFrameSample converts a frame header into native sample numbers for the
// decoder's coordinate bookkeeping: fixed blocking carries the FRAME number
// (first sample = Num*BlockSize), variable blocking carries the sample number
// directly.
func flacFrameSample(f *frame.Frame) int64 {
	if f.HasFixedBlockSize {
		return int64(f.Num) * int64(f.BlockSize)
	}
	return int64(f.Num)
}

// convert turns decoded frame samples into the transport's float pairs,
// mirroring the beep FLAC wrapper's conversion exactly (mono duplication,
// first two channels otherwise).
func (s *flacDecoder) convert(frame *frame.Frame) {
	n := len(frame.Subframes[0].Samples)
	if n > flacMaxFrameSamples {
		n = flacMaxFrameSamples
	}
	if cap(s.buf) < n {
		s.buf = make([]sample, n, flacMaxFrameSamples)
	}
	buf := s.buf[:n]
	scale := 1 / float64(int(1)<<(s.bps-1))
	for i := 0; i < n; i++ {
		switch {
		case s.channels == 1:
			left := float64(frame.Subframes[0].Samples[i]) * scale
			buf[i] = sample{left, left}
		default:
			buf[i] = sample{
				float64(frame.Subframes[0].Samples[i]) * scale,
				float64(frame.Subframes[1].Samples[i]) * scale,
			}
		}
	}
	s.buf = buf
}

// refill decodes the next frame at the current file position into the buffer.
// It reports io.EOF at the graceful end of the stream (as frame.New does) and
// adopts a sticky error on decoder failures.
func (s *flacDecoder) refill() error {
	var parsed *frame.Frame
	var err error
	// Silenced like the scan: even a real decode can hit a mewkiz log line
	// (unrecognized header/subframe field values would paint into the TUI).
	flacQuiet(func() { parsed, err = frame.Parse(s.file) })
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		s.eof = true
		return io.EOF
	}
	if err != nil {
		s.err = err
		s.eof = true
		return err
	}
	s.convert(parsed)
	s.bufPos = 0
	s.base = flacFrameSample(parsed)
	return nil
}

// Stream fills out from the decoded frame buffer, pulling frames as needed.
// ok is false only at the real end of data or after a sticky error.
func (s *flacDecoder) Stream(out []sample) (int, bool) {
	if s.err != nil {
		return 0, false
	}
	written := 0
	for i := 0; i < len(out); i++ {
		for s.bufPos >= len(s.buf) {
			if s.eof {
				return written, written > 0
			}
			err := s.refill()
			if err == io.EOF {
				return written, written > 0
			}
			if s.err != nil {
				return written, written > 0
			}
		}
		out[i] = s.buf[s.bufPos]
		s.bufPos++
		s.position++
		written++
	}
	return written, true
}

func (s *flacDecoder) Err() error { return s.err }

func (s *flacDecoder) Len() int { return int(s.nSamples) }

func (s *flacDecoder) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// resetDecodeState clears the frame buffer and error state for a reposition.
func (s *flacDecoder) resetDecodeState() {
	s.buf = s.buf[:0]
	s.bufPos = 0
	s.eof = false
	s.err = nil
}

// Seek repositions the decoder to a native sample number. With a ready index
// the cost is one file seek plus at most a few decoded frames regardless of
// jump distance; without one it decodes forward from the data start,
// discarding - bounded by the jump, never a whole-file re-scan.
func (s *flacDecoder) Seek(position int) error {
	target := int64(position)
	if target < 0 {
		target = 0
	}
	start := s.dataStart
	if ix := s.loader.index.Load(); ix != nil {
		if el, ok := ix.lookup(uint64(target)); ok {
			start = el.offset
		}
		// No covering entry means the index knows nothing before target;
		// falling back to the decode-from-start path is correct.
	}
	if _, err := s.file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	s.resetDecodeState()

	// Decode forward until a frame covers the target; discard earlier frames.
	empty := false // an unindexed seek defers base to the first decoded frame
	s.base = -1
	for !s.eof {
		if err := s.refill(); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if !empty {
			empty = true
		}
		frameEnd := s.base + int64(len(s.buf))
		if frameEnd > target {
			// The covering frame: hand back samples starting at the target.
			if target > s.base {
				s.bufPos = int(target - s.base)
			} else {
				s.bufPos = 0
			}
			s.position = target
			return nil
		}
		s.bufPos = len(s.buf) // discard this frame's samples entirely
	}
	s.position = target
	return nil
}
