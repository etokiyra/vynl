package ui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	"github.com/gopxl/beep"
	"github.com/gopxl/beep/flac"
	"github.com/gopxl/beep/mp3"
	"github.com/gopxl/beep/vorbis"
	"github.com/gopxl/beep/wav"
)

type tickMsg time.Time
type statusMsg player.Status
type trackDetailsMsg trackDetails

type trackDetails struct {
	path       string
	format     string
	sampleRate int
	bytes      int64
	readErr    string
}

type Model struct {
	tracks        []library.Track
	visible       []int
	engine        *player.Engine
	config        config.Config
	status        player.Status
	statusAt      time.Time
	loaded        trackDetails
	width         int
	height        int
	frame         int
	wheelPhase    float64
	rmsLevel      float64
	rmsTarget     float64
	channelLevel  [2]float64
	channelTarget [2]float64
	bassLevel     float64
	bassTarget    float64
	selected      int
	eqBand        int
	search        string
	searching     bool
	deckFocused   bool
}

func NewModel(tracks []library.Track, engine *player.Engine, cfg config.Config) Model {
	m := Model{
		tracks: tracks,
		engine: engine,
		config: cfg,
	}
	m.refreshVisible()
	return m
}

func (m Model) Init() tea.Cmd {
	return waitForStatus(m.engine.Updates())
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
		if m.status.Playing || m.hasPendingEase() {
			return m, tick()
		}
	case statusMsg:
		wasPlaying := m.status.Playing
		m.status = player.Status(msg)
		m.statusAt = time.Now()
		m.rmsTarget = 0
		if m.status.Playing {
			m.rmsTarget = m.status.RMS
			m.channelTarget = m.status.ChannelRMS
			m.bassTarget = m.status.BassRMS
		} else {
			m.channelTarget = [2]float64{}
			m.bassTarget = 0
		}
		commands := []tea.Cmd{waitForStatus(m.engine.Updates())}
		if m.loaded.path != m.status.Track.Path {
			// Reserve the path so repeated status updates do not queue duplicate
			// reads, and load the header off the update loop: decoding a file on
			// a slow or network-mounted library must not stall rendering.
			m.loaded = trackDetails{path: m.status.Track.Path}
			commands = append(commands, loadTrackDetails(m.status.Track.Path))
		}
		if !wasPlaying && m.status.Playing {
			commands = append(commands, tick())
		}
		return m, tea.Batch(commands...)
	case trackDetailsMsg:
		if details := trackDetails(msg); details.path == m.status.Track.Path {
			m.loaded = details
		}
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			m.search = ""
			m.refreshVisible()
			return m, nil
		case "enter":
			if len(m.visible) > 0 {
				m.engine.Send(player.Command{Action: player.Select, Value: float64(m.visible[m.selected])})
			}
			m.searching = false
			m.deckFocused = true
			return m, nil
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
				m.search += string(msg.Runes)
				m.refreshVisible()
			}
			return m, nil
		}
	}

	keys := m.config.Keybindings
	switch {
	case key == keys.Quit:
		return m, tea.Quit
	case key == "tab":
		m.deckFocused = !m.deckFocused
	case key == keys.Search:
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
		m.engine.Send(player.Command{Action: player.Seek, Value: -5})
	case key == keys.SeekForward:
		m.engine.Send(player.Command{Action: player.Seek, Value: 5})
	case key == keys.Restart:
		m.engine.Send(player.Command{Action: player.Restart})
	case key == keys.VolumeUp:
		if m.deckFocused {
			m.engine.Send(player.Command{Action: player.Volume, Value: 0.05})
		} else {
			m.moveSelection(-1)
		}
	case key == keys.VolumeDown:
		if m.deckFocused {
			m.engine.Send(player.Command{Action: player.Volume, Value: -0.05})
		} else {
			m.moveSelection(1)
		}
	case key == keys.Mute:
		m.engine.Send(player.Command{Action: player.Mute})
	case key == keys.SpeedDown:
		m.engine.Send(player.Command{Action: player.Speed, Value: -0.05})
	case key == keys.SpeedUp:
		m.engine.Send(player.Command{Action: player.Speed, Value: 0.05})
	case key == keys.PitchDown:
		m.engine.Send(player.Command{Action: player.Pitch, Value: -1})
	case key == keys.PitchUp:
		m.engine.Send(player.Command{Action: player.Pitch, Value: 1})
	case key == keys.Reset:
		m.engine.Send(player.Command{Action: player.Reset})
	case key == keys.Vinyl:
		m.engine.Send(player.Command{Action: player.Vinyl})
	case key == keys.Shuffle:
		m.engine.Send(player.Command{Action: player.Shuffle})
	case key == keys.Repeat:
		m.engine.Send(player.Command{Action: player.Repeat})
	case key == "1", key == "2", key == "3":
		m.eqBand = int(key[0] - '1')
	case key == "-":
		m.engine.Send(player.Command{Action: player.EQ, Band: m.eqBand, Value: -0.1})
	case key == "+", key == "=":
		m.engine.Send(player.Command{Action: player.EQ, Band: m.eqBand, Value: 0.1})
	case key == "up":
		m.moveSelection(-1)
	case key == "down":
		m.moveSelection(1)
	case key == "enter":
		if len(m.visible) > 0 {
			m.engine.Send(player.Command{Action: player.Select, Value: float64(m.visible[m.selected])})
			m.deckFocused = true
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width < 1 || m.height < 1 {
		return ""
	}
	palette := m.palette()
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
			m.libraryPanel(browserWidth, bodyHeight, palette),
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
			m.libraryPanel(browserWidth, browserHeight, palette),
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
			"TAB library",
			keyLabel(keys.Quit) + " quit",
		}
		return p.muted.Render(strings.Join(parts, "  "))
	}
	return p.muted.Render("↑/↓ browse   ENTER load   " + keyLabel(keys.Search) +
		" find   " + keyLabel(keys.Toggle) + " play   " + keyLabel(keys.Next) + "/" + keyLabel(keys.Prev) +
		" track   TAB deck   " + keyLabel(keys.Quit) + " quit")
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

func (m Model) deckPanel(width, height int, p palette) string {
	inner := max(1, width-6)
	title, artist := "No track loaded", "Scan a folder containing MP3, FLAC, WAV, or OGG files"
	if m.status.Track.Path != "" {
		title = m.status.Track.Title
		artist = m.status.Track.Artist
		if artist == "" {
			artist = m.status.Track.Album
		}
		if artist == "" {
			artist = filepathBase(m.status.Track.Path)
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
		p.cyan.Render(progressBar(position, m.status.Duration, infoWidth, m.frame, m.status.Playing)),
		p.muted.Render(fmt.Sprintf("%s  /  -%s", clock(position), clock(remaining))),
	}
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
	baseLines = append(baseLines, marker+"  "+p.muted.Render(strings.Join(modes, "  ")))
	baseLines = append(baseLines, p.text.Render(truncate(controlText, inner, "~")))
	if m.status.Err != "" {
		baseLines = append(baseLines, p.error.Render(truncate(m.status.Err, inner, "…")))
	}
	visualizerHeight := max(0, contentHeight-len(baseLines))
	needle := -1
	if m.status.Playing && m.status.Duration > 0 {
		needle = min(min(60, inner)-1, int(position/m.status.Duration*float64(min(60, inner))))
	}
	visualizer := spectrumRows(min(60, inner), visualizerHeight, m.visualRMS(), m.frame, m.metersActive(), needle, p)
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
	metaLines := m.loaded.displayLines(m.status.Duration, p)
	reserved := 2 + 1 + len(metaLines)
	maxTracks := max(0, (contentHeight-reserved-3)/entryHeight)
	maxTracks = min(8, maxTracks)
	start, end := 0, min(len(m.visible), maxTracks)
	if len(m.visible) > end {
		start = max(0, min(m.selected-maxTracks/2, len(m.visible)-maxTracks))
		end = start + maxTracks
	}
	header := fmt.Sprintf("%d TRACKS  /  %d MATCH", len(m.tracks), len(m.visible))
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
		if m.status.Count > 0 && trackIndex == m.status.Index {
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
	return panel("EQUALIZER", strings.Join(values, "   ")+"   "+p.muted.Render("BAND 1/2/3  +/- GAIN"), width, height, p.green, p.background)
}

func (m *Model) moveSelection(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.selected = (m.selected + delta + len(m.visible)) % len(m.visible)
}

func (m *Model) refreshVisible() {
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
	sort.SliceStable(results, func(i, j int) bool { return results[i].score < results[j].score })
	m.visible = m.visible[:0]
	for _, result := range results {
		m.visible = append(m.visible, result.index)
	}
	if m.selected >= len(m.visible) {
		m.selected = max(0, len(m.visible)-1)
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

func (m Model) waveform(width int) string {
	levels := "▁▂▃▄▅▆▇█"
	var line strings.Builder
	for i := 0; i < width; i++ {
		columnGain := 0.55 + 0.45*(0.5+0.5*math.Sin(float64(i)*0.63))
		level := int(math.Round(m.status.RMS * 8 * columnGain))
		level = min(len(levels)-1, max(0, level))
		line.WriteRune(rune(levels[level]))
	}
	return line.String()
}

func (m Model) playbackPosition() float64 {
	position := m.status.Position
	if m.status.Playing && !m.status.Buffering && !m.statusAt.IsZero() {
		position += max(0, time.Since(m.statusAt).Seconds()) * max(0, m.status.Speed)
	}
	return math.Min(m.status.Duration, math.Max(0, position))
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
		for _, glyph := range []rune(line) {
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

func spectrumRows(width, height int, rms float64, frame int, playing bool, needle int, p palette) []string {
	if height <= 0 || width <= 0 {
		return nil
	}
	width = min(60, width)
	levels := []rune("▁▂▃▄▅▆▇█")
	peaks := make([]int, width)
	for column := range peaks {
		shape := 0.35 + 0.65*math.Abs(math.Sin(float64(column)*0.31+0.8))
		motion := 0.84 + 0.16*math.Sin(float64(frame)*0.28+float64(column)*0.7)
		if playing {
			peaks[column] = int(math.Round(rms * float64(height*len(levels)) * 1.3 * shape * motion))
		}
		peaks[column] = max(0, min(height*len(levels)-1, peaks[column]))
	}
	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		threshold := (height - row - 1) * len(levels)
		for column, peak := range peaks {
			remaining := peak - threshold
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
			if playing && column == needle {
				line.WriteString(p.text.Bold(true).Render("┃"))
			} else {
				line.WriteString(color.Render(string(glyph)))
			}
		}
		rows[row] = line.String()
	}
	return rows
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
	track := m.status.Track.Title
	if track == "" {
		track = "No track loaded"
	}
	state := "PAUSED"
	if m.status.Playing {
		state = "PLAYING"
	}
	content := p.cyan.Bold(true).Render("VYNL") + "\n" + p.green.Render(state) + "  " + p.text.Render(truncate(track, innerWidth-8, "..."))
	inner := fixedBlock(content, innerWidth, innerHeight, p.background)
	return p.background.Width(innerWidth).Height(innerHeight).Border(lipgloss.RoundedBorder()).
		BorderForeground(p.cyan.GetForeground()).Render(inner)
}

func truncate(text string, width int, suffix string) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if lipgloss.Width(text) <= width {
		return text
	}
	limit := max(0, width-lipgloss.Width(suffix))
	var result strings.Builder
	used := 0
	for _, char := range runes {
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

func loadTrackDetails(path string) tea.Cmd {
	return func() tea.Msg { return trackDetailsMsg(readTrackDetails(path)) }
}

func readTrackDetails(path string) trackDetails {
	details := trackDetails{path: path, format: strings.TrimPrefix(strings.ToUpper(filepath.Ext(path)), ".")}
	if path == "" {
		return details
	}
	info, err := os.Stat(path)
	if err != nil {
		details.readErr = err.Error()
		return details
	}
	details.bytes = info.Size()
	file, err := os.Open(path)
	if err != nil {
		details.readErr = err.Error()
		return details
	}
	defer file.Close()
	var stream beep.StreamSeekCloser
	var format beep.Format
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		stream, format, err = mp3.Decode(file)
	case ".flac":
		stream, format, err = flac.Decode(file)
	case ".wav":
		stream, format, err = wav.Decode(file)
	case ".ogg":
		stream, format, err = vorbis.Decode(file)
	default:
		err = fmt.Errorf("unsupported format")
	}
	if err != nil {
		details.readErr = err.Error()
		return details
	}
	if stream != nil {
		defer stream.Close()
		details.sampleRate = int(format.SampleRate)
	}
	return details
}

func (d trackDetails) displayLines(duration float64, p palette) []string {
	if d.path == "" {
		return []string{p.muted.Render("No track loaded")}
	}
	bitrate := "--"
	if duration > 0 && d.bytes > 0 {
		bitrate = fmt.Sprintf("%d kb/s avg", int(float64(d.bytes)*8/duration/1000))
	}
	rate := "unknown"
	if d.sampleRate > 0 {
		rate = strconv.FormatFloat(float64(d.sampleRate)/1000, 'f', 1, 64) + " kHz"
	}
	return []string{
		p.text.Render(fmt.Sprintf("%s  /  %s", d.format, rate)),
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
		muted:           lipgloss.NewStyle().Foreground(lipgloss.Color("#78828e")),
		cyan:            lipgloss.NewStyle().Foreground(color(theme.Cyan, defaults.Cyan)),
		magenta:         lipgloss.NewStyle().Foreground(color(theme.Magenta, defaults.Magenta)),
		pink:            lipgloss.NewStyle().Foreground(lipgloss.Color("#ff77c8")),
		green:           lipgloss.NewStyle().Foreground(color(theme.Green, defaults.Green)),
		wave:            lipgloss.NewStyle().Foreground(color(theme.Cyan, defaults.Cyan)),
		error:           lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")),
	}
}

type palette struct {
	background, text, muted, cyan, magenta, green, wave, error lipgloss.Style
	pink                                                       lipgloss.Style
	foregroundColor                                            lipgloss.Color
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func waitForStatus(updates <-chan player.Status) tea.Cmd {
	return func() tea.Msg { return statusMsg(<-updates) }
}
