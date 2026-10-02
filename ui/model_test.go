package ui

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/etokiyra/vynl/config"
	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
)

func TestFuzzySearchRanksCloseMatches(t *testing.T) {
	closeScore, closeOK := fuzzyScore("blue note", "blue note session")
	looseScore, looseOK := fuzzyScore("blue note", "the blue jazz recording note")
	if !closeOK || !looseOK || closeScore >= looseScore {
		t.Fatalf("scores: close=(%d,%t), loose=(%d,%t)", closeScore, closeOK, looseScore, looseOK)
	}
}

func TestModelFiltersTrackList(t *testing.T) {
	tracks := []library.Track{
		{Title: "Blue Train", Artist: "John Coltrane", Path: "blue-train.flac"},
		{Title: "Kind of Blue", Artist: "Miles Davis", Path: "kind-of-blue.mp3"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.search = "miles blue"
	model.refreshVisible()
	if len(model.visible) != 1 || model.tracks[model.visible[0]].Title != "Kind of Blue" {
		t.Fatalf("filtered tracks = %+v", model.visible)
	}
}

func TestViewFitsTerminalSize(t *testing.T) {
	tracks := []library.Track{{Title: "A deliberately long track title", Artist: "An artist with a long name", Path: "track.flac"}}
	for _, size := range [][2]int{{24, 12}, {36, 20}, {80, 24}, {110, 34}, {130, 40}} {
		model := NewModel(tracks, nil, config.Defaults())
		model.width, model.height = size[0], size[1]
		model.status = player.Status{Track: tracks[0], Count: 1, Playing: true, Speed: 1, Volume: 0.8}
		lines := strings.Split(model.View(), "\n")
		if len(lines) != size[1] {
			t.Errorf("view at %dx%d has %d rows, want exactly %d", size[0], size[1], len(lines), size[1])
		}
		for row, line := range lines {
			if width := lipgloss.Width(line); width != size[0] {
				t.Errorf("view at %dx%d row %d is %d cells wide, want exactly %d", size[0], size[1], row, width, size[0])
			}
		}
		if size[0] >= 110 && strings.Count(viewText(lines), "VYNL") < 2 {
			t.Errorf("record label missing at %dx%d", size[0], size[1])
		}
	}
}

func TestViewHandlesTinyTerminals(t *testing.T) {
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {10, 4}, {23, 11}, {24, 12}, {72, 12}, {300, 5}}
	for _, size := range sizes {
		model := NewModel([]library.Track{{Title: "Tiny", Path: "tiny.flac"}}, nil, config.Defaults())
		model.width, model.height = size[0], size[1]
		model.status = player.Status{
			Track: library.Track{Title: "Tiny", Path: "tiny.flac"},
			Count: 1, Playing: true, Speed: 1, Volume: 0.5, Duration: 10, Position: 3,
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("View panicked at %dx%d: %v", size[0], size[1], r)
				}
			}()
			_ = model.View()
		}()
	}
}

func TestWindowSizeMessageSetsInitialDimensions(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	if model.width != 0 || model.height != 0 {
		t.Fatalf("initial dimensions = %dx%d, want to wait for terminal size", model.width, model.height)
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 92, Height: 28})
	got := updated.(Model)
	if got.width != 92 || got.height != 28 {
		t.Fatalf("window dimensions = %dx%d, want 92x28", got.width, got.height)
	}
}

func TestVinylArtSpinsOnlyDuringPlayback(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	paused := strings.Join(vinylArt(31, 9, model.palette()), "\n")
	pausedLater := strings.Join(vinylArt(31, 9, model.palette()), "\n")
	playing := strings.Join(vinylArt(31, 10, model.palette()), "\n")
	if paused != pausedLater {
		t.Fatal("paused vinyl art should remain still")
	}
	if paused == playing {
		t.Fatal("playing vinyl art should rotate")
	}
	for _, width := range []int{25, 31} {
		rows := vinylArt(width, 0, model.palette())
		wantHeight := width / 2
		if wantHeight%2 == 0 {
			wantHeight++
		}
		if len(rows) != wantHeight {
			t.Fatalf("vinyl at width %d has height %d, want %d", width, len(rows), wantHeight)
		}
		for i, row := range rows {
			if lipgloss.Width(row) != width {
				t.Errorf("vinyl row %d width = %d, want %d", i, lipgloss.Width(row), width)
			}
		}
		plain := make([]string, len(rows))
		for i, row := range rows {
			plain[i] = ansi.Strip(row)
		}
		centerRow, centerCol := len(rows)/2, width/2
		label := plain[centerRow]
		if strings.Count(label, "VYNL") != 1 {
			t.Fatalf("center label is missing or garbled at width %d: %q", width, label)
		}
		for row := centerRow - 1; row <= centerRow+1; row++ {
			for col := centerCol - 3; col <= centerCol+3; col++ {
				if row == centerRow && col >= centerCol-2 && col <= centerCol+1 {
					continue
				}
				if glyph := []rune(plain[row])[col]; glyph != ' ' {
					t.Fatalf("groove touches reserved label patch at %dx%d (%d,%d): %q\n%s", width, len(rows), col, row, glyph, strings.Join(plain, "\n"))
				}
			}
		}
		maxRadius := math.Min(float64(width/2), float64((len(rows)-1)/2*2))
		ringRadii := []float64{0.42 * maxRadius, 0.57 * maxRadius, 0.71 * maxRadius, 0.85 * maxRadius, maxRadius}
		grooveCounts := make([]int, len(ringRadii))
		for row := range plain {
			for col, glyph := range []rune(plain[row]) {
				if glyph == ' ' || glyph == '◆' || strings.ContainsRune("VYNL", glyph) {
					continue
				}
				dx := float64(col) - float64(width-1)/2
				dy := (float64(row) - float64(len(rows)-1)/2) * 2
				distance := math.Sqrt(dx*dx + dy*dy)
				matched := false
				for ring, radius := range ringRadii {
					if math.Abs(distance-radius) <= 0.52 {
						grooveCounts[ring]++
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("non-ring glyph %q at (%d,%d), distance %.2f", glyph, col, row, distance)
				}
			}
		}
		for ring, count := range grooveCounts {
			if count == 0 {
				t.Errorf("ring %d at radius %.2f has no visible cells at width %d", ring, ringRadii[ring], width)
			}
		}
		if width == 31 {
			outerSpan := func(row int) int {
				minimum, maximum := width, -1
				for col, glyph := range []rune(plain[row]) {
					if glyph == '+' || glyph == '◆' {
						minimum = min(minimum, col)
						maximum = max(maximum, col)
					}
				}
				if maximum < minimum {
					return 0
				}
				return maximum - minimum + 1
			}
			centerSpan := outerSpan(centerRow)
			nearSpan := outerSpan(centerRow - 1)
			farSpan := outerSpan(centerRow - 4)
			if centerSpan < nearSpan || centerSpan-farSpan < 4 ||
				(centerSpan-nearSpan)*2 >= centerSpan-farSpan {
				t.Fatalf("outer ring does not follow a circular arc: center=%d near=%d far=%d\n%s", centerSpan, nearSpan, farSpan, strings.Join(plain, "\n"))
			}
			if outerSpan(centerRow-4) != outerSpan(centerRow+4) {
				t.Fatalf("outer ring is vertically asymmetric: upper=%d lower=%d", outerSpan(centerRow-4), outerSpan(centerRow+4))
			}
		}
		for _, phase := range []float64{0, 1, 2, 3} {
			markerRows := vinylArt(width, phase, model.palette())
			found := false
			for row, line := range markerRows {
				for col, glyph := range []rune(ansi.Strip(line)) {
					if glyph != '◆' {
						continue
					}
					dx := float64(col) - float64(width-1)/2
					dy := (float64(row) - float64(len(markerRows)-1)/2) * 2
					if math.Abs(math.Sqrt(dx*dx+dy*dy)-maxRadius) > 1.5 {
						t.Errorf("marker left rim at phase %.1f: radius %.2f, want %.2f", phase, math.Sqrt(dx*dx+dy*dy), maxRadius)
					}
					found = true
				}
			}
			if !found {
				t.Errorf("rim marker missing at phase %.1f", phase)
			}
		}
		t.Logf("vinyl %dx%d:\n%s", width, len(rows), strings.Join(plain, "\n"))
	}
}

func TestSpectrumRowsRenderBandsAndSettleSilent(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	var spectrum [player.SpectrumBands]float64
	for i := range spectrum {
		spectrum[i] = float64(i+1) / float64(player.SpectrumBands)
	}
	rows := spectrumRows(spectrum, [player.SpectrumBands]float64{}, 32, 4, -1, model.palette())
	if len(rows) != 4 {
		t.Fatalf("spectrum rendered %d rows, want 4", len(rows))
	}
	for i, row := range rows {
		if lipgloss.Width(row) != 32 {
			t.Errorf("spectrum row %d width = %d, want 32", i, lipgloss.Width(row))
		}
	}
	if !strings.ContainsAny(strings.Join(rows, ""), "▁▂▃▄▅▆▇█") {
		t.Fatal("a non-silent spectrum produced no bars")
	}

	silent := spectrumRows([player.SpectrumBands]float64{}, [player.SpectrumBands]float64{}, 32, 4, -1, model.palette())
	if strings.ContainsAny(strings.Join(silent, ""), "▁▂▃▄▅▆▇█") {
		t.Fatalf("a silent spectrum should render empty bars:\n%s", strings.Join(silent, "\n"))
	}
}

func TestSpectrumSmoothsRiseAndDecay(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status.Playing = true
	for i := range model.spectrumTarget {
		model.spectrumTarget[i] = 1
	}
	updated, _ := model.Update(tickMsg(time.Now()))
	model = updated.(Model)
	if model.spectrum[0] <= 0 || model.spectrum[0] >= 1 {
		t.Fatalf("spectrum did not ease toward the target: %.3f", model.spectrum[0])
	}

	model.status.Playing = false
	model.spectrumTarget = [player.SpectrumBands]float64{}
	for i := 0; i < 200 && model.hasPendingEase(); i++ {
		updated, _ = model.Update(tickMsg(time.Now()))
		model = updated.(Model)
	}
	if model.spectrum[0] > 0.02 {
		t.Fatalf("spectrum failed to settle after pause: %.3f", model.spectrum[0])
	}
}

func TestDeckVisualizerStartsAfterControlsAndUsesDenseWidth(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Playing: true, Speed: 1, Duration: 10, Position: 5, Volume: 0.7, RMS: 0.7}
	model.rmsLevel = 0.7
	for i := range model.spectrum {
		model.spectrum[i] = 1
	}
	model.width, model.height = 110, 34
	lines := strings.Split(ansi.Strip(model.deckPanel(60, 28, model.palette())), "\n")
	controlRow := -1
	visualizerRow := -1
	for i, line := range lines {
		if strings.Contains(line, "VOL") {
			controlRow = i
			if i+1 < len(lines) && strings.ContainsAny(lines[i+1], "▁▂▃▄▅▆▇█┃") {
				visualizerRow = i + 1
			}
			break
		}
	}
	if controlRow < 0 || visualizerRow != controlRow+1 {
		t.Fatalf("visualizer does not immediately follow controls: control row %d, visualizer row %d", controlRow, visualizerRow)
	}
	barCount := 0
	for _, line := range lines[visualizerRow:] {
		rowBars := 0
		for _, glyph := range []rune(line) {
			if strings.ContainsRune("▁▂▃▄▅▆▇█┃", glyph) {
				rowBars++
			}
		}
		barCount = max(barCount, rowBars)
	}
	if barCount < 40 || barCount > 60 {
		t.Fatalf("visualizer has %d bars, want 40–60", barCount)
	}
}

func TestViewDoesNotEmitBackgroundColor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {110, 34}, {130, 40}} {
		model := NewModel(nil, nil, config.Defaults())
		model.width, model.height = size[0], size[1]
		view := model.View()
		if strings.Contains(view, "\x1b[48;") || strings.Contains(view, "\x1b[49m") {
			t.Errorf("view at %dx%d emitted terminal background-color escapes", size[0], size[1])
		}
	}
}

func TestProgressHeadPulsesOnlyWhilePlaying(t *testing.T) {
	paused := progressBar(4, 20, 24, 0, false)
	pausedLater := progressBar(4, 20, 24, 2, false)
	playing := progressBar(4, 20, 24, 0, true)
	if paused != pausedLater {
		t.Fatal("paused progress head should be steady")
	}
	if paused == playing {
		t.Fatal("playing progress head should pulse")
	}
}

func TestLibraryPanelUsesAvailableHeight(t *testing.T) {
	tracks := make([]library.Track, 20)
	for i := range tracks {
		title := "Track " + string(rune('A'+i))
		path := "/music/" + string(rune('a'+i)) + ".flac"
		tracks[i] = library.Track{Title: title, Path: path}
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.status = player.Status{Track: tracks[0], Index: 0, Count: len(tracks)}
	panel := ansi.Strip(model.libraryPanel(50, 44, model.palette()))
	shown := 0
	for _, track := range tracks {
		if strings.Contains(panel, track.Title) {
			shown++
		}
	}
	if shown <= 8 {
		t.Fatalf("tall library panel showed only %d tracks; want more than 8", shown)
	}
}

func TestHelpOverlayTogglesAndFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 36}} {
		model := NewModel(nil, &player.Engine{}, config.Defaults())
		model.width, model.height = size[0], size[1]

		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
		model = updated.(Model)
		if !model.showHelp {
			t.Fatalf("? did not open help at %dx%d", size[0], size[1])
		}
		view := ansi.Strip(model.View())
		if !strings.Contains(view, "KEYBINDINGS") || !strings.Contains(view, "SPACE") {
			t.Fatalf("help overlay missing content at %dx%d:\n%s", size[0], size[1], view)
		}
		lines := strings.Split(model.View(), "\n")
		if len(lines) != size[1] {
			t.Fatalf("help at %dx%d has %d rows, want %d", size[0], size[1], len(lines), size[1])
		}
		for row, line := range lines {
			if width := lipgloss.Width(line); width != size[0] {
				t.Fatalf("help at %dx%d row %d is %d wide, want %d", size[0], size[1], row, width, size[0])
			}
		}

		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		if updated.(Model).showHelp {
			t.Fatalf("a key press did not close help at %dx%d", size[0], size[1])
		}
	}
}

func TestWindowTitleIncludesTrackAndArtist(t *testing.T) {
	if got := windowTitle(library.Track{}); got != "VYNL" {
		t.Fatalf("empty title = %q", got)
	}
	if got := windowTitle(library.Track{Path: "/m/a.flac", Title: "Blue", Artist: "Miles"}); got != "VYNL · Blue — Miles" {
		t.Fatalf("title = %q", got)
	}
	if got := windowTitle(library.Track{Path: "/m/track.flac"}); got != "VYNL · track" {
		t.Fatalf("fallback title = %q", got)
	}
}

func TestCompactViewShowsStateTrackAndProgress(t *testing.T) {
	tracks := []library.Track{{Title: "Compact", Path: "c.flac"}}
	model := NewModel(tracks, nil, config.Defaults())
	model.width, model.height = 20, 8
	model.status = player.Status{Track: tracks[0], Count: 1, Playing: true, Position: 5, Duration: 20}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "PLAYING") || !strings.Contains(view, "Compact") {
		t.Fatalf("compact view missing state/track:\n%s", view)
	}
	if !strings.ContainsAny(view, "━─◆◇") {
		t.Fatalf("compact view missing progress bar:\n%s", view)
	}
}

func TestLibrarySortCycleReordersTracks(t *testing.T) {
	tracks := []library.Track{
		{Title: "Alpha", Artist: "Zeta", Album: "One", Path: "a.flac"},
		{Title: "Zulu", Artist: "Beta", Album: "Two", Path: "b.flac"},
		{Title: "Mike", Artist: "Alpha", Album: "Two", Path: "c.flac"},
	}
	model := NewModel(tracks, &player.Engine{}, config.Defaults())
	titles := func(m Model) string {
		out := make([]string, 0, len(m.visible))
		for _, index := range m.visible {
			out = append(out, m.tracks[index].Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles(model); got != "Alpha,Zulu,Mike" {
		t.Fatalf("default path order = %s", got)
	}

	model.selected = 0 // keep "Alpha" selected through the reorder
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	model = updated.(Model)
	if got := titles(model); got != "Alpha,Mike,Zulu" {
		t.Fatalf("title order = %s", got)
	}
	if model.sortKey.label() != "TITLE" {
		t.Fatalf("sort label = %s, want TITLE", model.sortKey.label())
	}
	if selected := model.tracks[model.visible[model.selected]].Title; selected != "Alpha" {
		t.Fatalf("sort moved the cursor off the selected track: %s", selected)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	model = updated.(Model)
	if got := titles(model); got != "Mike,Zulu,Alpha" {
		t.Fatalf("artist order = %s", got)
	}
}

func TestDeckShowsNextTrack(t *testing.T) {
	tracks := []library.Track{
		{Title: "Current", Path: "a.flac"},
		{Title: "Upcoming", Path: "b.flac"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.status = player.Status{Track: tracks[0], Index: 0, Count: 2, NextIndex: 1, NextTrack: tracks[1], Playing: true, Speed: 1}
	panel := ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "NEXT  Upcoming") {
		t.Fatalf("deck missing next track:\n%s", panel)
	}

	model.status.Count = 1
	if got := model.nextTrackTitle(); got != "" {
		t.Fatalf("single-track next title = %q, want empty", got)
	}
}

func TestSearchArrowKeysMoveSelection(t *testing.T) {
	tracks := []library.Track{
		{Title: "Alpha", Path: "a.flac"},
		{Title: "Gamma", Path: "c.flac"},
		{Title: "Zulu", Path: "z.flac"},
	}
	model := NewModel(tracks, &player.Engine{}, config.Defaults())
	model.search = "a"
	model.refreshVisible()
	model.searching = true
	if len(model.visible) < 2 {
		t.Fatalf("expected multiple matches, got %v", model.visible)
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.selected != 1 {
		t.Fatalf("down in search moved selection to %d, want 1", model.selected)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(Model)
	if model.selected != 0 {
		t.Fatalf("up in search moved selection to %d, want 0", model.selected)
	}
}

func TestHelpLineReflectsFocusAndConfiguredKeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Keybindings.Shuffle = "j"
	model := NewModel(nil, &player.Engine{}, cfg)

	model.deckFocused = true
	deck := ansi.Strip(model.helpLine(model.palette()))
	for _, expected := range []string{"J shuffle", "vinyl", "mute", "repeat"} {
		if !strings.Contains(deck, expected) {
			t.Errorf("deck help missing %q: %q", expected, deck)
		}
	}

	model.deckFocused = false
	library := ansi.Strip(model.helpLine(model.palette()))
	for _, expected := range []string{"browse", "find", "TAB deck"} {
		if !strings.Contains(library, expected) {
			t.Errorf("library help missing %q: %q", expected, library)
		}
	}

	model.searching = true
	model.search = "jazz"
	search := ansi.Strip(model.helpLine(model.palette()))
	if !strings.Contains(search, "jazz") || !strings.Contains(search, "pick") {
		t.Errorf("search help = %q, want query and navigation hint", search)
	}
}

func TestKeyLabel(t *testing.T) {
	cases := map[string]string{
		" ": "SPACE", "left": "←", "right": "→", "up": "↑", "down": "↓",
		"q": "Q", "esc": "esc", "home": "home",
	}
	for key, want := range cases {
		if got := keyLabel(key); got != want {
			t.Errorf("keyLabel(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestLibraryMarksPlayingTrackAndActiveFilter(t *testing.T) {
	tracks := []library.Track{
		{Title: "First", Artist: "One", Path: "/music/1.flac"},
		{Title: "Second", Artist: "Two", Path: "/music/2.flac"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.status = player.Status{Track: tracks[1], Index: 1, Count: 2}
	panel := ansi.Strip(model.libraryPanel(50, 30, model.palette()))
	if !strings.Contains(panel, "▶ Second") {
		t.Fatalf("library did not mark the playing track:\n%s", panel)
	}
	if strings.Contains(panel, "▶ First") {
		t.Fatalf("library marked a non-playing track:\n%s", panel)
	}

	model.search = "second"
	model.refreshVisible()
	panel = ansi.Strip(model.libraryPanel(50, 30, model.palette()))
	if !strings.Contains(panel, "FILTER: second") {
		t.Fatalf("library did not show the active filter:\n%s", panel)
	}
}

func TestDeckShowsSkippedNotice(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Playing: true, Count: 2, Volume: 0.5, Skipped: 2}
	panel := ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "SKIPPED 2 UNPLAYABLE") {
		t.Fatalf("deck did not surface skipped tracks:\n%s", panel)
	}

	// A hard error is more important than the informational skip notice.
	model.status = player.Status{Count: 2, Volume: 0.5, Skipped: 2, Err: "decode failed"}
	panel = ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "decode failed") || strings.Contains(panel, "SKIPPED") {
		t.Fatalf("error did not take precedence over the skip notice:\n%s", panel)
	}
}

func TestNowPlayingKeyMovesCursorToLoadedTrack(t *testing.T) {
	tracks := []library.Track{
		{Title: "Alpha", Path: "a.flac"},
		{Title: "Bravo", Path: "b.flac"},
		{Title: "Charlie", Path: "c.flac"},
	}
	model := NewModel(tracks, &player.Engine{}, config.Defaults())
	model.status = player.Status{Track: tracks[2], Index: 2, Count: 3}
	model.selected = 0

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	model = updated.(Model)
	if got := model.tracks[model.visible[model.selected]].Path; got != "c.flac" {
		t.Fatalf("now-playing key selected %q, want the loaded c.flac", got)
	}

	// A track filtered out of the view is not selected (the cursor stays put).
	model.search = "bravo"
	model.refreshVisible()
	before := model.selected
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	model = updated.(Model)
	if model.selected != before {
		t.Fatalf("now-playing key moved the cursor to a filtered-out track: %d -> %d", before, model.selected)
	}
}

func TestEQKeysAreConfigurable(t *testing.T) {
	cfg := config.Defaults()
	cfg.Keybindings.EQLow, cfg.Keybindings.EQMid, cfg.Keybindings.EQHigh = "j", "k", "l"
	model := NewModel(nil, &player.Engine{}, cfg)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	model = updated.(Model)
	if model.eqBand != 2 {
		t.Fatalf("configured high-band key set band %d, want 2", model.eqBand)
	}

	// Once rebound, the old hardcoded digit must not select a band.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	model = updated.(Model)
	if model.eqBand != 2 {
		t.Fatalf("hardcoded band key stayed active after rebinding: band %d", model.eqBand)
	}

	panel := ansi.Strip(model.eqPanel(80, 5, model.palette()))
	if !strings.Contains(panel, "J/K/L") {
		t.Fatalf("EQ panel did not reflect configured band keys:\n%s", panel)
	}
}

func TestPaletteUsesThemeColorsForMutedPinkError(t *testing.T) {
	cfg := config.Defaults()
	cfg.Theme.Muted, cfg.Theme.Pink, cfg.Theme.Error = "#111111", "#222222", "#333333"
	palette := NewModel(nil, nil, cfg).palette()
	cases := map[string]struct {
		style lipgloss.Style
		want  string
	}{
		"muted": {palette.muted, "#111111"},
		"pink":  {palette.pink, "#222222"},
		"error": {palette.error, "#333333"},
	}
	for name, test := range cases {
		got, ok := test.style.GetForeground().(lipgloss.Color)
		if !ok || string(got) != test.want {
			t.Errorf("%s color = %v, want %s", name, test.style.GetForeground(), test.want)
		}
	}
}

func TestHelpOverlayListsConfiguredEQKeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Keybindings.EQLow, cfg.Keybindings.EQGainUp = "j", "p"
	model := NewModel(nil, nil, cfg)
	model.width, model.height = 100, 30
	view := ansi.Strip(model.helpOverlay(model.palette()))
	if !strings.Contains(view, "J 2 3") || !strings.Contains(view, "P -") {
		t.Fatalf("help overlay did not reflect configured EQ keys:\n%s", view)
	}
}

func TestDeckShowsPlaybackModes(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Playing: true, Count: 1, Volume: 0.5, Shuffle: true, Repeat: player.RepeatOne}
	panel := ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "SHUFFLE") || !strings.Contains(panel, "REPEAT ONE") {
		t.Fatalf("deck did not show shuffle/repeat state:\n%s", panel)
	}

	model.status.Repeat = player.RepeatOff
	panel = ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if strings.Contains(panel, "REPEAT") {
		t.Fatalf("deck showed a repeat mode while repeat was off:\n%s", panel)
	}

	model.status.Muted = true
	panel = ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "MUTED") {
		t.Fatalf("deck did not show the muted state:\n%s", panel)
	}
}

func TestDeckShowsReplayGainWhenActive(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Playing: true, Count: 1, Volume: 0.5, ReplayGain: "off"}
	panel := ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if strings.Contains(panel, "RG ") {
		t.Fatalf("deck showed ReplayGain while it was off:\n%s", panel)
	}

	model.status.ReplayGain = "track"
	model.status.TrackGainDB = -6.7
	panel = ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "RG TRACK") || !strings.Contains(panel, "-6.7dB") {
		t.Fatalf("deck did not show the active ReplayGain mode/gain:\n%s", panel)
	}
}

func TestLibraryBoundaryAndPageNavigation(t *testing.T) {
	tracks := make([]library.Track, 30)
	for i := range tracks {
		tracks[i] = library.Track{
			Title: "Track " + string(rune('A'+i%26)),
			Path:  "t" + string(rune('a'+i%26)) + ".flac",
		}
	}
	model := NewModel(tracks, &player.Engine{}, config.Defaults())
	model.height = 30

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnd})
	model = updated.(Model)
	if model.selected != len(model.visible)-1 {
		t.Fatalf("End selected %d, want %d", model.selected, len(model.visible)-1)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyHome})
	model = updated.(Model)
	if model.selected != 0 {
		t.Fatalf("Home selected %d, want 0", model.selected)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if model.selected != model.libraryPageSize() {
		t.Fatalf("PgDown selected %d, want %d", model.selected, model.libraryPageSize())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if model.selected != 0 {
		t.Fatalf("PgUp selected %d, want 0", model.selected)
	}

	// Navigation works while the search box is open and is not typed as text.
	model.searching = true
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnd})
	model = updated.(Model)
	if !model.searching || model.selected != len(model.visible)-1 {
		t.Fatalf("search End: searching %t selected %d", model.searching, model.selected)
	}
}

func TestDeckShowsCrossfade(t *testing.T) {
	cfg := config.Defaults()
	cfg.Playback.CrossfadeMS = 3000
	model := NewModel(nil, nil, cfg)
	model.status = player.Status{Playing: true, Count: 1, Volume: 0.5}
	panel := ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if !strings.Contains(panel, "XFADE 3.0s") {
		t.Fatalf("deck did not show the configured crossfade:\n%s", panel)
	}

	cfg.Playback.CrossfadeMS = 0
	model = NewModel(nil, nil, cfg)
	panel = ansi.Strip(model.deckPanel(80, 24, model.palette()))
	if strings.Contains(panel, "XFADE") {
		t.Fatalf("deck showed crossfade while it was disabled:\n%s", panel)
	}
}

func viewText(lines []string) string {
	return strings.Join(lines, "\n")
}

func TestAnimationTickStopsWhenPaused(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.frame = 3
	updated, command := model.Update(tickMsg(time.Now()))
	if command != nil || updated.(Model).frame != 3 {
		t.Fatal("paused tick should not advance or schedule another animation frame")
	}

	model.status.Playing = true
	updated, command = model.Update(tickMsg(time.Now()))
	if command == nil || updated.(Model).frame != 4 {
		t.Fatal("playing tick should advance and schedule the next frame")
	}
}

func TestPlaybackPositionInterpolatesBetweenStatusUpdates(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Position: 2, Duration: 20, Speed: 2, Playing: true}
	model.statusAt = time.Now().Add(-250 * time.Millisecond)
	if position := model.playbackPosition(); position < 2.4 || position > 2.6 {
		t.Fatalf("interpolated position = %.2f, want about 2.50", position)
	}
}

func TestPlaybackPositionFreezesWhileBuffering(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Position: 4, Duration: 20, Speed: 1, Playing: true, Buffering: true}
	model.statusAt = time.Now().Add(-2 * time.Second)
	if got := model.playbackPosition(); got != 4 {
		t.Fatalf("buffering position advanced to %.2f seconds, want 4", got)
	}
}

func TestReadTrackDetailsStatsFileAndFormatsDisplay(t *testing.T) {
	path := writeTestWAV(t, 48000, 4800)
	details := readTrackDetails(path)
	if details.bytes != int64(44+4800*2) {
		t.Fatalf("file size = %d, want %d", details.bytes, 44+4800*2)
	}
	display := strings.Join(details.displayLines(0.1, 48000, NewModel(nil, nil, config.Defaults()).palette()), " ")
	if !strings.Contains(display, "WAV") || !strings.Contains(display, "48.0 kHz") || !strings.Contains(display, "AVG") {
		t.Fatalf("metadata display = %q", display)
	}
}

func TestClockFormatsLongDurations(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{-5, "00:00"},
		{0, "00:00"},
		{59, "00:59"},
		{60, "01:00"},
		{3599, "59:59"},
		{3600, "1:00:00"},
		{3661, "1:01:01"},
		{7325, "2:02:05"},
	}
	for _, test := range cases {
		if got := clock(test.seconds); got != test.want {
			t.Errorf("clock(%.0f) = %q, want %q", test.seconds, got, test.want)
		}
	}
}

func TestTrackDetailsLoadAsynchronously(t *testing.T) {
	path := writeTestWAV(t, 48000, 4800)
	model := NewModel([]library.Track{{Path: path}}, &player.Engine{}, config.Defaults())
	model.status = player.Status{Track: library.Track{Path: path}}

	updated, command := model.Update(statusMsg(model.status))
	if command == nil {
		t.Fatal("status message did not schedule asynchronous detail loading")
	}
	model = updated.(Model)
	if model.loaded.path != path {
		t.Fatalf("reserved details path = %q, want %q", model.loaded.path, path)
	}
	if model.loaded.bytes != 0 {
		t.Fatalf("details were read synchronously: bytes %d", model.loaded.bytes)
	}

	message := loadTrackDetails(path)()
	if _, ok := message.(trackDetailsMsg); !ok {
		t.Fatalf("detail command returned %T, want trackDetailsMsg", message)
	}
	updated, _ = model.Update(message)
	if got := updated.(Model).loaded.bytes; got != int64(44+4800*2) {
		t.Fatalf("loaded file size = %d, want %d", got, 44+4800*2)
	}

	model = updated.(Model)
	updated, _ = model.Update(trackDetailsMsg(trackDetails{path: "other.wav", bytes: 1}))
	if got := updated.(Model).loaded.bytes; got != int64(44+4800*2) {
		t.Fatalf("stale detail message was applied: bytes %d", got)
	}
}

func writeTestWAV(t *testing.T, sampleRate, frames int) string {
	t.Helper()
	data := make([]byte, 44+frames*2)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(data[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(len(data)-44))
	path := filepath.Join(t.TempDir(), "sample.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLibraryUsesMetadataAndStereoMeterForRemainingSpace(t *testing.T) {
	track := library.Track{Title: "Only Track", Artist: "One Artist", Path: "/music/only.wav"}
	model := NewModel([]library.Track{track}, nil, config.Defaults())
	model.status = player.Status{Track: track, Count: 1, Duration: 180, Playing: true, ChannelRMS: [2]float64{0.5, 0.2}, SampleRate: 48000}
	model.loaded = trackDetails{path: track.Path, bytes: 1200000}
	panel := ansi.Strip(model.libraryPanel(50, 30, model.palette()))
	for _, expected := range []string{"NOW LOADED", "WAV", "48.0 kHz", "L       R"} {
		if !strings.Contains(panel, expected) {
			t.Errorf("library panel missing %q", expected)
		}
	}
	if got := strings.Count(panel, "Only Track"); got != 1 {
		t.Errorf("short track list stretched/repeated: rendered %d copies", got)
	}
}

func TestSpectrumHoldLagsFallingBarsAndRenders(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status.Playing = true
	for i := range model.spectrumTarget {
		model.spectrumTarget[i] = 1
	}
	for i := 0; i < 12; i++ {
		updated, _ := model.Update(tickMsg(time.Now()))
		model = updated.(Model)
	}
	if model.spectrumHold[0] < model.spectrum[0] {
		t.Fatalf("hold %.3f below bar %.3f while rising", model.spectrumHold[0], model.spectrum[0])
	}

	model.spectrumTarget = [player.SpectrumBands]float64{}
	updated, _ := model.Update(tickMsg(time.Now()))
	model = updated.(Model)
	if model.spectrumHold[0] <= model.spectrum[0] {
		t.Fatalf("hold %.3f did not lag the falling bar %.3f", model.spectrumHold[0], model.spectrum[0])
	}

	var bar, hold [player.SpectrumBands]float64
	for i := range bar {
		bar[i] = 0.2
		hold[i] = 0.8
	}
	rendered := strings.Join(spectrumRows(bar, hold, 16, 6, -1, model.palette()), "\n")
	if !strings.Contains(rendered, "▔") {
		t.Fatalf("peak-hold marker missing:\n%s", rendered)
	}
}

func TestSpectrumScanlineMovesAcrossColumns(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	var spectrum [player.SpectrumBands]float64
	for i := range spectrum {
		spectrum[i] = 0.5
	}
	first := strings.Join(spectrumRows(spectrum, [player.SpectrumBands]float64{}, 56, 4, 8, model.palette()), "\n")
	second := strings.Join(spectrumRows(spectrum, [player.SpectrumBands]float64{}, 56, 4, 21, model.palette()), "\n")
	if first == second || strings.Count(first, "┃") != 4 || strings.Count(second, "┃") != 4 {
		t.Fatal("visualizer scanline should move to the requested column across all bar rows")
	}
}

func TestCtrlCQuitsEvenWhileSearchingOrHelpOpen(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	setups := []struct {
		name  string
		apply func(*Model)
	}{
		{"deck", func(*Model) {}},
		{"search", func(m *Model) { m.searching = true }},
		{"help", func(m *Model) { m.showHelp = true }},
	}
	for _, setup := range setups {
		t.Run(setup.name, func(t *testing.T) {
			m := model
			setup.apply(&m)
			_, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if command == nil {
				t.Fatal("ctrl+c did not return a command")
			}
			if _, ok := command().(tea.QuitMsg); !ok {
				t.Fatal("ctrl+c did not produce a quit")
			}
		})
	}
}

func TestTagBatchAppliesUpdatesInPlace(t *testing.T) {
	tracks := []library.Track{
		{Path: "a.flac", Title: "a"},
		{Path: "b.flac", Title: "b"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.sortKey = sortByTitle
	updated, _ := model.Update(tagBatchMsg{gen: model.scanGen, updates: []library.TagUpdate{
		{Index: 1, Track: library.Track{Path: "b.flac", Title: "Zed", Artist: "Artist"}},
	}})
	got := updated.(Model)
	if got.tracks[1].Title != "Zed" || got.tracks[1].Artist != "Artist" {
		t.Fatalf("tag update not applied in place: %+v", got.tracks[1])
	}
	if got.tagScanned != 1 {
		t.Fatalf("tagScanned = %d, want 1", got.tagScanned)
	}
}

func TestTagUpdatesAdvanceProgressToTotalThenClear(t *testing.T) {
	tracks := []library.Track{
		{Path: "a.mp3", Title: "a"},
		{Path: "b.mp3", Title: "b"},
		{Path: "c.mp3", Title: "c"},
	}
	model := NewModel(tracks, nil, config.Defaults()).WithTagUpdates(make(chan []library.TagUpdate), nil)
	updates := make([]library.TagUpdate, len(tracks))
	for i := range tracks {
		updates[i] = library.TagUpdate{Index: i, Track: library.Track{Path: tracks[i].Path, Title: "tagged"}}
	}
	updated, _ := model.Update(tagBatchMsg{gen: model.scanGen, updates: updates})
	got := updated.(Model)
	if got.tagScanned != len(tracks) {
		t.Fatalf("tagScanned = %d, want %d", got.tagScanned, len(tracks))
	}
	updated, _ = got.Update(tagScanDoneMsg{gen: got.scanGen})
	if updated.(Model).tagScanning {
		t.Fatal("tagScanDoneMsg did not clear the scanning indicator")
	}
}

func TestWithTagUpdatesEnablesScanning(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults()).WithTagUpdates(make(chan []library.TagUpdate), nil)
	if !model.tagScanning || model.tagUpdates == nil {
		t.Fatal("WithTagUpdates did not enable scanning")
	}
}

func TestScanningIndicatorRendersInLibraryHeader(t *testing.T) {
	tracks := []library.Track{{Path: "a.mp3", Title: "A"}, {Path: "b.mp3", Title: "B"}}
	model := NewModel(tracks, nil, config.Defaults())
	model.width, model.height = 120, 32
	model.status = player.Status{Track: tracks[0], Count: 2, Playing: true, Speed: 1, Volume: 0.8}
	model.tagScanning = true
	model.tagScanned = 1
	if view := model.View(); !strings.Contains(view, "SCANNING 1/2") {
		t.Fatal("scanning indicator missing from the rendered view")
	}
}

func TestCurrentTrackPrefersScannedMetadata(t *testing.T) {
	tracks := []library.Track{{Path: "a.mp3", Title: "fallback-a"}}
	model := NewModel(tracks, nil, config.Defaults())
	model.status = player.Status{Track: tracks[0], Index: 0, Count: 1}
	model.tracks[0] = library.Track{Path: "a.mp3", Title: "Tagged", Artist: "The Artist"}
	got := model.currentTrack()
	if got.Title != "Tagged" || got.Artist != "The Artist" {
		t.Fatalf("currentTrack = %+v, want tagged metadata", got)
	}
}

func TestCurrentTrackFallsBackWhenNothingLoaded(t *testing.T) {
	tracks := []library.Track{{Path: "a.mp3", Title: "a"}}
	model := NewModel(tracks, nil, config.Defaults())
	if got := model.currentTrack(); got.Path != "" {
		t.Fatalf("currentTrack = %+v, want empty before a track is loaded", got)
	}
}

func TestWaitForTagsReportsBatchThenDone(t *testing.T) {
	channel := make(chan []library.TagUpdate, 1)
	channel <- []library.TagUpdate{{Index: 0, Track: library.Track{Path: "a"}}}
	message := waitForTags(7, channel)()
	batch, ok := message.(tagBatchMsg)
	if !ok || len(batch.updates) != 1 || batch.gen != 7 {
		t.Fatalf("waitForTags returned %T %+v, want one-item tagBatchMsg for gen 7", message, message)
	}
	close(channel)
	done, ok := waitForTags(7, channel)().(tagScanDoneMsg)
	if !ok || done.gen != 7 {
		t.Fatal("closed tag stream did not report tagScanDoneMsg for the same generation")
	}
}

func TestCurrentTrackRejectsStaleIndex(t *testing.T) {
	tracks := []library.Track{
		{Path: "a.mp3", Title: "A"},
		{Path: "b.mp3", Title: "B"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	// The status index is stale (0) but the loaded track is b: the path check
	// must fall back to the engine's copy rather than render the wrong title.
	model.status = player.Status{Index: 0, Count: 2, Track: library.Track{Path: "b.mp3", Title: "B"}}
	if got := model.currentTrack(); got.Path != "b.mp3" || got.Title != "B" {
		t.Fatalf("currentTrack = %+v, want the b path", got)
	}
}

func TestLibraryMarksPlayingTrackByPathNotIndex(t *testing.T) {
	tracks := []library.Track{
		{Title: "First", Path: "/music/1.flac"},
		{Title: "Second", Path: "/music/2.flac"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.status = player.Status{Track: tracks[1], Index: 0, Count: 2}
	panel := ansi.Strip(model.libraryPanel(50, 30, model.palette()))
	if !strings.Contains(panel, "▶ Second") || strings.Contains(panel, "▶ First") {
		t.Fatalf("playing marker followed the stale index instead of the path:\n%s", panel)
	}
}

func TestNextTrackTitleIsPathKeyed(t *testing.T) {
	tracks := []library.Track{
		{Title: "Current", Path: "a.flac"},
		{Title: "Old Tag", Path: "b.flac"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.tracks[1].Title = "Fresh Tag"
	model.status = player.Status{Count: 2, Track: tracks[0], NextTrack: tracks[1]}
	if got := model.nextTrackTitle(); got != "Fresh Tag" {
		t.Fatalf("nextTrackTitle = %q, want the tagged title for the upcoming path", got)
	}
}

func TestRefreshVisibleKeepsCursorByPath(t *testing.T) {
	tracks := []library.Track{
		{Title: "A", Path: "a.flac"},
		{Title: "B", Path: "b.flac"},
	}
	model := NewModel(tracks, nil, config.Defaults())
	model.selected = 1 // B
	model.tracks = []library.Track{
		{Title: "B", Path: "b.flac"},
		{Title: "A", Path: "a.flac"},
	}
	model.refreshVisibleKeeping("b.flac")
	if got := model.tracks[model.visible[model.selected]].Path; got != "b.flac" {
		t.Fatalf("cursor = %q, want b.flac", got)
	}
}

func TestRescanResultStartsPendingSwapWithoutAdoptingList(t *testing.T) {
	old := []library.Track{{Title: "Old", Path: "old.mp3"}}
	model := NewModel(old, &player.Engine{}, config.Defaults())
	model.scanGen = 1
	updated, _ := model.Update(rescanResultMsg{gen: 1, paths: []string{"new-a.mp3", "new-b.mp3"}})
	got := updated.(Model)
	if len(got.tracks) != 1 || got.tracks[0].Path != "old.mp3" {
		t.Fatalf("rescan adopted the new list before the engine ack: %+v", got.tracks)
	}
	if len(got.pendingTracks) != 2 || got.pendingRevision != 1 {
		t.Fatalf("pending swap = %d tracks rev %d, want 2 rev 1", len(got.pendingTracks), got.pendingRevision)
	}
	if got.rescanning {
		t.Fatal("rescan indicator stayed on after the walk completed")
	}
}

func TestRescanFailureKeepsLibraryAndNotes(t *testing.T) {
	old := []library.Track{{Title: "Old", Path: "old.mp3"}}
	model := NewModel(old, &player.Engine{}, config.Defaults())
	model.scanGen = 1
	updated, _ := model.Update(rescanResultMsg{gen: 1, err: errors.New("permission denied")})
	got := updated.(Model)
	if got.notice == "" || len(got.pendingTracks) != 0 || got.tracks[0].Path != "old.mp3" {
		t.Fatalf("failed rescan changed state: notice %q pending %d tracks %+v", got.notice, len(got.pendingTracks), got.tracks)
	}
}

func TestRescanZeroTracksKeepsLibraryAndNotes(t *testing.T) {
	old := []library.Track{{Title: "Old", Path: "old.mp3"}}
	model := NewModel(old, &player.Engine{}, config.Defaults())
	model.scanGen = 1
	updated, _ := model.Update(rescanResultMsg{gen: 1})
	got := updated.(Model)
	if got.notice == "" || len(got.pendingTracks) != 0 || len(got.tracks) != 1 {
		t.Fatalf("zero-track rescan = notice %q pending %d tracks %d", got.notice, len(got.pendingTracks), len(got.tracks))
	}
}

func TestCommitRescanRequiresAckAndKeepsCursor(t *testing.T) {
	old := []library.Track{
		{Title: "A", Path: "a.mp3"},
		{Title: "B", Path: "b.mp3"},
		{Title: "C", Path: "c.mp3"},
	}
	model := NewModel(old, &player.Engine{}, config.Defaults())
	model.selected = 2 // C
	model.trackRevision = 1
	model.pendingTracks = []library.Track{
		{Title: "C", Path: "c.mp3"},
		{Title: "A", Path: "a.mp3"},
		{Title: "B", Path: "b.mp3"},
	}
	model.pendingPaths = nil // mechanics test: no tag goroutines need draining
	model.pendingRevision = 2

	// A status from before the swap (echoing the previously committed revision)
	// must not adopt the pending list.
	updated, _ := model.Update(statusMsg(player.Status{Revision: 1, Count: 3, Index: 0, Track: library.Track{Path: "c.mp3"}}))
	if got := updated.(Model); got.trackRevision != 1 || got.pendingTracks == nil {
		t.Fatal("a stale revision committed the pending swap")
	}

	// The matching revision commits and keeps the cursor on C by path.
	updated, _ = updated.(Model).Update(statusMsg(player.Status{Revision: 2, Count: 3, Index: 0, Track: library.Track{Path: "c.mp3"}}))
	got := updated.(Model)
	if got.trackRevision != 2 || got.pendingTracks != nil {
		t.Fatalf("matching revision did not commit: revision %d pending %d", got.trackRevision, len(got.pendingTracks))
	}
	if selected := got.tracks[got.visible[got.selected]].Path; selected != "c.mp3" {
		t.Fatalf("cursor moved off C after commit: %q", selected)
	}
}

func TestStaleTagBatchIsDroppedAndNotRearmed(t *testing.T) {
	tracks := []library.Track{{Path: "a", Title: "a"}, {Path: "b", Title: "b"}}
	model := NewModel(tracks, nil, config.Defaults()).WithTagUpdates(make(chan []library.TagUpdate), nil)

	updated, command := model.Update(tagBatchMsg{gen: 0, updates: []library.TagUpdate{{Index: 0, Track: library.Track{Path: "a", Title: "OLD"}}}})
	got := updated.(Model)
	if got.tracks[0].Title != "a" || got.tagScanned != 0 {
		t.Fatalf("stale batch was applied: %+v", got.tracks[0])
	}
	if command != nil {
		t.Fatal("stale batch re-armed the old tag channel")
	}

	updated, command = got.Update(tagBatchMsg{gen: got.scanGen, updates: []library.TagUpdate{{Index: 0, Track: library.Track{Path: "a", Title: "NEW"}}}})
	got = updated.(Model)
	if got.tracks[0].Title != "NEW" || got.tagScanned != 1 || command == nil {
		t.Fatalf("current batch not applied/re-armed: title %q scanned %d cmd %v", got.tracks[0].Title, got.tagScanned, command)
	}
}

func TestRescanKeyStartsCancellableWalk(t *testing.T) {
	model := NewModel([]library.Track{{Path: "a"}}, &player.Engine{}, config.Defaults())
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyF5})
	got := updated.(Model)
	if command == nil || !got.rescanning {
		t.Fatalf("rescan key did not start a walk: cmd %v rescanning %t", command, got.rescanning)
	}
	if got.scanGen == 0 || got.scanCancel == nil {
		t.Fatal("rescan key did not establish a cancellable walk generation")
	}
	got.CancelScan()
}

func TestSecondRescanSupersedesFirstWalk(t *testing.T) {
	model := NewModel([]library.Track{{Path: "a"}}, &player.Engine{}, config.Defaults())
	updated, first := model.Update(tea.KeyMsg{Type: tea.KeyF5})
	model = updated.(Model)
	firstGen := model.scanGen
	if first == nil {
		t.Fatal("first rescan produced no walk command")
	}
	updated, second := model.Update(tea.KeyMsg{Type: tea.KeyF5})
	model = updated.(Model)
	if second == nil || model.scanGen == firstGen {
		t.Fatalf("second rescan did not supersede the first (gen %d -> %d)", firstGen, model.scanGen)
	}
	// The superseded walk's result is dropped by generation.
	updated, _ = model.Update(rescanResultMsg{gen: firstGen, paths: []string{"stale.mp3"}})
	if got := updated.(Model); got.pendingTracks != nil {
		t.Fatal("a superseded walk's result was adopted")
	}
	model.CancelScan()
}

func TestCancelScanCancelsInFlightWalk(t *testing.T) {
	model := NewModel([]library.Track{{Path: "a"}}, &player.Engine{}, config.Defaults())
	walk := model.startRescan()
	model.CancelScan()

	message := walk()
	result, ok := message.(rescanResultMsg)
	if !ok {
		t.Fatalf("walk command returned %T, want rescanResultMsg", message)
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled walk error = %v, want context.Canceled", result.err)
	}
}

func TestRMSAnimationEasesToRestAfterPause(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status.Playing = true
	model.rmsTarget = 0.8
	model.channelTarget = [2]float64{0.7, 0.3}
	model.bassTarget = 0.4
	updated, command := model.Update(tickMsg(time.Now()))
	model = updated.(Model)
	if model.rmsLevel <= 0 || model.rmsLevel >= model.rmsTarget || model.channelLevel[0] <= 0 || model.bassLevel <= 0 || command == nil {
		t.Fatalf("playing amplitude did not ease toward targets: RMS %.3f, channels %v, bass %.3f", model.rmsLevel, model.channelLevel, model.bassLevel)
	}
	model.status.Playing = false
	model.rmsTarget = 0
	model.channelTarget = [2]float64{}
	model.bassTarget = 0
	start := model.rmsLevel
	for i := 0; i < 100; i++ {
		updated, command = model.Update(tickMsg(time.Now()))
		model = updated.(Model)
		if command == nil {
			break
		}
	}
	if model.rmsLevel >= start || model.rmsLevel > 0.008 || model.hasPendingEase() {
		t.Fatalf("paused amplitude failed to settle: started %.3f, ended %.3f, pending %t", start, model.rmsLevel, model.hasPendingEase())
	}
}
