package player

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestDecoderPrebuffersThenCompletesTrackInBackground(t *testing.T) {
	const seconds = 4
	const sampleRate = 44100
	path := writeTestWAV(t, seconds, sampleRate)
	track, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, more, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		track.close()
		t.Fatal(err)
	}
	if !more {
		track.close()
		t.Fatal("decoder unexpectedly reached EOF during the initial buffer")
	}
	if len(initial) < initialBufferFrames || len(initial) >= seconds*outputRate {
		track.close()
		t.Fatalf("initial sample count = %d, want at least %d and less than full track", len(initial), initialBufferFrames)
	}
	source := newPCMBufferWithInitial(track.totalFrames, initial)
	streamer := newBufferedTransportStreamer(source, outputRate)
	if got := streamer.DurationSeconds(); got != seconds {
		track.close()
		t.Fatalf("known duration = %.2f seconds, want %d", got, seconds)
	}
	engine := &Engine{source: source}
	engine.startDecoder(track)
	<-engine.decodeDone
	engine.stopDecoder()
	samples, totalFrames, done, decodeErr := source.snapshot()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !done || len(samples) != seconds*outputRate || totalFrames != seconds*outputRate {
		t.Fatalf("background decode ended with %d/%d samples, done=%t; want %d", len(samples), totalFrames, done, seconds*outputRate)
	}
}

func TestDecoderWorkerIsJoinedBeforeTrackReplacement(t *testing.T) {
	path := writeTestWAV(t, 20, 44100)
	track, err := openTrackDecoder(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := track.decodeInitial(initialBufferFrames)
	if err != nil {
		track.close()
		t.Fatal(err)
	}
	engine := &Engine{source: newPCMBufferWithInitial(track.totalFrames, initial)}
	engine.startDecoder(track)
	engine.stopDecoder()
	if engine.decodeCancel != nil || engine.decodeDone != nil {
		t.Fatal("decoder handles remain set after cancel-and-join")
	}
	source := engine.source
	length := source.length()
	if _, _, done, _ := source.snapshot(); !done {
		t.Fatal("cancelled decoder did not close its old buffer")
	}
	if source.length() != length {
		t.Fatalf("old track buffer changed after worker join: %d to %d", length, source.length())
	}
}

func TestNewEngineRejectsEmptyLibrary(t *testing.T) {
	if _, err := NewEngine(nil); err == nil {
		t.Fatal("NewEngine accepted an empty library")
	}
}

func TestEffectiveVolumeRespectsMute(t *testing.T) {
	engine := &Engine{volume: 0.8}
	if got := engine.effectiveVolume(); got != 0.8 {
		t.Fatalf("effective volume = %.2f, want 0.8", got)
	}
	engine.muted = true
	if got := engine.effectiveVolume(); got != 0 {
		t.Fatalf("muted effective volume = %.2f, want 0", got)
	}
	engine.volume = 0.4
	if got := engine.effectiveVolume(); got != 0 {
		t.Fatalf("muted effective volume changed with volume = %.2f, want 0", got)
	}
	engine.muted = false
	if got := engine.effectiveVolume(); got != 0.4 {
		t.Fatalf("unmuted effective volume = %.2f, want 0.4", got)
	}
}

func writeTestWAV(t *testing.T, seconds, sampleRate int) string {
	t.Helper()
	dataSize := seconds * sampleRate * 2 * 2
	data := make([]byte, 44+dataSize)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 2)
	binary.LittleEndian.PutUint32(data[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(data[28:32], uint32(sampleRate*4))
	binary.LittleEndian.PutUint16(data[32:34], 4)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(dataSize))
	path := filepath.Join(t.TempDir(), "progressive.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
