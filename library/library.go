package library

import (
	"os"
	"path/filepath"
	"strings"

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

func Scan(root string) ([]Track, error) {
	var tracks []Track
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
		track := Track{Path: path, Title: strings.TrimSuffix(entry.Name(), filepath.Ext(path))}
		if file, openErr := os.Open(path); openErr == nil {
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
		tracks = append(tracks, track)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tracks, nil
}
