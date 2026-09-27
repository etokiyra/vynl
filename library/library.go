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
			return walkErr
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
