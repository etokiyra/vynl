package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/etokiyra/vynl/config"
	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/etokiyra/vynl/ui"
)

// version is the release string, overridable at build time with
// -ldflags "-X main.version=vX.Y.Z". It defaults to "dev" for source builds.
var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to TOML config file")
	musicDir := flag.String("music-dir", "", "directory to scan for music")
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionLine())
		return
	}

	path := *configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	if *musicDir != "" {
		cfg.MusicDir = config.ExpandHome(*musicDir)
	}

	statePath := config.StatePath(path)
	state, stateErr := config.LoadState(statePath)
	if stateErr != nil {
		fmt.Fprintf(os.Stderr, "vynl: ignoring unreadable state file %s: %v\n", statePath, stateErr)
	}

	paths, err := library.ScanPaths(cfg.MusicDir)
	if err != nil {
		log.Fatal(err)
	}
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "No supported tracks found in %s\n", filepath.Clean(cfg.MusicDir))
		return
	}
	tracks := library.FallbackTracks(paths)

	engine, err := player.NewEngine(tracks, player.InitialState{
		Volume:  state.Volume,
		Muted:   state.Muted,
		EQ:      state.EQ,
		Shuffle: state.Shuffle,
		Vinyl:   state.Vinyl,
		Repeat:  player.RepeatMode(state.Repeat),
		// ReplayGain is a config preference (not persisted runtime state).
		ReplayGain:  cfg.Playback.ReplayGain,
		PreampDB:    cfg.Playback.PreampDB,
		EQLowHz:     cfg.EQ.LowHz,
		EQHighHz:    cfg.EQ.HighHz,
		CrossfadeMS: cfg.Playback.CrossfadeMS,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close()

	// Read tags off the UI thread and stream them into the event loop. The scan
	// stops when the program exits: any quit path cancels this context.
	scanCtx, stopScan := context.WithCancel(context.Background())
	defer stopScan()
	tagUpdates := library.ReadTags(scanCtx, paths, runtime.NumCPU())

	program := tea.NewProgram(
		ui.NewModel(tracks, engine, cfg).WithTagUpdates(tagUpdates, stopScan),
		tea.WithAltScreen(),
	)
	finalModel, runErr := program.Run()

	// A rescan replaces the initial scan context with its own; cancel whatever
	// walk/tag scan is current so no scan goroutine outlives the TUI. (The
	// initial context is also cancelled by the deferred stopScan.)
	if model, ok := finalModel.(ui.Model); ok {
		model.CancelScan()
	}

	// Persist on every exit path: the quit key, Ctrl+C, and SIGINT/SIGTERM
	// (Bubble Tea turns those into a clean Run return or ErrInterrupted). This
	// runs on the main goroutine, not the UI thread, and reads a race-free
	// engine snapshot.
	persistState(engine, statePath)

	if runErr != nil && !errors.Is(runErr, tea.ErrInterrupted) {
		log.Print(runErr)
	}
}

// versionLine is the output of -version. It includes the Go toolchain and
// platform so a bug report can be reproduced, plus a VCS revision when the
// binary was built from a git checkout.
func versionLine() string {
	line := fmt.Sprintf("vynl %s (%s %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if info, ok := debug.ReadBuildInfo(); ok {
		revision, modified := "", false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if revision != "" {
			if len(revision) > 12 {
				revision = revision[:12]
			}
			if modified {
				revision += "+dirty"
			}
			line += " rev " + revision
		}
	}
	return line
}

// persistState writes the engine's latest playback settings beside the config.
func persistState(engine *player.Engine, path string) {
	snapshot := engine.PersistState()
	state := config.State{
		Volume:  snapshot.Volume,
		Muted:   snapshot.Muted,
		EQ:      snapshot.EQ,
		Shuffle: snapshot.Shuffle,
		Vinyl:   snapshot.Vinyl,
		Repeat:  string(snapshot.Repeat),
	}
	if err := config.SaveState(path, state); err != nil {
		fmt.Fprintf(os.Stderr, "vynl: could not save playback state: %v\n", err)
	}
}
