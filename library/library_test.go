package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsSupportedFilesRecursively(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"set/first.mp3", "second.FLAC", "ignore.txt"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not audio metadata"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tracks, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("found %d tracks, want 2", len(tracks))
	}
	if tracks[0].Title == "" || tracks[1].Title == "" {
		t.Fatalf("fallback titles are missing: %+v", tracks)
	}
}
