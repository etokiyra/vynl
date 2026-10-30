package player

// flacSeek builds and caches per-file frame indexes for the positioned FLAC
// decoder (see flac_decoder.go). Repositioning a FLAC decoder is not inherently
// cheap: the decoder wrapper used by beep re-decodes the whole stream to build
// an index when the file has no seek table - and vynl builds a fresh decoder
// per track load, so a switch followed by a far seek re-paid a full-file scan.
// vynl therefore walks the file itself once and caches the result:
//
//   - a stored SEEKTABLE is used directly when it is sane (real points,
//     strictly monotone sample numbers);
//   - otherwise the raw frame headers are scanned from the data start. The
//     scan validates a candidate header with mewkiz's CRC-8 (the same check
//     the decoder applies at decode time) and each header carries its own
//     frame/sample number. It parses headers only - no audio decode - and
//     costs ~0.1-0.4 s for a full-length file, once per path.
//
// The decoder still tolerates a missing or late index: unindexed seeks fall
// back to bounded sequential decode-ahead, and bad offsets are caught by the
// frame CRCs.

import (
	"bytes"
	"io"
	"log"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// flacIndexEntry maps the first sample number of a frame to the byte offset of
// that frame's header.
type flacIndexEntry struct {
	sample uint64
	offset int64
}

// flacFrameIndex is a sorted, file-level frame/sample index.
type flacFrameIndex struct {
	entries []flacIndexEntry
}

// lookup returns the entry covering target: the last frame whose first sample
// number is <= target.
func (ix *flacFrameIndex) lookup(target uint64) (flacIndexEntry, bool) {
	if ix == nil || len(ix.entries) == 0 {
		return flacIndexEntry{}, false
	}
	i := sort.Search(len(ix.entries), func(i int) bool {
		return ix.entries[i].sample > target
	})
	if i == 0 {
		return flacIndexEntry{}, false
	}
	return ix.entries[i-1], true
}

// seektablePlaceholder is the sample number flac encoders use to pad a seek
// table out to a caller-requested size.
const seektablePlaceholder = ^uint64(0)

// flacMetadata holds what the index needs from the file's header blocks.
type flacMetadata struct {
	dataStart int64
	info      *meta.StreamInfo
	points    []meta.SeekPoint // stored SEEKTABLE points, nil when absent
}

// readFlacMetadata walks the FLAC signature and the metadata blocks with the
// file's own seek handle (no data copying beyond block bodies) and returns the
// parsed stream info, the offset of the first audio frame, and any stored seek
// points.
func readFlacMetadata(f *os.File) (flacMetadata, error) {
	var md flacMetadata
	if _, err := f.Seek(0, 0); err != nil {
		return md, err
	}
	sig := make([]byte, 4)
	if _, err := ioReadFull(f, sig); err != nil {
		return md, err
	}
	if !bytes.Equal(sig, []byte("fLaC")) {
		return md, errFlacSignature
	}
	pos := int64(4)
	for {
		hdr := make([]byte, 4)
		if _, err := ioReadFull(f, hdr); err != nil {
			return md, err
		}
		// 1 bit: IsLast. 7 bits: type. 24 bits: body length. The block header
		// is parsed from the raw bytes (this walk is seek-based so multi-MB
		// picture blocks are skipped without reads); streaminfo and seek
		// tables go through meta.Parse with their full header+body.
		bodyLen := int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		if bodyLen > 1<<28 {
			return md, errFlacSignature // implausible body length
		}
		blockType := hdr[0] & 0x7f
		if blockType == 0 || blockType == 3 { // StreamInfo / SeekTable
			body := make([]byte, 4+bodyLen)
			copy(body, hdr)
			if _, err := ioReadFull(f, body[4:]); err != nil {
				return md, err
			}
			parsed, err := meta.Parse(bytes.NewReader(body))
			if err != nil {
				return md, err
			}
			if blockType == 0 {
				info, ok := parsed.Body.(*meta.StreamInfo)
				if !ok {
					return md, errFlacSignature
				}
				md.info = info
			} else if table, ok := parsed.Body.(*meta.SeekTable); ok {
				// An unusable seek table is not fatal; the header scan covers it.
				md.points = table.Points
			}
			pos += 4 + bodyLen
		} else {
			// Skip large or irrelevant bodies with a seek rather than a read.
			if _, err := f.Seek(bodyLen, io.SeekCurrent); err != nil {
				return md, err
			}
			pos += 4 + bodyLen
		}
		if hdr[0]&0x80 != 0 { // IsLast
			break
		}
	}
	md.dataStart = pos
	if md.info == nil || md.dataStart <= 0 {
		return flacMetadata{}, errFlacSignature
	}
	return md, nil
}

// storedSeekTableIndex converts a stored seek table into index entries. A
// table containing placeholder padding only, out-of-range or non-monotone
// points is rejected (nil): the header scan is then more trustworthy.
func storedSeekTableIndex(md flacMetadata) *flacFrameIndex {
	if len(md.points) == 0 || md.info == nil || md.dataStart <= 0 {
		return nil
	}
	extent := md.info.NSamples
	entries := make([]flacIndexEntry, 0, len(md.points))
	var previous int64 = -1
	for _, point := range md.points {
		if point.SampleNum == seektablePlaceholder || point.NSamples == 0 {
			continue
		}
		if extent > 0 && point.SampleNum >= extent {
			return nil
		}
		if previous >= 0 && int64(point.SampleNum) <= previous {
			return nil
		}
		entries = append(entries, flacIndexEntry{
			sample: point.SampleNum,
			offset: md.dataStart + int64(point.Offset),
		})
		previous = int64(point.SampleNum)
	}
	if len(entries) == 0 {
		return nil
	}
	return &flacFrameIndex{entries: entries}
}

// flacScanWindowBytes bounds the in-memory chunk used by the header scan.
const flacScanWindowBytes = 1 << 20

// minFlacHeaderBytes is the fixed part of any legal frame header (14-bit sync,
// flags, codes + CRC-8). The next frame's header cannot start earlier.
const minFlacHeaderBytes = 6

// scanFlacHeaderStride bounds how far past the data start a windowed candidate
// scan may rewind, so overlapping windows never re-scan the same byte range.
const flacScanRewind = 4096

// scanFlacIndex walks the raw frames of a seekless FLAC file, validating each
// candidate frame header with the same CRC the decoder applies, recording
// (sample, offset) pairs. Audio is never decoded.
func scanFlacIndex(f *os.File, md flacMetadata) (*flacFrameIndex, error) {
	info := md.info
	if info == nil {
		return nil, errFlacSignature
	}
	size, err := fileSize(f)
	if err != nil {
		return nil, err
	}
	if md.dataStart >= size {
		return nil, errFlacSignature
	}
	entries := make([]flacIndexEntry, 0, 1024)
	window := make([]byte, flacScanWindowBytes)
	var previous uint64
	strategy := -1 // blocking strategy seen so far (unknown / fixed / variable)
	nextCandidate := md.dataStart

	for pos := md.dataStart; pos < size; {
		if _, err := f.Seek(pos, 0); err != nil {
			return nil, err
		}
		read, _ := f.Read(window)
		buf := window[:read]
		limit := len(buf) - minFlacHeaderBytes
		for candidate := 0; candidate <= limit; candidate++ {
			abs := pos + int64(candidate)
			if abs < nextCandidate {
				continue
			}
			// A frame header starts with 0xFF followed by a byte whose top 3
			// bits are the sync remainder 0b111, bit 4..3 hold the blocking
			// bit and the first blocksize bit: legal sync bytes are F8..FB.
			if buf[candidate] != 0xFF || buf[candidate+1]&0xFC != 0xF8 ||
				buf[candidate+1]&0x02 != 0 {
				continue
			}
			frame, err := frame.New(bytes.NewReader(buf[candidate:]))
			if err != nil {
				continue // not a frame header: sync bits alone do not suffice
			}
			// Consistency against the stream info makes false syncs vanishingly
			// rare in compressed data.
			if info.SampleRate != 0 && frame.SampleRate != 0 &&
				frame.SampleRate != info.SampleRate {
				continue
			}
			if info.BitsPerSample != 0 && frame.BitsPerSample != 0 &&
				frame.BitsPerSample != info.BitsPerSample {
				continue
			}
			if frame.BlockSize == 0 {
				continue
			}
			thisStrategy := 0
			if frame.HasFixedBlockSize {
				thisStrategy = 1
			}
			if strategy != -1 && strategy != thisStrategy {
				continue
			}
			var sample uint64
			if frame.HasFixedBlockSize {
				sample = frame.Num * uint64(frame.BlockSize)
			} else {
				sample = frame.Num
			}
			if info.NSamples > 0 && sample >= info.NSamples {
				continue
			}
			if len(entries) > 0 && sample <= previous {
				continue
			}
			entries = append(entries, flacIndexEntry{
				sample: sample,
				offset: abs,
			})
			previous = sample
			strategy = thisStrategy
			nextCandidate = abs + minFlacHeaderBytes
			candidate += minFlacHeaderBytes - 1
		}
		if read < len(window) {
			break
		}
		// Advance past what was scanned, re-reading a small tail so headers
		// spanning the window boundary are found in the next window.
		pos += int64(len(buf)) - flacScanRewind
	}
	if len(entries) == 0 {
		return nil, errFlacSignature
	}
	return &flacFrameIndex{entries: entries}, nil
}

// flacCacheEntry is one cached index keyed by path and file identity.
type flacCacheEntry struct {
	key   flacIndexKey
	index *flacFrameIndex
}

// flacIndexKey identifies an (immutable) build of a file's index.
type flacIndexKey struct {
	path    string
	size    int64
	modNano int64
}

// flacIndexCache is a small process-wide index cache: switching back to a
// previously played track reuses the scan instantly.
var flacIndexCache = struct {
	mu      sync.Mutex
	entries []flacCacheEntry
	max     int
}{max: 32}

// cachedFlacIndex returns a cached index for path, or nil.
func cachedFlacIndex(path string) *flacFrameIndex {
	key, ok := flacKeyFor(path)
	if !ok {
		return nil
	}
	flacIndexCache.mu.Lock()
	defer flacIndexCache.mu.Unlock()
	for _, entry := range flacIndexCache.entries {
		if entry.key == key {
			return entry.index
		}
	}
	return nil
}

// cacheFlacIndex stores a freshly built index, evicting the whole map when the
// bound is hit (small, rare, and the entries are instantly rebuildable).
func cacheFlacIndex(path string, ix *flacFrameIndex) {
	key, ok := flacKeyFor(path)
	if !ok {
		return
	}
	flacIndexCache.mu.Lock()
	defer flacIndexCache.mu.Unlock()
	for _, entry := range flacIndexCache.entries {
		if entry.key == key {
			return
		}
	}
	if len(flacIndexCache.entries) >= flacIndexCache.max {
		flacIndexCache.entries = flacIndexCache.entries[:0]
	}
	flacIndexCache.entries = append(flacIndexCache.entries,
		flacCacheEntry{key: key, index: ix})
}

func flacKeyFor(path string) (flacIndexKey, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return flacIndexKey{}, false
	}
	return flacIndexKey{
		path:    path,
		size:    stat.Size(),
		modNano: stat.ModTime().UnixNano(),
	}, true
}

// flacLogState refcounts the standard library logger swap used below:
// mewkiz's frame parser logs "not yet implemented"-style warnings through
// log.Printf for header field values it does not recognize - which fires for
// thousands of raw-byte candidates during the index scan (and occasionally
// for decoder frames) - straight at STDERR. In a TUI that paints garbage into
// the running interface, so every call into the parser is silenced.
type flacLogState struct {
	mu    sync.Mutex
	depth int
	prev  io.Writer
}

var flacLog flacLogState

// flacQuiet runs fn with the process logger silenced. The swap is
// refcounted rather than guarded: concurrent silent sections (the background
// index build and producer decode both reach the parser) keep the logger
// discarded until the last one exits, so an audio-path section never stalls
// on the index build.
func flacQuiet(fn func()) {
	flacLog.mu.Lock()
	flacLog.depth++
	if flacLog.depth == 1 {
		flacLog.prev = log.Writer()
		log.SetOutput(io.Discard)
	}
	flacLog.mu.Unlock()
	fn()
	flacLog.mu.Lock()
	flacLog.depth--
	if flacLog.depth == 0 {
		log.SetOutput(flacLog.prev)
		flacLog.prev = nil
	}
	flacLog.mu.Unlock()
}

// flacIndexLoader builds the index in the background the moment a decoder is
// constructed, and publishes it via an atomic pointer when done. The staged
// build publishes the stored seek table first when the file has one (coarse
// but ready immediately), then upgrades to the frame-exact header scan, which
// makes every later seek land within a single frame of the target. The build
// is allowed to outlive the decoder (it is cheap, deterministic and cached, so
// the next load of the same path finds it ready).
type flacIndexLoader struct {
	index atomic.Pointer[flacFrameIndex]
	done  chan struct{}
}

func newFlacIndexLoader(path string) *flacIndexLoader {
	loader := &flacIndexLoader{done: make(chan struct{})}
	if ix := cachedFlacIndex(path); ix != nil {
		loader.index.Store(ix)
		close(loader.done)
		return loader
	}
	go func() {
		defer close(loader.done)
		if _, err := buildFlacIndexStaged(path, loader); err != nil {
			return
		}
	}()
	return loader
}

// buildFlacIndexStaged populates the loader: the stored seek table first when
// it is sane (coarse but immediate), then the frame-exact scan replaces it.
// It returns whether any index ended up published.
func buildFlacIndexStaged(path string, loader *flacIndexLoader) (published bool, err error) {
	// The scan parses thousands of raw-byte header candidates; mewkiz logs on
	// unrecognized field values, which must never reach the terminal. The
	// named returns make the deferred silence wrapper tidy.
	flacQuiet(func() { published, err = buildFlacIndexStagedLocked(path, loader) })
	return published, err
}

func buildFlacIndexStagedLocked(path string, loader *flacIndexLoader) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	md, err := readFlacMetadata(f)
	if err != nil {
		return false, err
	}
	// Stage 1: the stored seek table, coarse but immediately usable.
	if ix := storedSeekTableIndex(md); ix != nil {
		loader.index.Store(ix)
	}
	// Stage 2: the frame-exact scan replaces the coarse table.
	ix, err := scanFlacIndex(f, md)
	if err != nil {
		// No scan possible: the stored table (or nothing) stands.
		if loaded := loader.index.Load(); loaded != nil {
			cacheFlacIndex(path, loaded)
			return true, nil
		}
		return false, err
	}
	loader.index.Store(ix)
	cacheFlacIndex(path, ix)
	return true, nil
}

// wait blocks until either the index is available or the build finished
// without one (error or unscannable file). It never blocks indefinitely.
func (l *flacIndexLoader) wait(timeout time.Duration) {
	if timeout <= 0 {
		<-l.done
		return
	}
	select {
	case <-l.done:
	case <-time.After(timeout):
	}
}

// fileSize is a small helper for os.File.Stat().Size().
func fileSize(f *os.File) (int64, error) {
	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

func ioReadFull(f *os.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
