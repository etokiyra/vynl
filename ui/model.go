package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/etokiyra/vynl/config"
	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/etokiyra/vynl/playlist"
)

type tickMsg time.Time
type statusMsg player.Status
type trackDetailsMsg trackDetails

// tagBatchMsg carries one batch of incremental tag metadata; tagScanDoneMsg is
// emitted once when the scan stream closes. Both carry the scan generation so a
// batch from a superseded scan (e.g. one already queued when a rescan starts)
// can be dropped instead of being applied to the new track list.
type tagBatchMsg struct {
	gen     int
	updates []library.TagUpdate
}
type tagScanDoneMsg struct{ gen int }

// rescanResultMsg is the outcome of the off-thread directory walk. gen is the
// walk generation; the walk can be cancelled (its context), and a superseded
// walk's result is dropped by generation.
type rescanResultMsg struct {
	gen   int
	paths []string
	err   error
}

// trackSort selects how the library list is ordered.
type trackSort int

const (
	sortByPath trackSort = iota
	sortByTitle
	sortByArtist
	sortByAlbum
)

func (s trackSort) label() string {
	switch s {
	case sortByTitle:
		return "TITLE"
	case sortByArtist:
		return "ARTIST"
	case sortByAlbum:
		return "ALBUM"
	default:
		return "PATH"
	}
}

func (s trackSort) next() trackSort {
	return (s + 1) % 4
}

type trackDetails struct {
	path    string
	bytes   int64
	readErr string
}

// viewMode selects which list the browser panel shows. The engine's active list
// is tracked separately (activeSource) because the user can browse the library
// while a playlist plays, or vice versa.
type viewMode int

const (
	viewLibrary viewMode = iota
	viewPlaylists
)

// promptKind selects the modal text prompt used to name playlists.
type promptKind int

const (
	promptNone promptKind = iota
	promptNewPlaylist
	promptRenamePlaylist
)

type Model struct {
	tracks         []library.Track
	visible        []int
	engine         *player.Engine
	config         config.Config
	status         player.Status
	statusAt       time.Time
	loaded         trackDetails
	width          int
	height         int
	frame          int
	wheelPhase     float64
	rmsLevel       float64
	rmsTarget      float64
	channelLevel   [2]float64
	channelTarget  [2]float64
	bassLevel      float64
	bassTarget     float64
	spectrum       [player.SpectrumBands]float64
	spectrumTarget [player.SpectrumBands]float64
	spectrumHold   [player.SpectrumBands]float64
	selected       int
	eqBand         int
	search         string
	searching      bool
	deckFocused    bool
	showHelp       bool
	sortKey        trackSort

	// Playlists. playlistDir is where they are stored; playlists holds the
	// loaded set; browse selects which list the browser shows; openPlaylist is
	// the index of the opened playlist (-1 when showing the list); active is
	// which list the engine is playing (activePlaylist is meaningful only when
	// active is viewPlaylists). playlistTracks/playlistMissing are the resolved
	// entries and their availability.
	playlistDir      string
	playlists        []playlist.Playlist
	browse           viewMode
	openPlaylist     int
	playlistSelected int
	listSelected     int
	playlistTracks   []library.Track
	playlistMissing  []bool
	active           viewMode
	activePlaylist   int
	prompt           promptKind
	promptText       string
	confirmDelete    int

	// tagUpdates is the asynchronous tag scan stream, if one is running, and
	// scanCancel cancels its context (covering both the walk and the tag
	// read). scanGen is the generation stamped on messages from that stream so
	// a superseded scan's late batch is ignored. All of this lives only on the
	// Bubble Tea event loop.
	tagUpdates  <-chan []library.TagUpdate
	scanCancel  context.CancelFunc
	scanGen     int
	tagScanning bool
	tagScanned  int

	// trackRevision is the engine track-list revision that m.tracks is known to
	// match. A rescan's replacement is held in pending* and only adopted (with
	// tag streaming restarted) once a Status echoes pendingRevision, so the UI
	// never trusts indices against a list the engine has not applied. rescanSeq
	// is a monotonic counter (never reset, even if a SetTracks is dropped) so a
	// stale ack from a superseded swap can never match a later pending one.
	// notice carries a one-line failure message (e.g. a rescan that found
	// nothing).
	trackRevision   int
	rescanSeq       int
	pendingTracks   []library.Track
	pendingPaths    []string
	pendingRevision int
	rescanning      bool
	notice          string
}

func NewModel(tracks []library.Track, engine *player.Engine, cfg config.Config) Model {
	m := Model{
		tracks:         tracks,
		engine:         engine,
		config:         cfg,
		openPlaylist:   -1,
		activePlaylist: -1,
		confirmDelete:  -1,
	}
	m.refreshVisible()
	return m
}

// WithPlaylists attaches the playlist directory and the playlists loaded from
// it. It is a separate method so callers that do not use playlists (and most
// tests) keep using NewModel unchanged.
func (m Model) WithPlaylists(dir string, lists []playlist.Playlist) Model {
	m.playlistDir = dir
	m.playlists = lists
	m.openPlaylist = -1
	m.activePlaylist = -1
	m.confirmDelete = -1
	return m
}

// WithTagUpdates attaches the initial incremental tag stream and the cancel for
// its context. The model cancels that context when the user starts a rescan and
// again on exit (see CancelScan). It is a separate method (not a NewModel
// parameter) so existing callers are unaffected.
func (m Model) WithTagUpdates(updates <-chan []library.TagUpdate, cancel context.CancelFunc) Model {
	m.tagUpdates = updates
	m.scanCancel = cancel
	m.scanGen = 1
	m.tagScanning = updates != nil
	return m
}

func (m Model) Init() tea.Cmd {
	commands := make([]tea.Cmd, 0, 2)
	if m.engine != nil {
		commands = append(commands, waitForStatus(m.engine.Updates()))
	}
	if m.tagUpdates != nil {
		commands = append(commands, waitForTags(m.scanGen, m.tagUpdates))
	}
	return tea.Batch(commands...)
}

// CancelScan cancels the current walk/tag scan context, if any. main calls it
// after the TUI exits so the latest rescan's walk and tag reader stop; the
// Bubble Tea command goroutines then return promptly and their late messages
// are discarded (Send respects the program context).
func (m Model) CancelScan() {
	if m.scanCancel != nil {
		m.scanCancel()
	}
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		if m.status.Playing {
			m.frame++
			m.wheelPhase += maxFloat(0.25, m.status.Speed)
		}
		m.rmsLevel += (m.rmsTarget - m.rmsLevel) * 0.32
		for channel := range m.channelLevel {
			m.channelLevel[channel] += (m.channelTarget[channel] - m.channelLevel[channel]) * 0.32
		}
		m.bassLevel += (m.bassTarget - m.bassLevel) * 0.32
		for band := range m.spectrum {
			rate := 0.45
			if m.spectrumTarget[band] < m.spectrum[band] {
				rate = 0.18 // decay more slowly than the rise for a natural falloff
			}
			m.spectrum[band] += (m.spectrumTarget[band] - m.spectrum[band]) * rate
			switch {
			case m.spectrum[band] >= m.spectrumHold[band]:
				m.spectrumHold[band] = m.spectrum[band]
			case m.spectrumHold[band] > 0:
				m.spectrumHold[band] = math.Max(m.spectrum[band], m.spectrumHold[band]-0.015)
			}
		}
		if m.status.Playing || m.hasPendingEase() {
			return m, tick()
		}
	case statusMsg:
		wasPlaying := m.status.Playing
		m.status = player.Status(msg)
		m.statusAt = time.Now()
		commands := []tea.Cmd{waitForStatus(m.engine.Updates())}
		// The engine echoes the track-list revision it has applied. Only when it
		// matches our pending swap do we adopt the new list, so indices are
		// never used against a list the engine has not switched to.
		if m.pendingTracks != nil && m.status.Revision == m.pendingRevision {
			commands = append(commands, m.commitRescan()...)
		}
		m.rmsTarget = 0
		m.spectrumTarget = [player.SpectrumBands]float64{}
		if m.status.Playing {
			m.rmsTarget = m.status.RMS
			m.channelTarget = m.status.ChannelRMS
			m.bassTarget = m.status.BassRMS
			m.spectrumTarget = m.status.Spectrum
		} else {
			m.channelTarget = [2]float64{}
			m.bassTarget = 0
		}
		track := m.currentTrack()
		if m.loaded.path != track.Path {
			// Reserve the path so repeated status updates do not queue duplicate
			// reads, and load the header off the update loop: decoding a file on
			// a slow or network-mounted library must not stall rendering.
			m.loaded = trackDetails{path: track.Path}
			commands = append(commands,
				loadTrackDetails(track.Path),
				tea.SetWindowTitle(windowTitle(track)))
		}
		if !wasPlaying && m.status.Playing {
			commands = append(commands, tick())
		}
		return m, tea.Batch(commands...)
	case trackDetailsMsg:
		if details := trackDetails(msg); details.path == m.currentTrack().Path {
			m.loaded = details
		}
	case tagBatchMsg:
		if msg.gen != m.scanGen {
			// A batch from a superseded scan that was already queued when the
			// rescan bumped the generation. Drop it and do not re-arm the old
			// channel.
			break
		}
		m.applyTagUpdates(msg.updates)
		if m.tagUpdates != nil {
			return m, waitForTags(m.scanGen, m.tagUpdates)
		}
	case tagScanDoneMsg:
		if msg.gen != m.scanGen {
			break
		}
		// The stream is exhausted; clear the indicator regardless of the count
		// so a late or missing final batch can never look stuck.
		m.tagScanning = false
		m.tagUpdates = nil
	case rescanResultMsg:
		if msg.gen != m.scanGen {
			break
		}
		m.rescanning = false
		switch {
		case msg.err != nil:
			m.notice = "Rescan failed: " + library.SanitizeText(msg.err.Error())
		case len(msg.paths) == 0:
			m.notice = "Rescan found no supported tracks; keeping the current library"
		default:
			m.pendingTracks = library.FallbackTracks(msg.paths)
			m.pendingPaths = msg.paths
			m.rescanSeq++
			m.pendingRevision = m.rescanSeq
			if m.engine != nil {
				m.engine.Send(player.Command{
					Action: player.SetTracks, Tracks: m.pendingTracks, Revision: m.pendingRevision,
				})
			}
		}
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// Ctrl+C always quits, even mid-search or with the help overlay open, so it
	// is a universal escape hatch and its exit path persists state.
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.prompt != promptNone {
		return m.updatePrompt(key, msg)
	}
	if m.confirmDelete >= 0 {
		return m.updateConfirmDelete(key)
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	// Boundary/page navigation works in the library list (and the search box);
	// the playlist browser keeps its own cursor.
	if m.browse == viewLibrary && m.handleNavigation(key) {
		return m, nil
	}
	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			m.search = ""
			m.refreshVisible()
			return m, nil
		case "enter":
			var command tea.Cmd
			if len(m.visible) > 0 {
				command = m.startLibraryPlayback(m.visible[m.selected])
			}
			m.searching = false
			m.deckFocused = true
			return m, command
		case "up":
			m.moveSelection(-1)
			return m, nil
		case "down":
			m.moveSelection(1)
			return m, nil
		case "backspace":
			if len(m.search) > 0 {
				m.search = m.search[:len(m.search)-1]
				m.refreshVisible()
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.search += library.StripControl(string(msg.Runes))
				m.refreshVisible()
			}
			return m, nil
		}
	}

	// The playlist browser owns Up/Down/Enter/Esc and the playlist actions;
	// other keys fall through to the transport controls so playback can still
	// be driven while browsing playlists.
	if m.browse == viewPlaylists {
		if handled, cmd := m.updatePlaylistKey(key); handled {
			return m, cmd
		}
	}

	keys := m.config.Keybindings
	steps := m.config.Steps
	switch {
	case key == keys.Quit:
		return m, tea.Quit
	case key == "?":
		m.showHelp = true
	case key == "tab":
		m.deckFocused = !m.deckFocused
	case key == keys.Playlists:
		m.toggleBrowse()
	case key == keys.Search:
		m.browse = viewLibrary
		m.searching = true
		m.search = ""
		m.selected = 0
		m.refreshVisible()
	case key == keys.Toggle:
		m.engine.Send(player.Command{Action: player.Toggle})
	case key == keys.Stop:
		m.engine.Send(player.Command{Action: player.Stop})
	case key == keys.Next:
		m.engine.Send(player.Command{Action: player.Next})
	case key == keys.Prev:
		m.engine.Send(player.Command{Action: player.Prev})
	case key == keys.SeekBack:
		m.engine.Send(player.Command{Action: player.Seek, Value: -steps.Seek})
	case key == keys.SeekForward:
		m.engine.Send(player.Command{Action: player.Seek, Value: steps.Seek})
	case key == keys.Restart:
		m.engine.Send(player.Command{Action: player.Restart})
	case key == keys.VolumeUp:
		if m.deckFocused {
			m.engine.Send(player.Command{Action: player.Volume, Value: steps.Volume})
		} else {
			m.moveSelection(-1)
		}
	case key == keys.VolumeDown:
		if m.deckFocused {
			m.engine.Send(player.Command{Action: player.Volume, Value: -steps.Volume})
		} else {
			m.moveSelection(1)
		}
	case key == keys.Mute:
		m.engine.Send(player.Command{Action: player.Mute})
	case key == keys.SpeedDown:
		m.engine.Send(player.Command{Action: player.Speed, Value: -steps.Tempo})
	case key == keys.SpeedUp:
		m.engine.Send(player.Command{Action: player.Speed, Value: steps.Tempo})
	case key == keys.PitchDown:
		m.engine.Send(player.Command{Action: player.Pitch, Value: -steps.Pitch})
	case key == keys.PitchUp:
		m.engine.Send(player.Command{Action: player.Pitch, Value: steps.Pitch})
	case key == keys.Reset:
		m.engine.Send(player.Command{Action: player.Reset})
	case key == keys.Vinyl:
		m.engine.Send(player.Command{Action: player.Vinyl})
	case key == keys.Shuffle:
		m.engine.Send(player.Command{Action: player.Shuffle})
	case key == keys.Repeat:
		m.engine.Send(player.Command{Action: player.Repeat})
	case key == keys.Sort:
		m.sortKey = m.sortKey.next()
		m.refreshVisible()
	case key == keys.Rescan:
		return m, m.startRescan()
	case key == keys.NowPlaying:
		m.selectPlaying()
	case key == keys.EQLow:
		m.eqBand = 0
	case key == keys.EQMid:
		m.eqBand = 1
	case key == keys.EQHigh:
		m.eqBand = 2
	case key == keys.EQGainDown:
		m.engine.Send(player.Command{Action: player.EQ, Band: m.eqBand, Value: -steps.EQ})
	case key == keys.EQGainUp, key == "=":
		m.engine.Send(player.Command{Action: player.EQ, Band: m.eqBand, Value: steps.EQ})
	case key == keys.CrossfadeToggle:
		m.engine.Send(player.Command{Action: player.CrossfadeToggle})
	case key == keys.CrossfadeDown:
		m.engine.Send(player.Command{Action: player.Crossfade, Value: -steps.Crossfade})
	case key == keys.CrossfadeUp:
		m.engine.Send(player.Command{Action: player.Crossfade, Value: steps.Crossfade})
	case key == keys.ReplayGainCycle:
		m.engine.Send(player.Command{Action: player.ReplayGain})
	case key == keys.PreampDown:
		m.engine.Send(player.Command{Action: player.Preamp, Value: -steps.Preamp})
	case key == keys.PreampUp:
		m.engine.Send(player.Command{Action: player.Preamp, Value: steps.Preamp})
	case key == keys.PlaylistAdd:
		m.addPlayingToPlaylist()
	case key == keys.PlaylistAddSelected:
		m.addSelectedToPlaylist()
	case key == keys.PlaylistAddAll:
		m.addFilteredToPlaylist()
	case key == "up":
		m.moveSelection(-1)
	case key == "down":
		m.moveSelection(1)
	case key == "enter":
		if len(m.visible) > 0 {
			return m, m.startLibraryPlayback(m.visible[m.selected])
		}
	}
	return m, nil
}

// toggleBrowse switches the browser between the library and the playlists. It
// never changes what the engine is playing; the playlist panel is a separate
// view of the same player.
func (m *Model) toggleBrowse() {
	if m.browse == viewLibrary {
		m.browse = viewPlaylists
	} else {
		m.browse = viewLibrary
	}
}

// updatePlaylistKey handles the keys that belong to the playlist browser. It
// reports whether the key was consumed; unhandled keys fall through to the
// transport controls so playback can still be driven from playlist view.
func (m *Model) updatePlaylistKey(key string) (bool, tea.Cmd) {
	keys := m.config.Keybindings
	switch {
	case key == "up" && !m.deckFocused:
		if m.openPlaylist >= 0 {
			m.movePlaylistSelection(-1)
		} else {
			m.moveListSelection(-1)
		}
		return true, nil
	case key == "down" && !m.deckFocused:
		if m.openPlaylist >= 0 {
			m.movePlaylistSelection(1)
		} else {
			m.moveListSelection(1)
		}
		return true, nil
	case key == "home":
		m.browseToEdge(false)
		return true, nil
	case key == "end":
		m.browseToEdge(true)
		return true, nil
	case key == "enter":
		if m.openPlaylist >= 0 {
			if len(m.playlistTracks) > 0 {
				return true, m.startPlaylistPlayback(m.playlistTracks, m.openPlaylist, m.playlistSelected)
			}
		} else if m.listSelected >= 0 && m.listSelected < len(m.playlists) {
			m.openPlaylistAt(m.listSelected)
		}
		return true, nil
	case key == "esc":
		if m.openPlaylist >= 0 {
			m.closePlaylist()
		} else {
			m.browse = viewLibrary
		}
		return true, nil
	case key == keys.PlaylistNew:
		m.openPrompt(promptNewPlaylist)
		return true, nil
	case key == keys.PlaylistRename:
		if m.openPlaylist >= 0 || len(m.playlists) > 0 {
			m.openPrompt(promptRenamePlaylist)
		} else {
			m.notice = "No playlist to rename"
		}
		return true, nil
	case key == keys.PlaylistDelete:
		if m.openPlaylist >= 0 {
			m.removePlaylistEntry(m.playlistSelected)
		} else if m.listSelected >= 0 && m.listSelected < len(m.playlists) {
			m.confirmDelete = m.listSelected
		}
		return true, nil
	case key == keys.PlaylistAdd:
		m.addPlayingToPlaylist()
		return true, nil
	case key == keys.PlaylistAddSelected:
		m.addSelectedToPlaylist()
		return true, nil
	case key == keys.PlaylistAddAll:
		m.addFilteredToPlaylist()
		return true, nil
	case key == keys.PlaylistMoveUp:
		m.movePlaylistEntry(-1)
		return true, nil
	case key == keys.PlaylistMoveDown:
		m.movePlaylistEntry(1)
		return true, nil
	}
	return false, nil
}

func (m *Model) browseToEdge(last bool) {
	if m.openPlaylist >= 0 {
		if len(m.playlistTracks) == 0 {
			return
		}
		if last {
			m.playlistSelected = len(m.playlistTracks) - 1
		} else {
			m.playlistSelected = 0
		}
		return
	}
	if len(m.playlists) == 0 {
		return
	}
	if last {
		m.listSelected = len(m.playlists) - 1
	} else {
		m.listSelected = 0
	}
}

func (m *Model) moveListSelection(delta int) {
	if len(m.playlists) == 0 {
		return
	}
	m.listSelected = (m.listSelected + delta + len(m.playlists)) % len(m.playlists)
}

func (m *Model) movePlaylistSelection(delta int) {
	if len(m.playlistTracks) == 0 {
		return
	}
	m.playlistSelected = (m.playlistSelected + delta + len(m.playlistTracks)) % len(m.playlistTracks)
}

// openPlaylistAt opens the playlist at index in the browser (it does not start
// playback) and resolves its entries.
func (m *Model) openPlaylistAt(index int) {
	if index < 0 || index >= len(m.playlists) {
		return
	}
	m.openPlaylist = index
	m.playlistSelected = 0
	m.resolvePlaylistTracks()
}

func (m *Model) closePlaylist() {
	m.openPlaylist = -1
	m.playlistTracks = nil
	m.playlistMissing = nil
	m.playlistSelected = 0
}

// resolvePlaylistTracks rebuilds the open playlist's displayed tracks, matching
// each path against the library for fresh metadata and checking availability
// without discarding entries that cannot currently be resolved.
func (m *Model) resolvePlaylistTracks() {
	m.playlistTracks = nil
	m.playlistMissing = nil
	if m.openPlaylist < 0 || m.openPlaylist >= len(m.playlists) {
		return
	}
	paths := m.playlists[m.openPlaylist].Paths
	m.playlistTracks = make([]library.Track, len(paths))
	m.playlistMissing = make([]bool, len(paths))
	for i, path := range paths {
		track := library.Track{
			Path:  path,
			Title: library.SanitizeText(strings.TrimSuffix(filepathBase(path), filepath.Ext(path))),
		}
		if index := m.indexOfPath(path); index >= 0 {
			track = m.tracks[index]
		}
		m.playlistTracks[i] = track
		m.playlistMissing[i] = !playlist.IsPlayable(path)
	}
	if m.playlistSelected >= len(m.playlistTracks) {
		m.playlistSelected = max(0, len(m.playlistTracks)-1)
	}
}

// resolveTrack prefers the library's current metadata for a track path (tags
// stream in after a playlist is opened), falling back to the given copy.
func (m Model) resolveTrack(track library.Track) library.Track {
	if index := m.indexOfPath(track.Path); index >= 0 {
		return m.tracks[index]
	}
	return track
}

func (m Model) currentPlaylist() *playlist.Playlist {
	if m.openPlaylist >= 0 && m.openPlaylist < len(m.playlists) {
		return &m.playlists[m.openPlaylist]
	}
	return nil
}

// planPlayback returns the commands that start playing entry from tracks. When
// the engine is not already on this source it prepends a revisioned SetTracks so
// the engine's list (and therefore Next/Prev/gapless order) matches. It is pure
// so tests can assert the exact command sequence.
func planPlayback(needsSwap bool, revision int, tracks []library.Track, entry int) []player.Command {
	commands := make([]player.Command, 0, 2)
	if needsSwap {
		commands = append(commands, player.Command{Action: player.SetTracks, Tracks: tracks, Revision: revision})
	}
	commands = append(commands, player.Command{Action: player.Select, Value: float64(entry)})
	return commands
}

// startLibraryPlayback starts (or moves to) a track in the library. The engine
// only needs a full SetTracks when it is not already playing the library, which
// avoids re-shuffling the order on every selection. If a rescan is still
// awaiting its ack, it is adopted first and the track is re-found by path so a
// replaced list cannot select the wrong entry.
func (m *Model) startLibraryPlayback(index int) tea.Cmd {
	if m.engine == nil || index < 0 || index >= len(m.tracks) {
		return nil
	}
	path := m.tracks[index].Path
	pending := m.adoptPendingLibrary()
	if resolved := m.indexOfPath(path); resolved >= 0 {
		index = resolved
	}
	needsSwap := m.active != viewLibrary
	if needsSwap {
		m.rescanSeq++
	}
	for _, command := range planPlayback(needsSwap, m.rescanSeq, m.tracks, index) {
		m.engine.Send(command)
	}
	m.active = viewLibrary
	m.activePlaylist = -1
	m.deckFocused = true
	return tea.Batch(pending...)
}

// startPlaylistPlayback replaces the engine's active list with the playlist and
// selects the chosen entry, so Next/Prev and gapless/crossfade follow the
// playlist order.
func (m *Model) startPlaylistPlayback(tracks []library.Track, playlistIndex, entry int) tea.Cmd {
	if m.engine == nil || entry < 0 || entry >= len(tracks) {
		return nil
	}
	pending := m.adoptPendingLibrary()
	m.rescanSeq++
	for _, command := range planPlayback(true, m.rescanSeq, tracks, entry) {
		m.engine.Send(command)
	}
	m.active = viewPlaylists
	m.activePlaylist = playlistIndex
	m.deckFocused = true
	return tea.Batch(pending...)
}

// --- playlist editing -------------------------------------------------------

func (m *Model) openPrompt(kind promptKind) {
	m.prompt = kind
	m.promptText = ""
	if kind == promptRenamePlaylist {
		if current := m.currentPlaylist(); current != nil {
			m.promptText = current.Name
		} else if m.listSelected >= 0 && m.listSelected < len(m.playlists) {
			m.promptText = m.playlists[m.listSelected].Name
		}
	}
}

func (m Model) updatePrompt(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.prompt = promptNone
		m.promptText = ""
	case "enter":
		m.commitPrompt()
	case "backspace":
		if runes := []rune(m.promptText); len(runes) > 0 {
			m.promptText = string(runes[:len(runes)-1])
		}
	default:
		if len(msg.Runes) > 0 {
			m.promptText += library.StripControl(string(msg.Runes))
		}
	}
	return m, nil
}

func (m *Model) commitPrompt() {
	kind := m.prompt
	text := m.promptText
	m.prompt = promptNone
	m.promptText = ""
	name, ok := playlist.SanitizeName(text)
	if !ok {
		m.notice = "Playlist name cannot be empty"
		return
	}
	switch kind {
	case promptNewPlaylist:
		m.createPlaylist(name)
	case promptRenamePlaylist:
		m.renameCurrentPlaylist(name)
	}
}

func (m *Model) createPlaylist(name string) {
	if m.playlistDir == "" {
		m.notice = "Playlists are unavailable"
		return
	}
	for _, existing := range m.playlists {
		if existing.Name == name {
			m.notice = "A playlist named " + name + " already exists"
			return
		}
	}
	file := playlist.FileName(m.playlistDir, name)
	if err := playlist.Save(playlist.Playlist{Name: name, File: file}); err != nil {
		m.notice = "Could not create playlist: " + library.SanitizeText(err.Error())
		return
	}
	m.playlists = append(m.playlists, playlist.Playlist{Name: name, File: file})
	sortPlaylists(m.playlists)
	m.openPlaylistAt(indexOfPlaylist(m.playlists, file))
	m.notice = "Created playlist " + name
}

// renameCurrentPlaylist renames the open playlist (or the highlighted one in the
// list) by writing the new file and removing the old one. The audio files are
// never touched.
func (m *Model) renameCurrentPlaylist(name string) {
	if m.playlistDir == "" {
		m.notice = "Playlists are unavailable"
		return
	}
	index := m.openPlaylist
	wasOpen := index >= 0
	if !wasOpen {
		index = m.listSelected
	}
	if index < 0 || index >= len(m.playlists) {
		return
	}
	old := m.playlists[index]
	if old.Name == name {
		return
	}
	newFile := playlist.FileName(m.playlistDir, name)
	for i, existing := range m.playlists {
		if i != index && existing.Name == name {
			m.notice = "A playlist named " + name + " already exists"
			return
		}
	}
	if err := playlist.Save(playlist.Playlist{Name: name, File: newFile, Paths: old.Paths}); err != nil {
		m.notice = "Could not rename playlist: " + library.SanitizeText(err.Error())
		return
	}
	_ = playlist.Delete(old.File)
	m.playlists[index] = playlist.Playlist{Name: name, File: newFile, Paths: old.Paths}
	sortPlaylists(m.playlists)
	position := indexOfPlaylist(m.playlists, newFile)
	if wasOpen {
		m.openPlaylist = position
		m.resolvePlaylistTracks()
	} else {
		m.listSelected = max(0, position)
	}
	m.notice = "Renamed playlist to " + name
}

// deletePlaylist removes a playlist file and updates the cursors and the
// engine's active-playlist marker. It never deletes music files.
func (m *Model) deletePlaylist(index int) {
	if index < 0 || index >= len(m.playlists) {
		return
	}
	removed := m.playlists[index]
	if err := playlist.Delete(removed.File); err != nil {
		m.notice = "Could not delete playlist: " + library.SanitizeText(err.Error())
		return
	}
	m.playlists = append(m.playlists[:index], m.playlists[index+1:]...)
	if m.active == viewPlaylists {
		switch {
		case m.activePlaylist == index:
			m.activePlaylist = -1
		case m.activePlaylist > index:
			m.activePlaylist--
		}
	}
	switch {
	case m.openPlaylist == index:
		m.closePlaylist()
	case m.openPlaylist > index:
		m.openPlaylist--
	}
	if m.listSelected >= len(m.playlists) {
		m.listSelected = max(0, len(m.playlists)-1)
	}
	m.notice = "Deleted playlist " + removed.Name
}

func (m Model) updateConfirmDelete(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y", "enter":
		m.deletePlaylist(m.confirmDelete)
		m.confirmDelete = -1
	case "esc", "n", "N":
		m.confirmDelete = -1
	}
	return m, nil
}

func (m *Model) addPlayingToPlaylist() {
	m.addPathsToPlaylist([]string{m.status.Track.Path})
}

func (m *Model) addSelectedToPlaylist() {
	m.addPathsToPlaylist([]string{m.currentSelectedPath()})
}

func (m *Model) addFilteredToPlaylist() {
	paths := make([]string, 0, len(m.visible))
	for _, index := range m.visible {
		if index >= 0 && index < len(m.tracks) {
			paths = append(paths, m.tracks[index].Path)
		}
	}
	m.addPathsToPlaylist(paths)
}

// addPathsToPlaylist appends resolved paths to the open playlist and saves it.
// Empty paths are ignored; the playlist size is capped so a save can always be
// read back.
func (m *Model) addPathsToPlaylist(paths []string) {
	current := m.currentPlaylist()
	if current == nil {
		m.notice = "Open a playlist first (" + keyLabel(m.config.Keybindings.Playlists) + " then ENTER)"
		return
	}
	added := 0
	for _, path := range paths {
		if path == "" {
			continue
		}
		if len(current.Paths) >= playlist.MaxEntries {
			m.notice = "Playlist is full"
			break
		}
		current.Paths = append(current.Paths, path)
		added++
	}
	if added == 0 {
		m.notice = "Nothing to add"
		return
	}
	if err := playlist.Save(*current); err != nil {
		m.notice = "Could not save playlist: " + library.SanitizeText(err.Error())
		return
	}
	m.resolvePlaylistTracks()
	m.playlistSelected = len(m.playlistTracks) - 1
	m.notice = fmt.Sprintf("Added %d track(s) to %s", added, current.Name)
}

func (m *Model) movePlaylistEntry(delta int) {
	current := m.currentPlaylist()
	if current == nil {
		return
	}
	target := m.playlistSelected + delta
	if target < 0 || target >= len(m.playlistTracks) {
		return
	}
	current.Paths[m.playlistSelected], current.Paths[target] = current.Paths[target], current.Paths[m.playlistSelected]
	m.playlistTracks[m.playlistSelected], m.playlistTracks[target] = m.playlistTracks[target], m.playlistTracks[m.playlistSelected]
	m.playlistMissing[m.playlistSelected], m.playlistMissing[target] = m.playlistMissing[target], m.playlistMissing[m.playlistSelected]
	m.playlistSelected = target
	if err := playlist.Save(*current); err != nil {
		m.notice = "Could not save playlist: " + library.SanitizeText(err.Error())
	}
}

func (m *Model) removePlaylistEntry(index int) {
	current := m.currentPlaylist()
	if current == nil || index < 0 || index >= len(current.Paths) {
		return
	}
	current.Paths = append(current.Paths[:index], current.Paths[index+1:]...)
	if err := playlist.Save(*current); err != nil {
		m.notice = "Could not save playlist: " + library.SanitizeText(err.Error())
		return
	}
	m.resolvePlaylistTracks()
	if m.playlistSelected >= len(m.playlistTracks) {
		m.playlistSelected = max(0, len(m.playlistTracks)-1)
	}
	m.notice = "Removed entry"
}

func sortPlaylists(lists []playlist.Playlist) {
	sort.Slice(lists, func(i, j int) bool { return lists[i].Name < lists[j].Name })
}

func indexOfPlaylist(lists []playlist.Playlist, file string) int {
	for i, list := range lists {
		if list.File == file {
			return i
		}
	}
	return -1
}

// browserPanel renders whichever list the user is browsing.
func (m Model) browserPanel(width, height int, p palette) string {
	if m.browse == viewPlaylists {
		return m.playlistPanel(width, height, p)
	}
	return m.libraryPanel(width, height, p)
}

func (m Model) playlistPanel(width, height int, p palette) string {
	inner := max(1, width-6)
	contentHeight := max(0, height-3)
	var lines []string
	switch {
	case m.prompt == promptNewPlaylist:
		lines = append(lines,
			p.magenta.Bold(true).Render("NEW PLAYLIST"),
			p.text.Render(truncate("NAME: "+m.promptText+"_", inner, "")),
			p.muted.Render("ENTER create   ESC cancel"),
		)
	case m.prompt == promptRenamePlaylist:
		lines = append(lines,
			p.magenta.Bold(true).Render("RENAME PLAYLIST"),
			p.text.Render(truncate("NAME: "+m.promptText+"_", inner, "")),
			p.muted.Render("ENTER rename   ESC cancel"),
		)
	case m.confirmDelete >= 0:
		name := ""
		if m.confirmDelete < len(m.playlists) {
			name = m.playlists[m.confirmDelete].Name
		}
		lines = append(lines,
			p.error.Bold(true).Render("DELETE PLAYLIST"),
			p.text.Render(truncate("Delete "+name+"?", inner, "…")),
			p.muted.Render("Y confirm   ESC cancel"),
		)
	case m.openPlaylist < 0:
		lines = m.playlistListLines(inner, p)
	default:
		lines = m.playlistEntryLines(inner, contentHeight, p)
	}
	if m.notice != "" {
		lines = append(lines, p.error.Render(truncate(m.notice, inner, "…")))
	}
	return panel("PLAYLISTS", strings.Join(lines, "\n"), width, height, p.magenta, p.background)
}

func (m Model) playlistListLines(inner int, p palette) []string {
	keys := m.config.Keybindings
	lines := []string{
		p.muted.Render(fmt.Sprintf("%d PLAYLISTS", len(m.playlists))),
		p.muted.Render("↑↓ browse   ENTER open   " + keyLabel(keys.PlaylistNew) + " new   " +
			keyLabel(keys.PlaylistRename) + " rename   " + keyLabel(keys.PlaylistDelete) + " delete"),
	}
	if len(m.playlists) == 0 {
		lines = append(lines, p.muted.Render("No playlists yet — press "+keyLabel(keys.PlaylistNew)+" to create one"))
		return lines
	}
	for i, list := range m.playlists {
		marker := "  "
		if m.active == viewPlaylists && m.activePlaylist == i {
			marker = "▶ "
		}
		label := fmt.Sprintf("%s%s  (%d)", marker, list.Name, len(list.Paths))
		switch {
		case i == m.listSelected:
			lines = append(lines, p.cyan.Bold(true).Render(truncate(label, inner, "…")))
		case marker == "▶ ":
			lines = append(lines, p.green.Render(truncate(label, inner, "…")))
		default:
			lines = append(lines, p.text.Render(truncate(label, inner, "…")))
		}
	}
	return lines
}

func (m Model) playlistEntryLines(inner, contentHeight int, p palette) []string {
	keys := m.config.Keybindings
	name := ""
	if m.openPlaylist < len(m.playlists) {
		name = m.playlists[m.openPlaylist].Name
	}
	missing := 0
	for _, unavailable := range m.playlistMissing {
		if unavailable {
			missing++
		}
	}
	header := fmt.Sprintf("%s  %d entries", name, len(m.playlistTracks))
	if missing > 0 {
		header += fmt.Sprintf("  %d unavailable", missing)
	}
	lines := []string{
		p.text.Bold(true).Render(truncate(header, inner, "…")),
		p.muted.Render("↑↓ browse   ENTER play   " + keyLabel(keys.PlaylistAdd) + " add playing   " +
			keyLabel(keys.PlaylistAddSelected) + " add selected   " + keyLabel(keys.PlaylistDelete) + " remove"),
		p.muted.Render(keyLabel(keys.PlaylistMoveUp) + "/" + keyLabel(keys.PlaylistMoveDown) + " reorder   " +
			keyLabel(keys.PlaylistRename) + " rename   ESC back"),
	}
	if len(m.playlistTracks) == 0 {
		lines = append(lines, p.muted.Render("Empty — add tracks from the library with "+keyLabel(keys.PlaylistAddSelected)))
		return lines
	}
	rows := max(1, contentHeight-len(lines))
	start, end := 0, min(len(m.playlistTracks), rows)
	if len(m.playlistTracks) > end {
		start = max(0, min(m.playlistSelected-rows/2, len(m.playlistTracks)-rows))
		end = start + rows
	}
	for i := start; i < end; i++ {
		track := m.resolveTrack(m.playlistTracks[i])
		title := track.Title
		if title == "" {
			title = filepathBase(track.Path)
		}
		marker := "  "
		if m.playlistMissing[i] {
			marker = "! "
		}
		if m.status.Track.Path != "" && track.Path == m.status.Track.Path {
			marker = "▶ "
		}
		label := marker + title
		switch {
		case i == m.playlistSelected:
			lines = append(lines, p.cyan.Bold(true).Render(truncate(label, inner-2, "…")))
		case m.playlistMissing[i]:
			lines = append(lines, p.error.Render(truncate(label, inner-2, "…")))
		case marker == "▶ ":
			lines = append(lines, p.green.Render(truncate(label, inner-2, "…")))
		default:
			lines = append(lines, p.text.Render(truncate(label, inner-2, "…")))
		}
	}
	return lines
}

// startRescan cancels any in-flight walk/tag scan and kicks off a fresh
// directory walk as a Bubble Tea command, so a large tree cannot freeze the
// update loop. Its result is gated by scanGen, so pressing rescan again (or
// quitting) supersedes rather than interleaves: the previous walk's context is
// cancelled and any late result is dropped.
func (m *Model) startRescan() tea.Cmd {
	if m.scanCancel != nil {
		m.scanCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.scanCancel = cancel
	m.scanGen++
	m.rescanning = true
	m.tagScanning = false
	m.notice = ""
	root := m.config.MusicDir
	gen := m.scanGen
	return rescanWalk(ctx, gen, root)
}

// commitRescan adopts the pending track list, cancels the previous scan, starts
// a fresh tag scan for the new list, and returns the command that reads it.
// Callers must have first observed a Status whose Revision equals
// pendingRevision, which is what guarantees the engine and UI lists agree.
// commitRescan adopts the pending track list after the engine has echoed its
// revision. The engine's list is now the new library, so library playback is the
// active source.
func (m *Model) commitRescan() []tea.Cmd {
	m.active = viewLibrary
	m.activePlaylist = -1
	return m.adoptPendingLibrary()
}

// adoptPendingLibrary installs the pending rescan's library into the UI and
// restarts tag streaming. It does not change which source the engine is playing:
// callers decide that (a normal commit is library playback; starting a playlist
// while a rescan is in flight would otherwise be undone by the ack).
func (m *Model) adoptPendingLibrary() []tea.Cmd {
	if m.pendingTracks == nil {
		return nil
	}
	if m.scanCancel != nil {
		m.scanCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.scanCancel = cancel
	m.scanGen++

	selectedPath := m.currentSelectedPath()
	m.tracks = m.pendingTracks
	m.trackRevision = m.pendingRevision
	if m.openPlaylist >= 0 {
		m.resolvePlaylistTracks()
	}
	paths := m.pendingPaths
	m.pendingTracks = nil
	m.pendingPaths = nil
	m.pendingRevision = 0
	m.rescanning = false
	m.notice = ""
	m.tagScanned = 0
	m.refreshVisibleKeeping(selectedPath)

	updates := library.ReadTags(ctx, paths, runtime.NumCPU())
	m.tagUpdates = updates
	m.tagScanning = updates != nil
	return []tea.Cmd{waitForTags(m.scanGen, updates)}
}

// rescanWalk performs the metadata-only directory walk off the update loop. The
// context lets a superseding rescan (or exit) abort a slow walk.
func rescanWalk(ctx context.Context, gen int, root string) tea.Cmd {
	return func() tea.Msg {
		paths, err := library.ScanPathsContext(ctx, root)
		return rescanResultMsg{gen: gen, paths: paths, err: err}
	}
}

func (m Model) View() string {
	if m.width < 1 || m.height < 1 {
		return ""
	}
	palette := m.palette()
	if m.showHelp && m.width >= 24 && m.height >= 12 {
		return m.helpOverlay(palette)
	}
	if m.width < 24 || m.height < 12 {
		return compactRoot(m, palette)
	}
	innerWidth, innerHeight := m.width-2, m.height-2
	headerHeight, footerHeight, eqHeight := 2, 1, 4
	if innerHeight < 12 {
		headerHeight, eqHeight = 1, 3
	}
	bodyHeight := innerHeight - headerHeight - footerHeight - eqHeight
	if bodyHeight < 6 {
		return compactRoot(m, palette)
	}
	deckWidth, browserWidth := innerWidth, innerWidth
	deckHeight, browserHeight := bodyHeight, bodyHeight
	var body string
	if innerWidth >= 72 && bodyHeight >= 8 {
		deckWidth = (innerWidth - 2) * 57 / 100
		browserWidth = innerWidth - deckWidth - 2
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			m.deckPanel(deckWidth, bodyHeight, palette),
			lipgloss.NewStyle().Width(2).Height(bodyHeight).Render(""),
			m.browserPanel(browserWidth, bodyHeight, palette),
		)
	} else {
		deckHeight = max(5, bodyHeight*58/100)
		browserHeight = bodyHeight - deckHeight
		if browserHeight < 3 {
			browserHeight = 3
			deckHeight = bodyHeight - browserHeight
		}
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.deckPanel(deckWidth, deckHeight, palette),
			m.browserPanel(browserWidth, browserHeight, palette),
		)
	}
	live := "·"
	if m.status.Playing {
		liveFrames := []string{"·", "•", "*", "•"}
		live = liveFrames[(m.frame/2)%len(liveFrames)]
	}
	header := palette.cyan.Bold(true).Render("VYNL") + palette.muted.Render("  /  ANALOG AUDIO CONSOLE") + palette.green.Render("  "+live+" LIVE")
	header = fixedBlock(header, innerWidth, headerHeight, palette.background)
	eq := m.eqPanel(innerWidth, eqHeight, palette)
	help := m.helpLine(palette)
	help = fixedBlock(help, innerWidth, footerHeight, palette.background)
	inner := lipgloss.JoinVertical(lipgloss.Left, header, body, eq, help)
	inner = fixedBlock(inner, innerWidth, innerHeight, palette.background)
	return lipgloss.NewStyle().Foreground(palette.foregroundColor).
		Width(innerWidth).Height(innerHeight).Border(lipgloss.RoundedBorder()).BorderForeground(palette.cyan.GetForeground()).
		Render(inner)
}

func (m Model) helpLine(p palette) string {
	if m.searching {
		return p.magenta.Render("SEARCH  ") + p.text.Render(m.search+"_") + p.muted.Render("   ↑↓ pick   ENTER load   ESC cancel")
	}
	keys := m.config.Keybindings
	if m.deckFocused {
		parts := []string{
			keyLabel(keys.Toggle) + " play",
			keyLabel(keys.Next) + "/" + keyLabel(keys.Prev) + " track",
			keyLabel(keys.SeekBack) + "/" + keyLabel(keys.SeekForward) + " seek",
			keyLabel(keys.VolumeDown) + "/" + keyLabel(keys.VolumeUp) + " vol",
			keyLabel(keys.Vinyl) + " vinyl",
			keyLabel(keys.Mute) + " mute",
			keyLabel(keys.Shuffle) + " shuffle",
			keyLabel(keys.Repeat) + " repeat",
			keyLabel(keys.CrossfadeToggle) + " xfade",
			keyLabel(keys.ReplayGainCycle) + " rg",
			keyLabel(keys.Rescan) + " rescan",
			"TAB library",
			"? help",
			keyLabel(keys.Quit) + " quit",
		}
		return p.muted.Render(strings.Join(parts, "  "))
	}
	return p.muted.Render("↑/↓ browse   ENTER load   " + keyLabel(keys.Search) +
		" find   " + keyLabel(keys.Sort) + " sort   " + keyLabel(keys.Playlists) + " playlists   " +
		keyLabel(keys.Rescan) + " rescan   " +
		keyLabel(keys.Toggle) + " play   " +
		keyLabel(keys.Next) + "/" + keyLabel(keys.Prev) + " track   " +
		keyLabel(keys.NowPlaying) + " now   TAB deck   ? help   " + keyLabel(keys.Quit) + " quit")
}

// keyLabel renders a configured key in a form that reads well in the help bar.
func keyLabel(key string) string {
	switch key {
	case " ":
		return "SPACE"
	case "left":
		return "←"
	case "right":
		return "→"
	case "up":
		return "↑"
	case "down":
		return "↓"
	}
	if len([]rune(key)) == 1 {
		return strings.ToUpper(key)
	}
	return key
}

type helpEntry struct {
	key    string
	action string
}

// helpEntries lists every binding the help overlay documents.
func (m Model) helpEntries() []helpEntry {
	keys := m.config.Keybindings
	return []helpEntry{
		{keyLabel(keys.Toggle), "Play / pause"},
		{keyLabel(keys.Stop), "Stop and rewind"},
		{keyLabel(keys.Next), "Next track"},
		{keyLabel(keys.Prev), "Previous track"},
		{keyLabel(keys.SeekBack), "Seek back 5s"},
		{keyLabel(keys.SeekForward), "Seek forward 5s"},
		{keyLabel(keys.Restart), "Restart current track"},
		{keyLabel(keys.VolumeDown), "Volume down"},
		{keyLabel(keys.VolumeUp), "Volume up"},
		{keyLabel(keys.Mute), "Mute / unmute"},
		{keyLabel(keys.SpeedDown), "Tempo down"},
		{keyLabel(keys.SpeedUp), "Tempo up"},
		{keyLabel(keys.PitchDown), "Pitch down"},
		{keyLabel(keys.PitchUp), "Pitch up"},
		{keyLabel(keys.Reset), "Reset transport"},
		{keyLabel(keys.Vinyl), "Toggle vinyl mode"},
		{keyLabel(keys.Shuffle), "Toggle shuffle"},
		{keyLabel(keys.Repeat), "Cycle repeat off/all/one"},
		{keyLabel(keys.Sort), "Cycle library sort order"},
		{keyLabel(keys.Rescan), "Rescan library"},
		{keyLabel(keys.NowPlaying), "Jump to playing track"},
		{keyLabel(keys.Top) + " / " + keyLabel(keys.Bottom), "First / last track"},
		{keyLabel(keys.PageUp) + " / " + keyLabel(keys.PageDown), "Page up / down"},
		{keyLabel(keys.EQLow) + " " + keyLabel(keys.EQMid) + " " + keyLabel(keys.EQHigh), "Select EQ band"},
		{keyLabel(keys.EQGainUp) + " " + keyLabel(keys.EQGainDown), "Adjust selected EQ band"},
		{keyLabel(keys.CrossfadeToggle) + " " + keyLabel(keys.CrossfadeDown) + " " + keyLabel(keys.CrossfadeUp), "Crossfade on/off and duration"},
		{keyLabel(keys.ReplayGainCycle), "Cycle ReplayGain off/track/album"},
		{keyLabel(keys.PreampDown) + " " + keyLabel(keys.PreampUp), "ReplayGain preamp down/up"},
		{keyLabel(keys.Playlists), "Switch library / playlists"},
		{keyLabel(keys.PlaylistNew) + " " + keyLabel(keys.PlaylistRename) + " " + keyLabel(keys.PlaylistDelete), "New / rename / delete playlist"},
		{keyLabel(keys.PlaylistAdd) + " " + keyLabel(keys.PlaylistAddSelected) + " " + keyLabel(keys.PlaylistAddAll), "Add playing / selected / all"},
		{keyLabel(keys.PlaylistMoveUp) + " " + keyLabel(keys.PlaylistMoveDown), "Reorder playlist entries"},
		{"TAB", "Switch focus"},
		{keyLabel(keys.Search), "Search library"},
		{keyLabel(keys.Quit), "Quit"},
		{"?", "Close this help"},
	}
}

// helpOverlay renders a full-screen, multi-column keybinding reference that
// fills exactly m.width x m.height.
func (m Model) helpOverlay(p palette) string {
	innerWidth, innerHeight := max(1, m.width-2), max(1, m.height-2)
	entries := m.helpEntries()
	keyWidth, actionWidth := 0, 0
	for _, entry := range entries {
		keyWidth = max(keyWidth, lipgloss.Width(entry.key))
		actionWidth = max(actionWidth, lipgloss.Width(entry.action))
	}
	const gap = 3
	entryWidth := keyWidth + 2 + actionWidth
	columns := max(1, min(3, (innerWidth+gap)/(entryWidth+gap)))
	rows := max(1, (len(entries)+columns-1)/columns)

	rendered := make([][]string, columns)
	for index, entry := range entries {
		column := min(index/rows, columns-1)
		line := p.cyan.Bold(true).Render(padRight(entry.key, keyWidth)) + "  " +
			p.text.Render(padRight(entry.action, actionWidth))
		rendered[column] = append(rendered[column], line)
	}
	grid := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		parts := make([]string, 0, columns)
		for column := 0; column < columns; column++ {
			if row < len(rendered[column]) {
				parts = append(parts, rendered[column][row])
			}
		}
		grid = append(grid, strings.Join(parts, strings.Repeat(" ", gap)))
	}
	title := p.cyan.Bold(true).Render("KEYBINDINGS") + p.muted.Render("    press any key to close")
	body := title + "\n\n" + strings.Join(grid, "\n")
	inner := fixedBlock(body, innerWidth, innerHeight, p.background)
	return p.background.Width(innerWidth).Height(innerHeight).
		Border(lipgloss.RoundedBorder()).BorderForeground(p.cyan.GetForeground()).Render(inner)
}

func padRight(text string, width int) string {
	if padding := width - lipgloss.Width(text); padding > 0 {
		return text + strings.Repeat(" ", padding)
	}
	return text
}

func (m Model) deckPanel(width, height int, p palette) string {
	inner := max(1, width-6)
	title, artist := "No track loaded", "Scan a folder containing MP3, FLAC, WAV, or OGG files"
	if track := m.currentTrack(); track.Path != "" {
		title = track.Title
		artist = track.Artist
		if artist == "" {
			artist = track.Album
		}
		if artist == "" {
			artist = filepathBase(track.Path)
		}
	}
	showVinyl := inner >= 52 && height >= 20
	vinylWidth := min(31, max(25, inner/3))
	infoWidth := inner
	if showVinyl {
		infoWidth = max(1, inner-vinylWidth-2)
	}
	position := m.playbackPosition()
	remaining := max(0, m.status.Duration-position)
	timeLine := fmt.Sprintf("%s  /  -%s", clock(position), clock(remaining))
	if m.status.Duration > 0 {
		timeLine = fmt.Sprintf("%s  /  %s  /  -%s", clock(position), clock(m.status.Duration), clock(remaining))
	}
	status := "PAUSED"
	if m.status.Loading {
		status = "LOADING"
	} else if m.status.Buffering {
		status = "BUFFERING"
	} else if m.status.Playing {
		status = "PLAYING"
	}
	if m.status.Vinyl {
		status += " / VINYL"
	}
	info := []string{
		p.green.Bold(true).Render(truncate(status, infoWidth, "")),
		p.text.Bold(true).Render(truncate(title, infoWidth, "~")),
		p.muted.Render(truncate(artist, infoWidth, "~")),
		p.muted.Render(fmt.Sprintf("TRACK %02d / %02d", m.status.Index+1, max(1, m.status.Count))),
	}
	if next := m.nextTrackTitle(); next != "" {
		info = append(info, p.muted.Render(truncate("NEXT  "+next, infoWidth, "~")))
	}
	info = append(info,
		p.cyan.Render(progressBar(position, m.status.Duration, infoWidth, m.frame, m.status.Playing)),
		p.muted.Render(timeLine),
	)
	top := info
	if showVinyl {
		art := vinylArt(vinylWidth, m.wheelPhase, p, m.pulseStyle())
		for len(info) < len(art) {
			info = append(info, "")
		}
		top = make([]string, len(art))
		for i := range art {
			top[i] = art[i] + "  " + info[i]
		}
	}
	volumeBar := fmt.Sprintf("VOL %s %02.0f%%", slider(m.status.Volume, max(3, min(10, inner-15))), m.status.Volume*100)
	shortVolume := fmt.Sprintf("VOL %02.0f%%", m.status.Volume*100)
	if m.status.Muted {
		volumeBar = "VOL MUTED"
		shortVolume = "VOL MUTED"
	}
	controls := []string{
		fmt.Sprintf("SPD %s %.2fx", slider((m.status.Speed-0.5)/1.5, max(3, min(10, inner-16))), m.status.Speed),
		fmt.Sprintf("PITCH %s %+.0fst", slider((m.status.Pitch+12)/24, max(3, min(10, inner-18))), m.status.Pitch),
		volumeBar,
	}
	vu := vuInline(m.visualChannels(), m.metersActive(), p)
	controlText := strings.Join(controls, "   ") + "   " + vu
	if lipgloss.Width(controlText) > inner {
		controlText = strings.Join([]string{fmt.Sprintf("SPD %.2fx", m.status.Speed), fmt.Sprintf("PITCH %+.0fst", m.status.Pitch), shortVolume, vu}, "   ")
	}
	contentHeight := max(0, height-3)
	baseLines := append([]string(nil), top...)
	marker := p.muted.Render("[ PAUSED ]")
	if m.status.Playing {
		icons := []string{">>>", "> >", ">>>", " > "}
		marker = p.green.Bold(true).Render("[ " + icons[(m.frame/2)%len(icons)] + " PLAYING ]")
	}
	modes := []string{"VINYL " + boolLabel(m.status.Vinyl)}
	if m.status.Shuffle {
		modes = append(modes, "SHUFFLE")
	}
	if m.status.Repeat != player.RepeatOff {
		modes = append(modes, "REPEAT "+strings.ToUpper(string(m.status.Repeat)))
	}
	if m.status.ReplayGain != "" && m.status.ReplayGain != "off" {
		rg := "RG " + strings.ToUpper(m.status.ReplayGain)
		if m.status.TrackGainDB != 0 {
			rg += fmt.Sprintf(" %+.1fdB", m.status.TrackGainDB)
		}
		modes = append(modes, rg)
	}
	if ms := m.status.CrossfadeMS; ms > 0 {
		modes = append(modes, fmt.Sprintf("XFADE %.1fs", float64(ms)/1000))
	}
	baseLines = append(baseLines, marker+"  "+p.muted.Render(strings.Join(modes, "  ")))
	baseLines = append(baseLines, p.text.Render(truncate(controlText, inner, "~")))
	if m.status.Err != "" {
		baseLines = append(baseLines, p.error.Render(truncate(library.SanitizeText(m.status.Err), inner, "…")))
	} else if m.status.Skipped > 0 {
		baseLines = append(baseLines, p.muted.Render(truncate(
			fmt.Sprintf("SKIPPED %d UNPLAYABLE", m.status.Skipped), inner, "…")))
	}
	visualizerHeight := max(0, contentHeight-len(baseLines))
	needle := -1
	if m.status.Playing && m.status.Duration > 0 {
		needle = min(min(60, inner)-1, int(position/m.status.Duration*float64(min(60, inner))))
	}
	visualizer := spectrumRows(m.spectrum, m.spectrumHold, min(60, inner), visualizerHeight, needle, p)
	baseLines = append(baseLines, visualizer...)
	if !m.deckFocused && len(baseLines) > 0 {
		baseLines[0] += p.muted.Render("  TRACK FOCUS")
	}
	return panel("OUTPUT / TRANSPORT", strings.Join(baseLines, "\n"), width, height, m.pulseStyle(), p.background)
}

func (m Model) libraryPanel(width, height int, p palette) string {
	inner := max(1, width-6)
	contentHeight := max(0, height-3)
	entryHeight := 1
	if inner > 30 {
		entryHeight = 2
	}
	metaLines := m.loaded.displayLines(m.status.Duration, m.status.SampleRate, p)
	reserved := 2 + 1 + len(metaLines)
	// Use all the vertical space the panel affords (reserving a few rows for
	// the metadata block and stereo meter) instead of a fixed track count.
	maxTracks := max(0, (contentHeight-reserved-3)/entryHeight)
	start, end := 0, min(len(m.visible), maxTracks)
	if len(m.visible) > end {
		start = max(0, min(m.selected-maxTracks/2, len(m.visible)-maxTracks))
		end = start + maxTracks
	}
	header := fmt.Sprintf("%d/%d TRACKS  %s", len(m.visible), len(m.tracks), m.sortKey.label())
	if m.rescanning {
		header += "  RESCANNING"
	}
	if m.tagScanning {
		header += fmt.Sprintf("  SCANNING %d/%d", min(m.tagScanned, len(m.tracks)), len(m.tracks))
	}
	if m.notice != "" {
		header += "  " + m.notice
	}
	if query := strings.TrimSpace(m.search); query != "" {
		header += "  FILTER: " + query
	}
	lines := []string{p.muted.Render(header)}
	if m.searching {
		lines = append(lines, p.magenta.Render("FIND: "+m.search+"_"))
	} else {
		lines = append(lines, p.muted.Render("↑ ↓ browse   ENTER load   / find"))
	}
	for index := start; index < end; index++ {
		trackIndex := m.visible[index]
		track := m.tracks[trackIndex]
		name := track.Title
		artist := track.Artist
		if artist == "" {
			artist = track.Album
		}
		if artist == "" {
			artist = filepathBase(track.Path)
		}
		marker := "  "
		if m.status.Track.Path != "" && track.Path == m.status.Track.Path {
			marker = "▶ "
		}
		switch {
		case index == m.selected:
			lines = append(lines, p.cyan.Bold(true).Render(marker+truncate(name, inner-2, "…")))
		case marker == "▶ ":
			lines = append(lines, p.green.Render(marker+truncate(name, inner-2, "…")))
		default:
			lines = append(lines, p.text.Render(marker+truncate(name, inner-2, "…")))
		}
		if inner > 28 {
			lines = append(lines, p.muted.Render("  "+truncate(artist, inner-2, "…")))
		}
	}
	if len(m.visible) == 0 {
		lines = append(lines, p.muted.Render("No matching tracks"))
	}
	lines = append(lines, p.magenta.Bold(true).Render("NOW LOADED"))
	lines = append(lines, metaLines...)
	meterRows := max(0, contentHeight-len(lines))
	lines = append(lines, stereoMeterLines(meterRows, m.visualChannels(), m.metersActive(), p)...)
	return panel("LIBRARY / BROWSER", strings.Join(lines, "\n"), width, height, p.magenta, p.background)
}

func stereoMeterLines(height int, rms [2]float64, playing bool, p palette) []string {
	if height <= 0 {
		return nil
	}
	lines := make([]string, height)
	active := [2]int{}
	if playing {
		for channel := range active {
			active[channel] = int(math.Round(math.Min(1, rms[channel]*2) * float64(max(0, height-1))))
		}
	}
	lines[0] = p.muted.Render("L       R")
	for row := 1; row < height; row++ {
		glyphs := [2]string{"  ", "  "}
		for channel := range glyphs {
			if height-row <= active[channel] {
				glyphs[channel] = p.pink.Render("█")
			}
		}
		lines[row] = "  " + glyphs[0] + "      " + glyphs[1]
	}
	return lines
}

func (m Model) eqPanel(width, height int, p palette) string {
	eqNames := []string{"LOW", "MID", "HIGH"}
	values := make([]string, 0, len(eqNames))
	for i, name := range eqNames {
		label := p.muted.Render(" " + name + " ")
		if i == m.eqBand {
			label = p.magenta.Bold(true).Render("[" + name + "]")
		}
		values = append(values, label+" "+p.green.Render(slider(m.status.EQ[i]/2, 8)))
	}
	keys := m.config.Keybindings
	hint := fmt.Sprintf("BAND %s/%s/%s  %s/%s GAIN",
		keyLabel(keys.EQLow), keyLabel(keys.EQMid), keyLabel(keys.EQHigh),
		keyLabel(keys.EQGainUp), keyLabel(keys.EQGainDown))
	return panel("EQUALIZER", strings.Join(values, "   ")+"   "+p.muted.Render(hint), width, height, p.green, p.background)
}

func (m *Model) moveSelection(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.selected = (m.selected + delta + len(m.visible)) % len(m.visible)
}

// moveSelectionTo clamps the cursor to a position in the filtered list.
func (m *Model) moveSelectionTo(position int) {
	if len(m.visible) == 0 {
		return
	}
	m.selected = max(0, min(len(m.visible)-1, position))
}

// libraryPageSize approximates how many tracks the library panel shows, used by
// PgUp/PgDn. Home/End jump exactly, so an approximate page is enough.
func (m Model) libraryPageSize() int {
	return max(1, m.height/3)
}

// handleNavigation applies Home/End/PgUp/PgDn to the library cursor. It reports
// whether the key was a navigation binding so callers can stop processing it.
func (m *Model) handleNavigation(key string) bool {
	keys := m.config.Keybindings
	switch key {
	case keys.Top:
		m.moveSelectionTo(0)
	case keys.Bottom:
		m.moveSelectionTo(len(m.visible) - 1)
	case keys.PageUp:
		m.moveSelectionTo(m.selected - m.libraryPageSize())
	case keys.PageDown:
		m.moveSelectionTo(m.selected + m.libraryPageSize())
	default:
		return false
	}
	return true
}

// selectPlaying moves the library cursor onto the currently loaded track when it
// is present in the filtered view, so a large (or auto-advancing) library can be
// located at a keystroke. It is a no-op when nothing is loaded or the track is
// filtered out.
func (m *Model) selectPlaying() {
	path := m.status.Track.Path
	if path == "" {
		return
	}
	for position, index := range m.visible {
		if index >= 0 && index < len(m.tracks) && m.tracks[index].Path == path {
			m.selected = position
			return
		}
	}
}

func (m *Model) refreshVisible() {
	m.refreshVisibleKeeping(m.currentSelectedPath())
}

// currentSelectedPath returns the path of the track under the cursor, or "" if
// there is none. Paths, not indices, are the stable identity across a rescan:
// the track list is replaced, so an old index can point at a different track.
func (m *Model) currentSelectedPath() string {
	if m.selected >= 0 && m.selected < len(m.visible) {
		if index := m.visible[m.selected]; index >= 0 && index < len(m.tracks) {
			return m.tracks[index].Path
		}
	}
	return ""
}

// refreshVisibleKeeping rebuilds the filtered/sorted view and restores the
// cursor to the track with the given path (clamping when it is gone). Tag
// updates and a rescan both reorder or replace the list, so the cursor is keyed
// by path rather than by a position that would go stale.
func (m *Model) refreshVisibleKeeping(selectedPath string) {
	query := strings.ToLower(strings.TrimSpace(m.search))

	type result struct{ index, score int }
	results := make([]result, 0, len(m.tracks))
	for i, track := range m.tracks {
		candidate := strings.ToLower(track.Title + " " + track.Artist + " " + track.Album + " " + track.Path)
		score, ok := fuzzyScore(query, candidate)
		if ok {
			results = append(results, result{i, score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score < results[j].score
		}
		return m.lessTrack(results[i].index, results[j].index)
	})
	m.visible = m.visible[:0]
	for _, result := range results {
		m.visible = append(m.visible, result.index)
	}

	// Keep the cursor on the same track where possible when the list reorders,
	// is filtered, or is replaced by a rescan.
	if selectedPath != "" {
		m.selected = 0
		for position, index := range m.visible {
			if m.tracks[index].Path == selectedPath {
				m.selected = position
				break
			}
		}
	}
	if m.selected >= len(m.visible) {
		m.selected = max(0, len(m.visible)-1)
	}
}

// applyTagUpdates replaces display metadata in place. It refreshes the visible
// list at most once per batch, and skips the O(n log n) rebuild entirely when
// the order cannot change (no search, sorted by path).
func (m *Model) applyTagUpdates(updates []library.TagUpdate) {
	identityOrder := m.search == "" && m.sortKey == sortByPath
	for _, update := range updates {
		if update.Index < 0 || update.Index >= len(m.tracks) {
			continue
		}
		m.tracks[update.Index] = update.Track
		m.tagScanned++
	}
	if identityOrder {
		return
	}
	m.refreshVisible()
}

// currentTrack prefers the UI's own track list, which carries incremental tag
// metadata, over the engine's fallback copy. It falls back to the status track
// when nothing is loaded, so the deck still shows "No track loaded" at startup.
// Paths are identical either way.
func (m Model) currentTrack() library.Track {
	if m.status.Track.Path == "" {
		return m.status.Track
	}
	// Resolve by path, not by the engine's index: the engine may be playing a
	// playlist (or a replaced library), so its index is not an index into
	// m.tracks. Falling back to the engine's own copy keeps the deck correct
	// for a track that is not in the library.
	if index := m.indexOfPath(m.status.Track.Path); index >= 0 {
		return m.tracks[index]
	}
	return m.status.Track
}

// lessTrack orders two tracks by the active sort key, falling back to the path
// so the ordering is total and deterministic.
func (m Model) lessTrack(first, second int) bool {
	a, b := m.tracks[first], m.tracks[second]
	firstKey, secondKey := m.sortText(a), m.sortText(b)
	if firstKey != secondKey {
		return firstKey < secondKey
	}
	return a.Path < b.Path
}

func (m Model) sortText(track library.Track) string {
	switch m.sortKey {
	case sortByTitle:
		if track.Title != "" {
			return strings.ToLower(track.Title)
		}
		return strings.ToLower(filepathBase(track.Path))
	case sortByArtist:
		if track.Artist != "" {
			return strings.ToLower(track.Artist)
		}
		return strings.ToLower(track.Album)
	case sortByAlbum:
		if track.Album != "" {
			return strings.ToLower(track.Album)
		}
		return strings.ToLower(track.Title)
	default:
		return track.Path
	}
}

func fuzzyScore(query, candidate string) (int, bool) {
	if query == "" {
		return 0, true
	}
	queryRunes, candidateRunes := []rune(query), []rune(candidate)
	queryIndex, score, gaps, previous := 0, 0, 0, -2
	for i, char := range candidateRunes {
		if char != queryRunes[queryIndex] {
			continue
		}
		if previous == i-1 {
			score--
		} else if previous >= 0 {
			gaps += i - previous - 1
		}
		score += i
		previous = i
		queryIndex++
		if queryIndex == len(queryRunes) {
			return score + gaps*3, true
		}
	}
	return 0, false
}

func (m Model) playbackPosition() float64 {
	position := m.status.Position
	if m.status.Playing && !m.status.Buffering && !m.statusAt.IsZero() {
		position += max(0, time.Since(m.statusAt).Seconds()) * max(0, m.status.Speed)
	}
	return math.Min(m.status.Duration, math.Max(0, position))
}

// nextTrackTitle returns the title of the track the engine will play next, or
// "" when there is nothing meaningful to show. It is driven by the status's
// NextTrack (path-keyed), not by a raw index, so a just-committed rescan cannot
// show the wrong upcoming track.
func (m Model) nextTrackTitle() string {
	if m.status.Count <= 1 {
		return ""
	}
	track := m.status.NextTrack
	if index := m.indexOfPath(track.Path); index >= 0 {
		track = m.tracks[index]
	}
	if track.Path == "" {
		return ""
	}
	if track.Title != "" {
		return track.Title
	}
	return filepathBase(track.Path)
}

// indexOfPath returns the position of a path in the UI's track list, or -1.
func (m Model) indexOfPath(path string) int {
	if path == "" {
		return -1
	}
	for index, track := range m.tracks {
		if track.Path == path {
			return index
		}
	}
	return -1
}

func (m Model) visualRMS() float64 {
	if m.rmsLevel == 0 && m.status.Playing {
		return m.status.RMS
	}
	return m.rmsLevel
}

func (m Model) visualChannels() [2]float64 {
	if m.channelLevel == [2]float64{} && m.status.Playing {
		return m.status.ChannelRMS
	}
	return m.channelLevel
}

func (m Model) hasPendingEase() bool {
	if math.Abs(m.rmsLevel-m.rmsTarget) > 0.008 || math.Abs(m.bassLevel-m.bassTarget) > 0.008 {
		return true
	}
	for channel := range m.channelLevel {
		if math.Abs(m.channelLevel[channel]-m.channelTarget[channel]) > 0.008 {
			return true
		}
	}
	for band := range m.spectrum {
		if math.Abs(m.spectrum[band]-m.spectrumTarget[band]) > 0.01 {
			return true
		}
	}
	for band := range m.spectrumHold {
		if m.spectrumHold[band] > 0.01 {
			return true
		}
	}
	return false
}

func (m Model) metersActive() bool {
	channels := m.visualChannels()
	return m.status.Playing || m.visualRMS() > 0.008 || channels[0] > 0.008 || channels[1] > 0.008 || m.bassLevel > 0.008
}

func panel(title, content string, width, height int, accent, base lipgloss.Style) string {
	innerWidth, innerHeight := max(1, width-4), max(1, height-2)
	text := accent.Bold(true).Render(title) + "\n" + content
	text = fitTerminal(text, innerWidth, innerHeight)
	return base.Width(innerWidth).Height(innerHeight).Padding(0, 1).Border(lipgloss.RoundedBorder()).
		BorderForeground(accent.GetForeground()).Render(text)
}

func vinylArt(width int, phase float64, p palette, accents ...lipgloss.Style) []string {
	templateWidth := max(21, min(31, width))
	if templateWidth%2 == 0 {
		templateWidth--
	}
	templateHeight := templateWidth / 2
	if templateHeight%2 == 0 {
		templateHeight++
	}
	canvas := make([][]rune, templateHeight)
	for row := range canvas {
		canvas[row] = []rune(strings.Repeat(" ", templateWidth))
	}
	centerCol, centerRow := (templateWidth-1)/2, (templateHeight-1)/2
	cx, cy := float64(centerCol), float64(centerRow)
	const aspectCorrection = 2.0
	maxRadius := math.Min(cx, cy*2)
	ringRadii := []float64{0.42 * maxRadius, 0.57 * maxRadius, 0.71 * maxRadius, 0.85 * maxRadius, maxRadius}
	labelLeft, labelRight := int(cx)-3, int(cx)+3
	labelTop, labelBottom := int(cy)-1, int(cy)+1
	for row := range canvas {
		for column := range canvas[row] {
			if column >= labelLeft && column <= labelRight && row >= labelTop && row <= labelBottom {
				continue
			}
			dx := float64(column - centerCol)
			dy := float64(row-centerRow) * aspectCorrection
			distance := math.Sqrt(dx*dx + dy*dy)
			for ringIndex, radius := range ringRadii {
				if math.Abs(distance-radius) <= 0.52 {
					canvas[row][column] = []rune("#O*+=")[ringIndex]
					break
				}
			}
		}
	}
	label := []rune(" VYNL ")
	copy(canvas[int(cy)][int(cx)-len(label)/2:], label)
	markerAngle := math.Mod(phase*math.Pi/4, 2*math.Pi)
	markerX := int(math.Round(cx + maxRadius*math.Cos(markerAngle)))
	markerY := int(math.Round(cy + maxRadius*math.Sin(markerAngle)/2))
	if markerY >= 0 && markerY < templateHeight && markerX >= 0 && markerX < templateWidth &&
		!(markerX >= labelLeft && markerX <= labelRight && markerY >= labelTop && markerY <= labelBottom) {
		canvas[markerY][markerX] = '◆'
	}
	rows := make([]string, templateHeight)
	outerAccent := p.cyan
	if len(accents) > 0 {
		outerAccent = accents[0]
	}
	for row := range canvas {
		line := string(canvas[row])
		if row == markerY {
			markerAt := strings.IndexRune(line, '◆')
			if markerAt >= 0 {
				glyphs := []rune(line)
				rows[row] = p.cyan.Render(string(glyphs[:markerAt])) + p.magenta.Bold(true).Render("◆") + p.cyan.Render(string(glyphs[markerAt+1:]))
				continue
			}
		}
		if row == int(cy) {
			labelAt := strings.Index(line, " VYNL ")
			if labelAt >= 0 {
				rows[row] = p.cyan.Render(line[:labelAt]) + p.magenta.Bold(true).Render(" VYNL ") + p.cyan.Render(line[labelAt+6:])
				continue
			}
		}
		var rendered strings.Builder
		for _, glyph := range line {
			style := p.cyan
			if glyph == '#' {
				style = outerAccent
			}
			rendered.WriteString(style.Render(string(glyph)))
		}
		rows[row] = rendered.String()
	}
	return rows
}

func spectrumRows(spectrum, hold [player.SpectrumBands]float64, width, height, needle int, p palette) []string {
	if height <= 0 || width <= 0 {
		return nil
	}
	width = min(60, width)
	levels := []rune("▁▂▃▄▅▆▇█")
	scale := float64(height * len(levels))
	peaks := make([]int, width)
	holds := make([]int, width)
	for column := range peaks {
		peaks[column] = max(0, min(height*len(levels)-1, int(math.Round(sampleSpectrum(spectrum, column, width)*scale))))
		holds[column] = max(0, min(height*len(levels)-1, int(math.Round(sampleSpectrum(hold, column, width)*scale))))
	}
	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		threshold := (height - row - 1) * len(levels)
		for column, peak := range peaks {
			remaining := peak - threshold
			holdRemaining := holds[column] - threshold
			glyph := ' '
			if remaining >= len(levels) {
				glyph = levels[len(levels)-1]
			} else if remaining > 0 {
				glyph = levels[remaining-1]
			}
			color := p.cyan
			if remaining >= len(levels)*2 {
				color = p.pink
			} else if remaining >= len(levels) {
				color = p.magenta
			}
			switch {
			case column == needle:
				line.WriteString(p.text.Bold(true).Render("┃"))
			case holdRemaining > 0 && holdRemaining <= len(levels) && remaining <= 0:
				line.WriteString(p.pink.Render("▔"))
			default:
				line.WriteString(color.Render(string(glyph)))
			}
		}
		rows[row] = line.String()
	}
	return rows
}

// sampleSpectrum maps a display column onto the band array with linear
// interpolation so the visualizer stays smooth at any width.
func sampleSpectrum(spectrum [player.SpectrumBands]float64, column, width int) float64 {
	if width <= 1 {
		return spectrum[0]
	}
	position := float64(column) * float64(player.SpectrumBands-1) / float64(width-1)
	low := int(position)
	if low >= player.SpectrumBands-1 {
		return spectrum[player.SpectrumBands-1]
	}
	fraction := position - float64(low)
	return spectrum[low]*(1-fraction) + spectrum[low+1]*fraction
}

func vuInline(rms [2]float64, playing bool, p palette) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	glyphs := [2]string{" ", " "}
	if playing {
		for channel := range glyphs {
			level := min(7, int(math.Round(math.Min(1, rms[channel]*2)*7)))
			if level > 0 {
				glyphs[channel] = p.pink.Render(string(blocks[level]))
			}
		}
	}
	return p.muted.Render("L") + glyphs[0] + p.muted.Render(" R") + glyphs[1]
}

func (m Model) pulseStyle() lipgloss.Style {
	amount := math.Max(0, math.Min(1, m.bassLevel*4))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(blendHex(styleHex(m.palette().cyan, "#45e6dc"), styleHex(m.palette().pink, "#ff77c8"), amount)))
}

func styleHex(style lipgloss.Style, fallback string) string {
	if color, ok := style.GetForeground().(lipgloss.Color); ok {
		return string(color)
	}
	return fallback
}

func blendHex(first, second string, amount float64) string {
	parse := func(value string) (uint64, uint64, uint64) {
		value = strings.TrimPrefix(value, "#")
		if len(value) != 6 {
			return 69, 230, 220
		}
		red, _ := strconv.ParseUint(value[:2], 16, 8)
		green, _ := strconv.ParseUint(value[2:4], 16, 8)
		blue, _ := strconv.ParseUint(value[4:], 16, 8)
		return red, green, blue
	}
	firstRed, firstGreen, firstBlue := parse(first)
	secondRed, secondGreen, secondBlue := parse(second)
	blend := func(a, b uint64) uint64 { return uint64(float64(a) + (float64(b)-float64(a))*amount) }
	return fmt.Sprintf("#%02x%02x%02x", blend(firstRed, secondRed), blend(firstGreen, secondGreen), blend(firstBlue, secondBlue))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func boolLabel(value bool) string {
	if value {
		return "ON"
	}
	return "OFF"
}

func fitTerminal(view string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
}

func fixedBlock(content string, width, height int, base lipgloss.Style) string {
	if width < 1 || height < 1 {
		return ""
	}
	return base.Width(width).Height(height).Render(fitTerminal(content, width, height))
}

func compactRoot(m Model, p palette) string {
	if m.width < 3 || m.height < 3 {
		return fixedBlock("VYNL", m.width, m.height, p.background)
	}
	innerWidth, innerHeight := m.width-2, m.height-2
	track := m.currentTrack().Title
	if track == "" {
		track = "No track loaded"
	}
	state := "PAUSED"
	if m.status.Loading {
		state = "LOADING"
	} else if m.status.Buffering {
		state = "BUFFERING"
	} else if m.status.Playing {
		state = "PLAYING"
	}
	if m.status.Muted {
		state += " / MUTED"
	}
	lines := []string{
		p.cyan.Bold(true).Render("VYNL") + p.muted.Render("  "+state),
		p.text.Render(truncate(track, innerWidth, "...")),
	}
	if innerHeight >= 3 {
		position := m.playbackPosition()
		lines = append(lines,
			p.cyan.Render(progressBar(position, m.status.Duration, innerWidth, m.frame, m.status.Playing)),
			p.muted.Render(fmt.Sprintf("%s  /  -%s", clock(position), clock(max(0, m.status.Duration-position)))),
		)
	}
	inner := fixedBlock(strings.Join(lines, "\n"), innerWidth, innerHeight, p.background)
	return p.background.Width(innerWidth).Height(innerHeight).Border(lipgloss.RoundedBorder()).
		BorderForeground(p.cyan.GetForeground()).Render(inner)
}

func truncate(text string, width int, suffix string) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	limit := max(0, width-lipgloss.Width(suffix))
	var result strings.Builder
	used := 0
	for _, char := range text {
		charWidth := lipgloss.Width(string(char))
		if used+charWidth > limit {
			break
		}
		result.WriteRune(char)
		used += charWidth
	}
	return result.String() + suffix
}

func slider(value float64, width int) string {
	value = math.Max(0, math.Min(1, value))
	position := int(math.Round(value * float64(width-1)))
	var bar strings.Builder
	bar.WriteByte('[')
	for i := 0; i < width; i++ {
		if i == position {
			bar.WriteRune('●')
		} else if i < position {
			bar.WriteRune('━')
		} else {
			bar.WriteRune('─')
		}
	}
	bar.WriteByte(']')
	return bar.String()
}

func progressBar(position, duration float64, width, frame int, playing bool) string {
	if width < 1 {
		return ""
	}
	ratio := 0.0
	if duration > 0 {
		ratio = math.Max(0, math.Min(1, position/duration))
	}
	filled := int(ratio * float64(width-1))
	var bar strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i == filled:
			if playing && frame%4 < 2 {
				bar.WriteRune('◇')
			} else {
				bar.WriteRune('◆')
			}
		case i < filled:
			bar.WriteRune('━')
		default:
			bar.WriteRune('─')
		}
	}
	return bar.String()
}

func clock(seconds float64) string {
	if seconds < 0 || math.IsNaN(seconds) {
		seconds = 0
	}
	duration := time.Duration(seconds * float64(time.Second))
	hours := int(duration / time.Hour)
	minutes := int(duration/time.Minute) % 60
	secondsPart := int(duration/time.Second) % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secondsPart)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secondsPart)
}

func filepathBase(path string) string {
	return filepath.Base(path)
}

// windowTitle builds the terminal title for the currently loaded track.
func windowTitle(track library.Track) string {
	if track.Path == "" {
		return "VYNL"
	}
	title := track.Title
	if title == "" {
		title = strings.TrimSuffix(filepathBase(track.Path), filepath.Ext(track.Path))
	}
	if track.Artist != "" {
		return "VYNL · " + title + " — " + track.Artist
	}
	return "VYNL · " + title
}

func loadTrackDetails(path string) tea.Cmd {
	return func() tea.Msg { return trackDetailsMsg(readTrackDetails(path)) }
}

func readTrackDetails(path string) trackDetails {
	details := trackDetails{path: path}
	if path == "" {
		return details
	}
	// Only stat the file: the sample rate and duration come from the engine's
	// status, so the UI never decodes audio (a redundant, potentially full-file
	// decode that would otherwise repeat the engine's work).
	if info, err := os.Stat(path); err != nil {
		details.readErr = err.Error()
	} else {
		details.bytes = info.Size()
	}
	return details
}

func (d trackDetails) displayLines(duration float64, sampleRate int, p palette) []string {
	if d.path == "" {
		return []string{p.muted.Render("No track loaded")}
	}
	format := strings.TrimPrefix(strings.ToUpper(filepath.Ext(d.path)), ".")
	bitrate := "--"
	if duration > 0 && d.bytes > 0 {
		bitrate = fmt.Sprintf("%d kb/s avg", int(float64(d.bytes)*8/duration/1000))
	}
	rate := "unknown"
	if sampleRate > 0 {
		rate = strconv.FormatFloat(float64(sampleRate)/1000, 'f', 1, 64) + " kHz"
	}
	return []string{
		p.text.Render(fmt.Sprintf("%s  /  %s", format, rate)),
		p.muted.Render(fmt.Sprintf("%s  /  %s", clock(duration), formatBytes(d.bytes))),
		p.muted.Render("AVG " + bitrate),
	}
}

func formatBytes(size int64) string {
	if size <= 0 {
		return "--"
	}
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + units[unit]
}

func (m Model) palette() palette {
	theme := m.config.Theme
	defaults := config.Defaults().Theme
	color := func(value, fallback string) lipgloss.Color {
		if value == "" {
			value = fallback
		}
		return lipgloss.Color(value)
	}
	foregroundColor := color(theme.Foreground, defaults.Foreground)
	return palette{
		background:      lipgloss.NewStyle().Foreground(foregroundColor),
		foregroundColor: foregroundColor,
		text:            lipgloss.NewStyle().Foreground(foregroundColor),
		muted:           lipgloss.NewStyle().Foreground(color(theme.Muted, defaults.Muted)),
		cyan:            lipgloss.NewStyle().Foreground(color(theme.Cyan, defaults.Cyan)),
		magenta:         lipgloss.NewStyle().Foreground(color(theme.Magenta, defaults.Magenta)),
		pink:            lipgloss.NewStyle().Foreground(color(theme.Pink, defaults.Pink)),
		green:           lipgloss.NewStyle().Foreground(color(theme.Green, defaults.Green)),
		error:           lipgloss.NewStyle().Foreground(color(theme.Error, defaults.Error)),
	}
}

type palette struct {
	background, text, muted, cyan, magenta, green, error lipgloss.Style
	pink                                                 lipgloss.Style
	foregroundColor                                      lipgloss.Color
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func waitForStatus(updates <-chan player.Status) tea.Cmd {
	return func() tea.Msg { return statusMsg(<-updates) }
}

func waitForTags(gen int, updates <-chan []library.TagUpdate) tea.Cmd {
	return func() tea.Msg {
		batch, ok := <-updates
		if !ok {
			return tagScanDoneMsg{gen: gen}
		}
		return tagBatchMsg{gen: gen, updates: batch}
	}
}
