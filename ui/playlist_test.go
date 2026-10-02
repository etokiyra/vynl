package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/etokiyra/vynl/config"
	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/etokiyra/vynl/playlist"
)

func keyRunes(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

func playlistModel(t *testing.T, tracks []library.Track) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	model := NewModel(tracks, &player.Engine{}, config.Defaults()).WithPlaylists(dir, nil)
	return model, dir
}

func TestPlanPlaybackCommands(t *testing.T) {
	tracks := []library.Track{{Path: "a.flac"}, {Path: "b.flac"}}

	// Already on the source: only a Select.
	same := planPlayback(false, 0, tracks, 1)
	if len(same) != 1 || same[0].Action != player.Select || same[0].Value != 1 {
		t.Fatalf("same-source commands = %+v, want a single Select 1", same)
	}

	// Switching sources: a revisioned SetTracks then the Select.
	swapped := planPlayback(true, 7, tracks, 0)
	if len(swapped) != 2 {
		t.Fatalf("swap commands = %+v, want two", swapped)
	}
	if swapped[0].Action != player.SetTracks || swapped[0].Revision != 7 || len(swapped[0].Tracks) != 2 {
		t.Fatalf("first command = %+v, want SetTracks rev 7", swapped[0])
	}
	if swapped[1].Action != player.Select || swapped[1].Value != 0 {
		t.Fatalf("second command = %+v, want Select 0", swapped[1])
	}
}

func TestBrowseToggleSwitchesPanel(t *testing.T) {
	model := NewModel([]library.Track{{Path: "a.flac"}}, nil, config.Defaults())
	if model.browse != viewLibrary {
		t.Fatal("model did not start on the library")
	}
	updated, _ := model.Update(keyRunes("l"))
	model = updated.(Model)
	if model.browse != viewPlaylists {
		t.Fatal("playlists key did not switch the browser")
	}
	model.width, model.height = 100, 30
	if view := ansi.Strip(model.View()); !strings.Contains(view, "PLAYLISTS") {
		t.Fatalf("playlist panel missing from view:\n%s", view)
	}
	updated, _ = model.Update(keyRunes("l"))
	if updated.(Model).browse != viewLibrary {
		t.Fatal("playlists key did not switch back to the library")
	}
}

func TestCreatePlaylistViaPromptAndPersist(t *testing.T) {
	model, dir := playlistModel(t, nil)

	model = press(t, model, keyRunes("l"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlN})
	if model.prompt != promptNewPlaylist {
		t.Fatal("ctrl+n did not open the new-playlist prompt")
	}
	model = press(t, model, keyRunes("Road Trip"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(model.playlists) != 1 || model.playlists[0].Name != "Road Trip" {
		t.Fatalf("playlists = %+v, want one named Road Trip", model.playlists)
	}
	if model.openPlaylist != 0 {
		t.Fatalf("openPlaylist = %d, want 0 after creating", model.openPlaylist)
	}
	file := playlist.FileName(dir, "Road Trip")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("playlist file not written: %v", err)
	}
}

func TestSanitizedPlaylistName(t *testing.T) {
	model, dir := playlistModel(t, nil)
	model = press(t, model, keyRunes("l"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlN})
	model = press(t, model, keyRunes("a/b"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(model.playlists) != 1 || model.playlists[0].Name != "ab" {
		t.Fatalf("playlists = %+v, want sanitized name ab", model.playlists)
	}
	if _, err := os.Stat(playlist.FileName(dir, "ab")); err != nil {
		t.Fatalf("sanitized playlist file missing: %v", err)
	}
}

func TestDuplicatePlaylistNameRejected(t *testing.T) {
	model, _ := playlistModel(t, nil)
	model = press(t, model, keyRunes("l"))
	for _, name := range []string{"Mix", "Mix"} {
		model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlN})
		model = press(t, model, keyRunes(name))
		model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if len(model.playlists) != 1 {
		t.Fatalf("duplicate playlist name created a second playlist: %+v", model.playlists)
	}
	if !strings.Contains(model.notice, "already exists") {
		t.Fatalf("notice = %q, want a duplicate warning", model.notice)
	}
}

func TestAddSelectedPlayingAndAllToPlaylist(t *testing.T) {
	tracks := []library.Track{
		{Path: "/music/a.flac", Title: "A"},
		{Path: "/music/b.flac", Title: "B"},
	}
	model, dir := playlistModel(t, tracks)
	model.status = player.Status{Track: library.Track{Path: "/music/b.flac", Title: "B"}}
	model.selected = 0 // library cursor on A

	// Create and open a playlist, then add from both the library and playback.
	model = press(t, model, keyRunes("l"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlN})
	model = press(t, model, keyRunes("Mix"))
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	model = press(t, model, keyRunes("A")) // add selected (A)
	model = press(t, model, keyRunes("a")) // add playing (B)
	model = press(t, model, tea.KeyMsg{Type: tea.KeyCtrlA})

	loaded, err := playlist.Load(playlist.FileName(dir, "Mix"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/music/a.flac", "/music/b.flac", "/music/a.flac", "/music/b.flac"}
	if strings.Join(loaded.Paths, ",") != strings.Join(want, ",") {
		t.Fatalf("saved paths = %v, want %v", loaded.Paths, want)
	}
	if len(model.playlistTracks) != len(want) {
		t.Fatalf("resolved tracks = %d, want %d", len(model.playlistTracks), len(want))
	}
}

func TestPlaylistReorderAndRemove(t *testing.T) {
	tracks := []library.Track{{Path: "/m/a.flac", Title: "A"}, {Path: "/m/b.flac", Title: "B"}}
	model, dir := playlistModel(t, tracks)
	model.playlists = []playlist.Playlist{{
		Name: "Mix", File: playlist.FileName(dir, "Mix"),
		Paths: []string{"/m/a.flac", "/m/b.flac"},
	}}
	model.browse = viewPlaylists
	model.openPlaylistAt(0)
	model.playlistSelected = 0

	// Move the first entry down.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := model.playlists[0].Paths; got[0] != "/m/b.flac" || got[1] != "/m/a.flac" {
		t.Fatalf("reorder paths = %v, want b then a", got)
	}
	if model.playlistSelected != 1 {
		t.Fatalf("cursor after reorder = %d, want 1", model.playlistSelected)
	}

	// Remove the selected entry.
	model = press(t, model, keyRunes("d"))
	if len(model.playlists[0].Paths) != 1 || model.playlists[0].Paths[0] != "/m/b.flac" {
		t.Fatalf("paths after remove = %v, want [b]", model.playlists[0].Paths)
	}
	loaded, err := playlist.Load(playlist.FileName(dir, "Mix"))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 1 {
		t.Fatalf("saved playlist did not reflect the removal: %v", loaded.Paths)
	}
}

func TestDeletePlaylistRequiresConfirmation(t *testing.T) {
	model, dir := playlistModel(t, nil)
	file := playlist.FileName(dir, "Gone")
	if err := playlist.Save(playlist.Playlist{Name: "Gone", File: file}); err != nil {
		t.Fatal(err)
	}
	model.playlists = []playlist.Playlist{{Name: "Gone", File: file}}
	model.browse = viewPlaylists

	model = press(t, model, keyRunes("d"))
	if model.confirmDelete != 0 {
		t.Fatal("delete key did not ask for confirmation")
	}
	// Escape cancels and keeps the file.
	model = press(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.confirmDelete != -1 {
		t.Fatal("escape did not cancel the confirmation")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("cancelled delete removed the playlist")
	}

	model = press(t, model, keyRunes("d"))
	model = press(t, model, keyRunes("y"))
	if len(model.playlists) != 0 {
		t.Fatalf("playlist not deleted: %+v", model.playlists)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("playlist file still present after confirmed delete: %v", err)
	}
}

func TestPlaylistMarksUnavailableEntries(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone.mp3")
	model := NewModel(nil, &player.Engine{}, config.Defaults()).WithPlaylists(dir, []playlist.Playlist{{
		Name: "Mix", File: playlist.FileName(dir, "Mix"),
		Paths: []string{good, missing},
	}})
	model.browse = viewPlaylists
	model.openPlaylistAt(0)

	if model.playlistMissing[0] {
		t.Fatal("existing file was marked unavailable")
	}
	if !model.playlistMissing[1] {
		t.Fatal("missing file was not marked unavailable")
	}
	model.width, model.height = 100, 30
	panel := ansi.Strip(model.playlistPanel(60, 24, model.palette()))
	if !strings.Contains(panel, "1 unavailable") {
		t.Fatalf("panel did not report the unavailable entry:\n%s", panel)
	}
}

func TestPlaylistPlaybackSetsActiveSource(t *testing.T) {
	tracks := []library.Track{{Path: "/m/a.flac", Title: "A"}, {Path: "/m/b.flac", Title: "B"}}
	model, dir := playlistModel(t, tracks)
	model.playlists = []playlist.Playlist{{
		Name: "Mix", File: playlist.FileName(dir, "Mix"),
		Paths: []string{"/m/b.flac", "/m/a.flac"},
	}}
	model.browse = viewPlaylists
	model.openPlaylistAt(0)

	model = press(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.active != viewPlaylists || model.activePlaylist != 0 {
		t.Fatalf("active source = %v/%d, want playlists/0", model.active, model.activePlaylist)
	}
	if !model.deckFocused {
		t.Fatal("starting a playlist did not focus the deck")
	}

	// Selecting a library track switches the active source back and bumps the
	// revision counter used for the swap.
	before := model.rescanSeq
	model.browse = viewLibrary
	model.selected = 0
	model.startLibraryPlayback(0)
	if model.active != viewLibrary || model.activePlaylist != -1 {
		t.Fatalf("active source after library select = %v/%d", model.active, model.activePlaylist)
	}
	if model.rescanSeq != before+1 {
		t.Fatalf("revision counter = %d, want %d", model.rescanSeq, before+1)
	}
}

// TestAddSelectedFromLibraryView checks the natural flow: open a playlist, go
// back to the library, and add the highlighted track with the playlist-add key.
func TestAddSelectedFromLibraryView(t *testing.T) {
	tracks := []library.Track{{Path: "/m/a.flac", Title: "A"}, {Path: "/m/b.flac", Title: "B"}}
	model, dir := playlistModel(t, tracks)
	file := playlist.FileName(dir, "Mix")
	model.playlists = []playlist.Playlist{{Name: "Mix", File: file}}
	model.openPlaylistAt(0)
	model.browse = viewLibrary
	model.selected = 1 // B

	model = press(t, model, keyRunes("A"))

	loaded, err := playlist.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 1 || loaded.Paths[0] != "/m/b.flac" {
		t.Fatalf("paths = %v, want the highlighted library track", loaded.Paths)
	}
}

func TestPlaylistPanelFitsTerminalSize(t *testing.T) {
	model := NewModel(
		[]library.Track{{Path: "/m/a.flac", Title: "A"}},
		nil,
		config.Defaults(),
	).WithPlaylists(t.TempDir(), []playlist.Playlist{{
		Name: "Mix", File: "/tmp/Mix.m3u8", Paths: []string{"/m/a.flac", "/m/missing.flac"},
	}})
	model.browse = viewPlaylists
	model.openPlaylistAt(0)
	for _, size := range [][2]int{{72, 13}, {80, 24}, {110, 34}} {
		model.width, model.height = size[0], size[1]
		lines := strings.Split(model.View(), "\n")
		if len(lines) != size[1] {
			t.Errorf("playlist view at %dx%d has %d rows, want %d", size[0], size[1], len(lines), size[1])
		}
		for row, line := range lines {
			if width := lipgloss.Width(line); width != size[0] {
				t.Errorf("playlist view at %dx%d row %d is %d wide, want %d", size[0], size[1], row, width, size[0])
			}
		}
	}
}

// press feeds one key message through the model and returns the result.
func press(t *testing.T, model Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := model.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("update returned %T, want Model", updated)
	}
	return result
}

// TestStartingPlaylistAdoptsPendingRescan covers the race where a rescan's
// ack has not arrived yet: starting a playlist must adopt the new library and
// keep the playlist as the active source, not let the ack rewind it.
func TestStartingPlaylistAdoptsPendingRescan(t *testing.T) {
	model := NewModel([]library.Track{{Path: "/m/old.flac", Title: "Old"}}, &player.Engine{}, config.Defaults())
	model.scanGen = 1
	model = press(t, model, rescanResultMsg{gen: 1, paths: []string{"/m/new.flac"}})
	if model.pendingTracks == nil {
		t.Fatal("rescan result did not stage a pending swap")
	}
	model.playlists = []playlist.Playlist{{Name: "Mix", File: "/tmp/mix.m3u8", Paths: []string{"/m/new.flac"}}}
	model.openPlaylistAt(0)

	model.startPlaylistPlayback(model.playlistTracks, 0, 0)

	if model.pendingTracks != nil {
		t.Fatal("starting a playlist left the pending swap staged")
	}
	if len(model.tracks) != 1 || model.tracks[0].Path != "/m/new.flac" {
		t.Fatalf("pending library was not adopted: %+v", model.tracks)
	}
	if model.active != viewPlaylists {
		t.Fatalf("active source = %v, want playlists", model.active)
	}
}

func TestHelpOverlayListsRuntimeAndPlaylistKeys(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.width, model.height = 120, 40
	view := strings.ToLower(ansi.Strip(model.helpOverlay(model.palette())))
	for _, want := range []string{"crossfade", "replaygain", "playlist"} {
		if !strings.Contains(view, want) {
			t.Errorf("help overlay does not mention %q", want)
		}
	}
}
