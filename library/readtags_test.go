package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func TestScanPathsFiltersAndOrdersFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"set/first.mp3", "second.FLAC", "ignore.txt"} {
		path := filepath.Join(root, name)
		if err := writeFile(path, []byte("not audio metadata")); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := ScanPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "second.FLAC"), filepath.Join(root, "set", "first.mp3")}
	if len(paths) != len(want) {
		t.Fatalf("ScanPaths found %d paths, want %d: %v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestScanPathsContextCancellationStopsWalk(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.mp3", "b.flac", "c.wav"} {
		if err := writeFile(filepath.Join(root, name), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	paths, err := ScanPathsContext(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ScanPathsContext error = %v, want context.Canceled", err)
	}
	if paths != nil {
		t.Fatalf("cancelled ScanPathsContext returned %v, want no paths", paths)
	}
}

func TestReadTagsDeliversEveryPathExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 40)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("missing-%d.mp3", i))
	}
	seen := make([]int, len(paths))
	for batch := range ReadTags(context.Background(), paths, 4) {
		for _, update := range batch {
			if update.Index < 0 || update.Index >= len(paths) {
				t.Fatalf("update index %d out of range", update.Index)
			}
			seen[update.Index]++
			if update.Track.Path != paths[update.Index] {
				t.Fatalf("update %d path = %q, want %q", update.Index, update.Track.Path, paths[update.Index])
			}
		}
	}
	for index, count := range seen {
		if count != 1 {
			t.Fatalf("index %d delivered %d times, want exactly once", index, count)
		}
	}
}

func TestReadTagsEmptyClosesImmediately(t *testing.T) {
	out := ReadTags(context.Background(), nil, 4)
	if _, ok := <-out; ok {
		t.Fatal("empty scan did not close its channel")
	}
}

// TestReadTagsDeliversIncrementally proves the first result is delivered before
// the rest of the scan completes, so the UI can populate as tags arrive.
func TestReadTagsDeliversIncrementally(t *testing.T) {
	paths := []string{"fast", "slow-1", "slow-2", "slow-3"}
	release := make(chan struct{})
	original := tagReader
	tagReader = func(path string) Track {
		if path != "fast" {
			<-release
		}
		return Track{Path: path, Title: path}
	}
	defer func() { tagReader = original }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := ReadTags(ctx, paths, len(paths))

	select {
	case batch, ok := <-out:
		if !ok {
			t.Fatal("channel closed before the first result")
		}
		found := false
		for _, update := range batch {
			if update.Track.Path == "fast" {
				found = true
			}
		}
		if !found {
			t.Fatalf("first batch %+v did not include the fast result", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first incremental batch")
	}
	close(release)
	for range out {
	}
}

// TestReadTagsCancellationClosesChannel asserts the strongest leak check the
// API exposes: the channel closes only after every worker has exited, so a
// closed channel after ctx cancellation means no scan goroutine leaked.
func TestReadTagsCancellationClosesChannel(t *testing.T) {
	paths := make([]string, 64)
	for i := range paths {
		paths[i] = fmt.Sprintf("path-%d", i)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	original := tagReader
	tagReader = func(path string) Track {
		once.Do(func() { close(entered) })
		<-release
		return Track{Path: path}
	}
	defer func() { tagReader = original }()

	ctx, cancel := context.WithCancel(context.Background())
	out := ReadTags(ctx, paths, 4)
	<-entered
	cancel()
	close(release)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("ReadTags did not close its channel after cancellation")
		}
	}
}
