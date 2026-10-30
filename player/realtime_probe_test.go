package player

// REALTIME PROBE — the user's end-to-end scenarios against real FLAC files,
// paced at the real audio-callback cadence (2048 frames / ~46.4 ms), driven
// through the engine's own queue/transport paths with a fake callback and
// engine loop:
//   - switch song then skip forward once (the original report),
//   - jump to the middle and to the end of a long file (the follow-up report).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/gopxl/beep/speaker"
)

const probePeriod = 46_440_000 * time.Nanosecond // 2048 frames @ 44.1 kHz realtime

func TestProbeRealtimeFLACSwitchAndJump(t *testing.T) {
	paths := []string{
		"/tmp/opencode/flacprobe/noise240_notbl.flac", // the user's case: no seek table
		"/tmp/opencode/flacprobe/noise240_tbl.flac",   // carrier of a stored table
	}
	for _, path := range paths {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("FLAC probe asset unavailable (dev environment only): %v", err)
			}
			engine := engineWithTracks(t, tracksFor(path))
			t.Cleanup(engine.shutdownStreams)
			engine.repeat = RepeatAll
			engine.rebuildOrder(0)
			engine.selectTrack(0, true)
			if engine.current == nil {
				t.Fatal("track failed to load")
			}

			out := make([]sample, 2048)
			next := time.Now().Add(probePeriod)
			callback := func() {
				if d := time.Until(next); d > 0 {
					time.Sleep(d)
				}
				next = time.Now().Add(probePeriod)
				speaker.Lock()
				if !engine.control.Paused {
					engine.queue.Stream(out)
				}
				speaker.Unlock()
			}
			awaitAudio := func(start time.Time, limit time.Duration, label string) time.Duration {
				for time.Since(start) < limit {
					callback()
					speaker.Lock()
					buffering := engine.queue.primary == nil ||
						engine.queue.primary.transport.Buffering()
					speaker.Unlock()
					if !buffering {
						break
					}
				}
				return time.Since(start)
			}

			// 0.3 s of playback, then skip forward once (the original report).
			for i := 0; i < 6; i++ {
				callback()
			}
			before := time.Now()
			speaker.Lock()
			primary := engine.queue.primary
			primary.transport.Seek(primary.transport.PositionSeconds() + 5)
			speaker.Unlock()
			absorb := awaitAudio(before, 60*time.Second, "skip-forward")
			t.Logf("switch+skip+5s absorbed in %v (position %.2f)", absorb, primary.transport.PositionSeconds())
			if absorb > 2*time.Second {
				t.Errorf("switch+skip+5s took %v; want well under 2 s", absorb)
			}

			// Mid-file and near-end jumps (the follow-up report). The index
			// built during the load; give the loader a beat regardless.
			engine.current.decoder.fastSeekCapable()
			lengthSeconds := float64(engine.current.decoder.totalFrames) / outputRate
			jump := func(label string, seconds float64) {
				before := time.Now()
				speaker.Lock()
				engine.queue.primary.transport.Seek(seconds)
				speaker.Unlock()
				absorb := awaitAudio(before, 60*time.Second, label)
				t.Logf("%s jump absorbed in %v (position %.2f)", label, absorb,
					engine.queue.primary.transport.PositionSeconds())
				if absorb > 2*time.Second {
					t.Errorf("%s jump took %v; want well under 2 s", label, absorb)
				}
			}
			jump("mid-file", lengthSeconds/2)
			jump("near-end", lengthSeconds-0.5)
		})
	}
}

func tracksFor(paths ...string) []library.Track {
	tags := make([]library.Track, len(paths))
	for i, path := range paths {
		tags[i] = library.Track{Path: path, Title: filepath.Base(path)}
	}
	return tags
}
