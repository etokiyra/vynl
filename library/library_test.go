package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanSkipsUnreadableEntries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep/first.mp3", "locked/hidden.mp3"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not audio metadata"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	tracks, err := Scan(root)
	if err != nil {
		t.Fatalf("scan aborted on an unreadable entry: %v", err)
	}
	if len(tracks) != 1 || filepath.Base(filepath.Dir(tracks[0].Path)) != "keep" {
		t.Fatalf("scanned tracks = %+v, want only the readable track", tracks)
	}
}

func TestScanReportsUnreadableRoot(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("scanning a missing root should return an error")
	}
}

func TestScanPreservesWalkOrder(t *testing.T) {
	root := t.TempDir()
	var names []string
	for i := 0; i < 25; i++ {
		name := "track-" + string(rune('a'+i)) + ".mp3"
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(root, name), []byte("not audio metadata"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tracks, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != len(names) {
		t.Fatalf("scanned %d tracks, want %d", len(tracks), len(names))
	}
	for i, track := range tracks {
		if want := filepath.Join(root, names[i]); track.Path != want {
			t.Fatalf("track %d = %q, want %q", i, track.Path, want)
		}
	}
}

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
