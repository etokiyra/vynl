package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/etokiyra/vynl/config"
	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/etokiyra/vynl/ui"
)

func main() {
	configPath := flag.String("config", "", "path to TOML config file")
	musicDir := flag.String("music-dir", "", "directory to scan for music")
	flag.Parse()

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

	tracks, err := library.Scan(cfg.MusicDir)
	if err != nil {
		log.Fatal(err)
	}
	if len(tracks) == 0 {
		fmt.Fprintf(os.Stderr, "No supported tracks found in %s\n", filepath.Clean(cfg.MusicDir))
		return
	}

	engine, err := player.NewEngine(tracks)
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close()
	program := tea.NewProgram(ui.NewModel(tracks, engine, cfg), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Print(err)
	}
}
