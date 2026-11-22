// Package mpris publishes VYNL as an MPRIS media player on the session bus, so
// desktop shells, lock screens and status widgets can show what is playing and
// control playback. It is an optional integration: a missing or unreachable
// session bus simply disables it.
package mpris

import (
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/etokiyra/vynl/library"
	"github.com/etokiyra/vynl/player"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	rootInterface   = "org.mpris.MediaPlayer2"
	playerInterface = "org.mpris.MediaPlayer2.Player"
	mediaObjectPath = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	busName         = "org.mpris.MediaPlayer2.vynl"
	// noTrack is the track ID MPRIS reserves for "nothing loaded". It is not a
	// real object; the spec only requires a valid, stable object path.
	noTrack = dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack")
)

// Controller is the engine surface the service needs. Keeping it an interface
// lets tests exercise command translation without a live audio engine.
type Controller interface {
	Send(player.Command)
}

// Service owns the session-bus connection, the exported MPRIS object and the
// latest playback snapshot. Publish is safe to call from the Bubble Tea event
// loop; every call copies the status into a one-slot mailbox so bus traffic can
// never stall the UI. All bus I/O happens on the service goroutine (or on
// godbus's own method goroutines).
type Service struct {
	conn   *dbus.Conn
	engine Controller
	props  *prop.Properties

	updates chan update
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	current snapshot
	quit    func()

	closeOnce sync.Once
}

// update is the latest engine snapshot plus the UI's tagged copy of the track.
// The engine intentionally keeps fallback (filename) metadata, while the UI
// receives tags asynchronously; the UI's copy is what a desktop widget should
// display.
type update struct {
	status player.Status
	track  library.Track
	at     time.Time
}

// snapshot is the state the D-Bus methods read. It is guarded by Service.mu.
type snapshot struct {
	status     player.Status
	track      library.Track
	trackID    dbus.ObjectPath
	positionUS int64
	durationUS int64
	at         time.Time
}

// New connects to the session bus and exports the MPRIS object. The caller
// should treat a non-nil error as "desktop integration unavailable" and keep
// running; no audio or UI behavior depends on it.
func New(engine Controller) (*Service, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect to session bus: %w", err)
	}
	s := &Service{
		conn:    conn,
		engine:  engine,
		updates: make(chan update, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.current = snapshot{
		trackID: noTrack,
		status:  player.Status{Speed: 1, Volume: 1},
	}

	props, err := prop.Export(conn, mediaObjectPath, s.properties())
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("export MPRIS properties: %w", err)
	}
	s.props = props

	if err := exportInterfaces(conn, s, props); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := claimName(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}

	go s.run()
	return s, nil
}

// SetQuit installs the callback used by MPRIS Quit. main wires it to the Bubble
// Tea program so quitting through a desktop shell takes the normal shutdown
// path (state persistence and audio teardown).
func (s *Service) SetQuit(quit func()) {
	s.mu.Lock()
	s.quit = quit
	s.mu.Unlock()
}

// Publish hands the service the latest status. It never blocks: if the service
// goroutine has not consumed the previous update, that one is discarded in
// favour of this one (the same latest-wins policy the engine uses).
func (s *Service) Publish(status player.Status, track library.Track) {
	next := update{status: status, track: track, at: time.Now()}
	select {
	case s.updates <- next:
		return
	default:
	}
	select {
	case <-s.updates:
	default:
	}
	select {
	case s.updates <- next:
	default:
	}
}

// Close stops the service and drops the bus name. It is safe to call once;
// subsequent calls are no-ops.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		// Close the connection before joining the service goroutine: a bus
		// write already in flight then fails immediately instead of holding up
		// shutdown.
		if s.conn != nil {
			_ = s.conn.Close()
		}
		<-s.done
	})
}

func (s *Service) run() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case next := <-s.updates:
			s.apply(next)
		}
	}
}

// apply turns one engine snapshot into MPRIS property changes and, when a
// position jump cannot be explained by elapsed playback, a Seeked signal.
func (s *Service) apply(next update) {
	previous := s.store(next)

	status := next.status
	duration := microseconds(status.Duration)
	position := microseconds(status.Position)
	trackID := trackIDFor(next.track.Path)

	s.setString("PlaybackStatus", playbackStatus(status))
	s.setString("LoopStatus", loopStatus(status.Repeat))
	s.setBool("Shuffle", status.Shuffle)
	s.setFloat("Rate", sanitizeRate(status.Speed))
	s.setFloat("Volume", sanitizeVolume(status.Volume))
	s.setBool("CanSeek", duration > 0)
	s.setBool("CanGoNext", status.Count > 1)
	s.setBool("CanGoPrevious", status.Count > 1)
	// Position is deliberately not announced in PropertiesChanged (MPRIS
	// mandates that); Seeked carries jumps instead.
	s.props.SetMust(playerInterface, "Position", position)

	if previous.track != next.track || previous.durationUS != duration || previous.status.Track.Path != status.Track.Path {
		s.setValue("Metadata", metadataFor(next.track, trackID, duration))
	}

	if seeked, ok := detectedSeek(previous, next, position); ok {
		s.emitSeeked(seeked)
	}
}

// store commits the new snapshot and returns the previous one.
func (s *Service) store(next update) snapshot {
	trackID := trackIDFor(next.track.Path)
	duration := microseconds(next.status.Duration)
	position := microseconds(next.status.Position)
	s.mu.Lock()
	previous := s.current
	s.current = snapshot{
		status: next.status, track: next.track, trackID: trackID,
		positionUS: position, durationUS: duration, at: next.at,
	}
	s.mu.Unlock()
	return previous
}

// detectedSeek reports a position change that elapsed playback cannot explain.
// Track changes and loads are skipped; a pause followed by a jump is a seek.
func detectedSeek(previous snapshot, next update, position int64) (int64, bool) {
	if previous.track.Path == "" || previous.track.Path != next.track.Path {
		return 0, false
	}
	if previous.status.Loading || next.status.Loading || next.status.Buffering {
		return 0, false
	}
	if previous.status.Duration <= 0 || next.status.Duration <= 0 {
		return 0, false
	}
	expected := previous.positionUS
	if previous.status.Playing && next.status.Playing {
		elapsed := next.at.Sub(previous.at).Seconds()
		expected += int64(math.Round(elapsed * sanitizeRate(previous.status.Speed) * 1e6))
	}
	delta := position - expected
	if delta < 0 {
		delta = -delta
	}
	if delta < 1_500_000 { // sub-1.5s drift is normal scheduling jitter
		return 0, false
	}
	return position, true
}

func (s *Service) emitSeeked(position int64) {
	// The connection is nil only in unit tests that exercise command
	// translation without a session bus.
	if s.conn == nil {
		return
	}
	_ = s.conn.Emit(mediaObjectPath, playerInterface+".Seeked", position)
}

func (s *Service) setString(name, value string) {
	if current, ok := s.props.GetMust(playerInterface, name).(string); ok && current == value {
		return
	}
	s.props.SetMust(playerInterface, name, value)
}

func (s *Service) setBool(name string, value bool) {
	if current, ok := s.props.GetMust(playerInterface, name).(bool); ok && current == value {
		return
	}
	s.props.SetMust(playerInterface, name, value)
}

func (s *Service) setFloat(name string, value float64) {
	if current, ok := s.props.GetMust(playerInterface, name).(float64); ok && current == value {
		return
	}
	s.props.SetMust(playerInterface, name, value)
}

func (s *Service) setValue(name string, value any) {
	if reflect.DeepEqual(s.props.GetMust(playerInterface, name), value) {
		return
	}
	s.props.SetMust(playerInterface, name, value)
}

func (s *Service) properties() prop.Map {
	return prop.Map{
		rootInterface: {
			"Identity":            {Value: "VYNL", Emit: prop.EmitConst},
			"DesktopEntry":        {Value: "vynl", Emit: prop.EmitConst},
			"SupportedUriSchemes": {Value: []string{"file"}, Emit: prop.EmitConst},
			"SupportedMimeTypes": {Value: []string{
				"audio/mpeg", "audio/flac", "audio/ogg", "audio/wav", "audio/x-wav",
			}, Emit: prop.EmitConst},
			"CanQuit":          {Value: true, Emit: prop.EmitConst},
			"CanRaise":         {Value: false, Emit: prop.EmitConst},
			"HasTrackList":     {Value: false, Emit: prop.EmitConst},
			"CanSetFullscreen": {Value: false, Emit: prop.EmitConst},
			"Fullscreen":       {Value: false, Emit: prop.EmitConst},
		},
		playerInterface: {
			"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
			"LoopStatus": {
				Value: "None", Writable: true, Emit: prop.EmitTrue,
				Callback: s.onSetLoopStatus,
			},
			"Shuffle": {
				Value: false, Writable: true, Emit: prop.EmitTrue,
				Callback: s.onSetShuffle,
			},
			"Metadata":      {Value: metadataFor(library.Track{}, noTrack, 0), Emit: prop.EmitTrue},
			"Volume":        {Value: 1.0, Writable: true, Emit: prop.EmitTrue, Callback: s.onSetVolume},
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"Rate":          {Value: 1.0, Writable: true, Emit: prop.EmitTrue, Callback: s.onSetRate},
			"MinimumRate":   {Value: 0.5, Emit: prop.EmitConst},
			"MaximumRate":   {Value: 2.0, Emit: prop.EmitConst},
			"CanGoNext":     {Value: false, Emit: prop.EmitTrue},
			"CanGoPrevious": {Value: false, Emit: prop.EmitTrue},
			"CanPlay":       {Value: true, Emit: prop.EmitConst},
			"CanPause":      {Value: true, Emit: prop.EmitConst},
			"CanSeek":       {Value: false, Emit: prop.EmitTrue},
			"CanControl":    {Value: true, Emit: prop.EmitConst},
		},
	}
}

func exportInterfaces(conn *dbus.Conn, service *Service, props *prop.Properties) error {
	if err := conn.Export(service, mediaObjectPath, rootInterface); err != nil {
		return fmt.Errorf("export MPRIS root interface: %w", err)
	}
	// godbus exports Go method names; the MPRIS method is "Seek". SeekRelative
	// is named that way to keep the canonical io.Seeker signature free for vet.
	if err := conn.ExportWithMap(service, map[string]string{"SeekRelative": "Seek"}, mediaObjectPath, playerInterface); err != nil {
		return fmt.Errorf("export MPRIS player interface: %w", err)
	}
	node := &introspect.Node{
		Name: string(mediaObjectPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name: rootInterface,
				Methods: []introspect.Method{
					{Name: "Raise"},
					{Name: "Quit"},
				},
				Properties: props.Introspection(rootInterface),
			},
			{
				Name: playerInterface,
				Methods: []introspect.Method{
					{Name: "Next"},
					{Name: "Previous"},
					{Name: "Pause"},
					{Name: "PlayPause"},
					{Name: "Stop"},
					{Name: "Play"},
					{Name: "Seek", Args: []introspect.Arg{
						{Name: "Offset", Type: "x", Direction: "in"},
					}},
					{Name: "SetPosition", Args: []introspect.Arg{
						{Name: "TrackId", Type: "o", Direction: "in"},
						{Name: "Position", Type: "x", Direction: "in"},
					}},
				},
				Signals: []introspect.Signal{
					{Name: "Seeked", Args: []introspect.Arg{
						{Name: "Position", Type: "x", Direction: "out"},
					}},
				},
				Properties: props.Introspection(playerInterface),
			},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), mediaObjectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("export MPRIS introspection: %w", err)
	}
	return nil
}

// claimName acquires the MPRIS name. A second running VYNL gets a suffixed
// instance name (the spec allows it) instead of failing to publish at all.
func claimName(conn *dbus.Conn) (string, error) {
	names := []string{busName, busName + ".instance" + strconv.Itoa(os.Getpid())}
	for _, name := range names {
		reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
		if err != nil {
			return "", fmt.Errorf("request bus name %s: %w", name, err)
		}
		if reply == dbus.RequestNameReplyPrimaryOwner || reply == dbus.RequestNameReplyAlreadyOwner {
			return name, nil
		}
	}
	return "", errors.New("all MPRIS bus names are already owned")
}

// --- org.mpris.MediaPlayer2 ------------------------------------------------

// Raise is a no-op: a terminal player has no window to bring forward.
func (s *Service) Raise() *dbus.Error { return nil }

// Quit asks the host program to shut down cleanly.
func (s *Service) Quit() *dbus.Error {
	s.mu.Lock()
	quit := s.quit
	s.mu.Unlock()
	if quit != nil {
		quit()
	}
	return nil
}

// --- org.mpris.MediaPlayer2.Player ----------------------------------------
//
// Every exported method returns *dbus.Error because godbus's Export only
// registers methods with that final result. The nil return is an empty,
// successful D-Bus reply.

func (s *Service) Next() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Next})
	return nil
}

func (s *Service) Previous() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Prev})
	return nil
}

func (s *Service) Pause() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Pause})
	return nil
}

func (s *Service) Play() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Play})
	return nil
}

func (s *Service) PlayPause() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Toggle})
	return nil
}

func (s *Service) Stop() *dbus.Error {
	s.engine.Send(player.Command{Action: player.Stop})
	return nil
}

// SeekRelative moves by a relative offset in microseconds. It is exported to
// D-Bus as Seek; the Go name avoids colliding with io.Seeker's canonical
// signature, which go vet checks.
func (s *Service) SeekRelative(offset int64) *dbus.Error {
	s.mu.Lock()
	position := s.current.positionUS
	duration := s.current.durationUS
	s.mu.Unlock()
	if duration <= 0 {
		return nil
	}
	target := clampMicros(position+offset, duration)
	s.engine.Send(player.Command{Action: player.Seek, Value: float64(offset) / 1e6})
	s.emitSeeked(target)
	return nil
}

// SetPosition moves to an absolute position, but only for the track the caller
// thinks is playing; a stale track ID is ignored as the MPRIS spec requires.
func (s *Service) SetPosition(trackID dbus.ObjectPath, position int64) *dbus.Error {
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	if current.trackID == noTrack || trackID != current.trackID || current.durationUS <= 0 {
		return nil
	}
	target := clampMicros(position, current.durationUS)
	s.engine.Send(player.Command{Action: player.SeekTo, Value: float64(target) / 1e6})
	s.emitSeeked(target)
	return nil
}

// --- writable properties --------------------------------------------------

func (s *Service) onSetVolume(change *prop.Change) *dbus.Error {
	value, ok := change.Value.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return prop.ErrInvalidArg
	}
	value = sanitizeVolume(value)
	s.mu.Lock()
	current := sanitizeVolume(s.current.status.Volume)
	s.mu.Unlock()
	s.engine.Send(player.Command{Action: player.Volume, Value: value - current})
	return nil
}

func (s *Service) onSetRate(change *prop.Change) *dbus.Error {
	value, ok := change.Value.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return prop.ErrInvalidArg
	}
	value = sanitizeRate(value)
	s.mu.Lock()
	current := sanitizeRate(s.current.status.Speed)
	s.mu.Unlock()
	s.engine.Send(player.Command{Action: player.Speed, Value: value - current})
	return nil
}

func (s *Service) onSetLoopStatus(change *prop.Change) *dbus.Error {
	target, ok := change.Value.(string)
	if !ok || !validLoopStatus(target) {
		return prop.ErrInvalidArg
	}
	s.mu.Lock()
	current := loopStatus(s.current.status.Repeat)
	s.mu.Unlock()
	// The engine exposes Repeat as a cycle; walk it to the requested mode.
	for steps := 0; steps < 3 && current != target; steps++ {
		s.engine.Send(player.Command{Action: player.Repeat})
		current = nextLoopStatus(current)
	}
	return nil
}

func (s *Service) onSetShuffle(change *prop.Change) *dbus.Error {
	value, ok := change.Value.(bool)
	if !ok {
		return prop.ErrInvalidArg
	}
	s.mu.Lock()
	current := s.current.status.Shuffle
	s.mu.Unlock()
	if value != current {
		s.engine.Send(player.Command{Action: player.Shuffle})
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func playbackStatus(status player.Status) string {
	switch {
	case status.Playing:
		return "Playing"
	case status.Track.Path == "":
		return "Stopped"
	default:
		return "Paused"
	}
}

func loopStatus(repeat player.RepeatMode) string {
	switch repeat {
	case player.RepeatOne:
		return "Track"
	case player.RepeatAll:
		return "Playlist"
	default:
		return "None"
	}
}

func validLoopStatus(value string) bool {
	return value == "None" || value == "Playlist" || value == "Track"
}

func nextLoopStatus(current string) string {
	switch current {
	case "None":
		return "Playlist"
	case "Playlist":
		return "Track"
	default:
		return "None"
	}
}

func sanitizeVolume(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(1, value))
}

func sanitizeRate(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 1
	}
	return math.Max(0.5, math.Min(2, value))
}

// microseconds converts a seconds value to MPRIS's int64 microsecond unit,
// rejecting the zero/negative/NaN duration a loading track reports.
func microseconds(seconds float64) int64 {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	return int64(math.Round(seconds * 1e6))
}

func clampMicros(position, duration int64) int64 {
	if position < 0 {
		return 0
	}
	if position > duration {
		return duration
	}
	return position
}
