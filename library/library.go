package library

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dhowden/tag"
)

type Track struct {
	Path   string
	Title  string
	Artist string
	Album  string
}

// ReplayGain is the loudness-normalization metadata read from a track's tags,
// in dB. TrackDB/AlbumDB are the ReplayGain track/album gains.
type ReplayGain struct {
	TrackDB  float64
	AlbumDB  float64
	HasTrack bool
	HasAlbum bool
}

var extensions = map[string]struct{}{
	".mp3": {}, ".flac": {}, ".wav": {}, ".ogg": {},
}

// maxScanWorkers bounds how many files are opened for tag reading at once.
const maxScanWorkers = 8

// maxTagBatch bounds how many tag updates are coalesced into one channel send.
// The reader flushes sooner when results are sparse, so delivery stays
// incremental.
const maxTagBatch = 512

// TagUpdate reports the tagged metadata for the track at Index.
type TagUpdate struct {
	Index int
	Track Track
}

// ScanPaths walks root and returns the paths of supported audio files in
// lexical walk order. It only stats the tree and never opens a file, so it is
// the cheap, synchronous phase of a scan. Unreadable descendants are skipped;
// an unreadable root is returned as an error.
func ScanPaths(root string) ([]string, error) {
	return ScanPathsContext(context.Background(), root)
}

// ScanPathsContext is ScanPaths with cancellation. It returns ctx.Err() if ctx
// is cancelled before the walk finishes, which lets a rescan (or exit) abort an
// in-flight walk instead of leaking a goroutine until a huge tree is walked.
// A nil ctx is treated as a background context.
func ScanPathsContext(ctx context.Context, root string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if walkErr != nil {
			// One unreadable folder should not make the whole library
			// unscannable: skip the entry and keep walking. A failure to read
			// the root itself is still fatal so a bad music directory is
			// reported to the user.
			if path == root {
				return walkErr
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := extensions[strings.ToLower(filepath.Ext(path))]; !ok {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// FallbackTracks builds a track for each path using the filename as the title.
// It is the metadata shown before tags have been read.
func FallbackTracks(paths []string) []Track {
	tracks := make([]Track, len(paths))
	for i, path := range paths {
		tracks[i] = fallbackTrack(path)
	}
	return tracks
}

func fallbackTrack(path string) Track {
	return Track{Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
}

// tagReader reads metadata for one path. It is a variable so tests can
// substitute a blocking reader to exercise cancellation deterministically.
var tagReader = readTrack

// ReadTags reads tags for paths concurrently and delivers updates in batches on
// the returned channel. Every index is delivered exactly once (with the
// filename fallback when tags are missing or unreadable), so a consumer can
// count up to len(paths). The channel is closed once all updates have been
// delivered or ctx is cancelled. Closing is also the "no goroutine leak"
// signal: the coordinator only closes after the workers have exited.
//
// Callers must drain the channel or cancel ctx; the workers block on a full
// buffer otherwise (deliberate backpressure, since this runs off the UI thread).
func ReadTags(ctx context.Context, paths []string, workers int) <-chan []TagUpdate {
	out := make(chan []TagUpdate)
	if len(paths) == 0 {
		close(out)
		return out
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers > maxScanWorkers {
		workers = maxScanWorkers
	}

	results := make(chan TagUpdate, workers)
	var group sync.WaitGroup
	group.Add(workers)
	var next atomic.Int64
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer group.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				index := int(next.Add(1)) - 1
				if index >= len(paths) {
					return
				}
				update := TagUpdate{Index: index, Track: tagReader(paths[index])}
				select {
				case <-ctx.Done():
					return
				case results <- update:
				}
			}
		}()
	}
	go func() {
		group.Wait()
		close(results)
	}()

	go func() {
		// Wait for every worker before closing out even on the cancellation
		// path: the channel closing is the documented "no goroutine leaked"
		// signal, so it must never precede worker exit.
		defer func() {
			group.Wait()
			close(out)
		}()
		batch := make([]TagUpdate, 0, maxTagBatch)
		flush := func() bool {
			if len(batch) == 0 {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case out <- batch:
				batch = make([]TagUpdate, 0, maxTagBatch)
				return true
			}
		}
		for {
			update, ok := <-results
			if !ok {
				flush()
				return
			}
			batch = append(batch, update)
			// Coalesce everything already waiting, then flush: sparse results
			// are delivered immediately, bursts are batched.
		drain:
			for len(batch) < maxTagBatch {
				select {
				case more, ok := <-results:
					if !ok {
						flush()
						return
					}
					batch = append(batch, more)
				default:
					break drain
				}
			}
			if !flush() {
				return
			}
		}
	}()
	return out
}

// Scan walks root and reads tags for every supported file. It is the
// synchronous convenience wrapper over ScanPaths + ReadTags, kept for callers
// that do not need incremental delivery. Tracks keep the lexical walk order
// regardless of worker completion order.
func Scan(root string) ([]Track, error) {
	paths, err := ScanPaths(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	tracks := FallbackTracks(paths)
	workers := min(runtime.NumCPU(), maxScanWorkers)
	for batch := range ReadTags(context.Background(), paths, workers) {
		for _, update := range batch {
			tracks[update.Index] = update.Track
		}
	}
	return tracks, nil
}

// readTrack builds a track from a file path, falling back to the filename when
// tags are missing or unreadable.
func readTrack(path string) Track {
	track := fallbackTrack(path)
	if file, err := os.Open(path); err == nil {
		metadata, metaErr := tag.ReadFrom(file)
		_ = file.Close()
		if metaErr == nil {
			if metadata.Title() != "" {
				track.Title = metadata.Title()
			}
			track.Artist = metadata.Artist()
			track.Album = metadata.Album()
		}
	}
	return track
}

// ReadReplayGain reads only the ReplayGain metadata for path. The engine calls
// it when it loads a track: the UI's tag stream runs on the event loop and the
// engine owns an independent track list, so it cannot rely on the UI's copy. A
// missing, unreadable, or untagged file yields zero-value metadata, which makes
// normalization a no-op for that track.
func ReadReplayGain(path string) ReplayGain {
	file, err := os.Open(path)
	if err != nil {
		return ReplayGain{}
	}
	defer file.Close()
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return ReplayGain{}
	}
	return replayGainFromRaw(metadata.Raw())
}

// maxReplayGainDB bounds a tag value so a corrupt or hostile tag cannot blow up
// the output. Values outside [-24, +24] dB are ignored.
const maxReplayGainDB = 24

// replayGainFromRaw extracts ReplayGain track/album gains (dB) from tag
// metadata. Vorbis comments (FLAC/OGG) use lowercase replaygain_* keys; ID3v2
// stores them in TXXX frames, which dhowden/tag exposes as *tag.Comm values
// under "TXXX"/"TXXX_n" keys. Missing or malformed values are ignored.
func replayGainFromRaw(raw map[string]interface{}) ReplayGain {
	var gain ReplayGain
	for key, value := range raw {
		lower := strings.ToLower(key)
		switch lower {
		case "replaygain_track_gain":
			if db, ok := parseGainValue(value); ok {
				gain.TrackDB, gain.HasTrack = db, true
			}
		case "replaygain_album_gain":
			if db, ok := parseGainValue(value); ok {
				gain.AlbumDB, gain.HasAlbum = db, true
			}
		default:
			// ID3v2 user-defined text frames.
			if !strings.HasPrefix(lower, "txxx") {
				continue
			}
			comm, ok := value.(*tag.Comm)
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(comm.Description)) {
			case "replaygain_track_gain":
				if db, ok := parseGainText(comm.Text); ok {
					gain.TrackDB, gain.HasTrack = db, true
				}
			case "replaygain_album_gain":
				if db, ok := parseGainText(comm.Text); ok {
					gain.AlbumDB, gain.HasAlbum = db, true
				}
			}
		}
	}
	return gain
}

// parseGainValue accepts the metadata forms ReplayGain uses: Vorbis comments
// store a string, TXXX stores a *tag.Comm, and some writers store a number.
func parseGainValue(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case string:
		return parseGainText(v)
	case *tag.Comm:
		return parseGainText(v.Text)
	case float64:
		return validGainDB(v)
	case float32:
		return validGainDB(float64(v))
	}
	return 0, false
}

// parseGainText parses a textual gain such as "-7.23 dB" or "+2.0 dB".
func parseGainText(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if len(text) >= 2 && strings.EqualFold(text[len(text)-2:], "db") {
		text = strings.TrimSpace(text[:len(text)-2])
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return validGainDB(value)
}

func validGainDB(value float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > maxReplayGainDB {
		return 0, false
	}
	return value, true
}
