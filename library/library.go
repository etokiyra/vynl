package library

import (
	"os"
	"path/filepath"
	"runtime"
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

var extensions = map[string]struct{}{
	".mp3": {}, ".flac": {}, ".wav": {}, ".ogg": {},
}

// maxScanWorkers bounds how many files are opened for tag reading at once.
const maxScanWorkers = 8

// Scan walks root, reading tags for every supported file. Unreadable
// descendants are skipped; an unreadable root is returned as an error. Tracks
// keep the lexical walk order regardless of worker completion order.
func Scan(root string) ([]Track, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
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
	if len(paths) == 0 {
		return nil, nil
	}

	tracks := make([]Track, len(paths))
	workers := min(runtime.NumCPU(), maxScanWorkers, len(paths))
	var next atomic.Int64
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= len(paths) {
					return
				}
				tracks[index] = readTrack(paths[index])
			}
		}()
	}
	group.Wait()
	return tracks, nil
}

// readTrack builds a track from a file path, falling back to the filename when
// tags are missing or unreadable.
func readTrack(path string) Track {
	track := Track{Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
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
