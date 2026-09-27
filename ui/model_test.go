package ui

import (
	"encoding/binary"
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

func TestWaveformFollowsRMSAmplitude(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status.RMS = 0.05
	quiet := model.waveform(16)
	model.status.RMS = 0.5
	loud := model.waveform(16)
	if quiet == loud {
		t.Fatal("visualizer bars should change with RMS amplitude")
	}
	if len([]rune(loud)) != 16 {
		t.Fatalf("visualizer width = %d, want 16", len([]rune(loud)))
	}
}

func TestSpectrumUsesMultipleRMSDrivenBarsAndSettlesPaused(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	p := model.palette()
	playing := spectrumRows(32, 4, 0.65, 2, true, -1, p)
	changed := spectrumRows(32, 4, 0.65, 3, true, -1, p)
	paused := spectrumRows(32, 4, 0.65, 3, false, -1, p)
	if len(playing) != 4 || strings.Join(playing, "\n") == strings.Join(changed, "\n") {
		t.Fatal("spectrum should have multiple rows and animate with the audio envelope")
	}
	if strings.Contains(strings.Join(paused, ""), "▁") || strings.Contains(strings.Join(paused, ""), "█") {
		t.Fatal("paused spectrum should settle to empty bars")
	}
	for i, row := range playing {
		if lipgloss.Width(row) != 32 {
			t.Errorf("spectrum row %d width = %d, want 32", i, lipgloss.Width(row))
		}
	}
}

func TestDeckVisualizerStartsAfterControlsAndUsesDenseWidth(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	model.status = player.Status{Playing: true, Speed: 1, Duration: 10, Position: 5, Volume: 0.7, RMS: 0.7}
	model.rmsLevel = 0.7
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

func TestReadTrackDetailsUsesAudioHeaderAndFileStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.wav")
	data := make([]byte, 44+4800*2)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], 48000)
	binary.LittleEndian.PutUint32(data[28:32], 96000)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(len(data)-44))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	details := readTrackDetails(path)
	if details.format != "WAV" || details.sampleRate != 48000 || details.bytes != int64(len(data)) {
		t.Fatalf("track details = %+v", details)
	}
	if got := strings.Join(details.displayLines(0.1, NewModel(nil, nil, config.Defaults()).palette()), " "); !strings.Contains(got, "48.0 kHz") || !strings.Contains(got, "AVG") {
		t.Fatalf("metadata display = %q", got)
	}
}

func TestLibraryUsesMetadataAndStereoMeterForRemainingSpace(t *testing.T) {
	track := library.Track{Title: "Only Track", Artist: "One Artist", Path: "/music/only.wav"}
	model := NewModel([]library.Track{track}, nil, config.Defaults())
	model.status = player.Status{Track: track, Count: 1, Duration: 180, Playing: true, ChannelRMS: [2]float64{0.5, 0.2}}
	model.loaded = trackDetails{path: track.Path, format: "WAV", sampleRate: 48000, bytes: 1200000}
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

func TestSpectrumScanlineMovesAcrossColumns(t *testing.T) {
	model := NewModel(nil, nil, config.Defaults())
	first := strings.Join(spectrumRows(56, 4, 0.5, 1, true, 8, model.palette()), "\n")
	second := strings.Join(spectrumRows(56, 4, 0.5, 1, true, 21, model.palette()), "\n")
	if first == second || strings.Count(first, "┃") != 4 || strings.Count(second, "┃") != 4 {
		t.Fatal("visualizer scanline should move to the requested column across all bar rows")
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
