package library

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/dhowden/tag"
)

func TestReplayGainFromVorbisComments(t *testing.T) {
	raw := map[string]interface{}{
		"title":                 "A",
		"replaygain_track_gain": "-7.50 dB",
		"replaygain_album_gain": "+2.00 dB",
	}
	gain := replayGainFromRaw(raw)
	if !gain.HasTrack || gain.TrackDB != -7.5 {
		t.Fatalf("track gain = %v (has %t), want -7.5", gain.TrackDB, gain.HasTrack)
	}
	if !gain.HasAlbum || gain.AlbumDB != 2 {
		t.Fatalf("album gain = %v (has %t), want +2.0", gain.AlbumDB, gain.HasAlbum)
	}
}

func TestReplayGainFromID3TXXXFrames(t *testing.T) {
	raw := map[string]interface{}{
		// dhowden/tag stores each TXXX frame as a *tag.Comm keyed by TXXX,
		// TXXX_0, ... The description names the field.
		"TXXX":   &tag.Comm{Description: "REPLAYGAIN_TRACK_GAIN", Text: "-3.20 dB"},
		"TXXX_0": &tag.Comm{Description: "replaygain_album_gain", Text: "-4.00 dB"},
	}
	gain := replayGainFromRaw(raw)
	if !gain.HasTrack || math.Abs(gain.TrackDB-(-3.2)) > 1e-9 {
		t.Fatalf("track gain = %v (has %t), want -3.2", gain.TrackDB, gain.HasTrack)
	}
	if !gain.HasAlbum || math.Abs(gain.AlbumDB-(-4)) > 1e-9 {
		t.Fatalf("album gain = %v (has %t), want -4.0", gain.AlbumDB, gain.HasAlbum)
	}
}

func TestReplayGainIgnoresMissingAndMalformedValues(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]interface{}
	}{
		{"empty", map[string]interface{}{}},
		{"no gain keys", map[string]interface{}{"replaygain_track_peak": "0.99"}},
		{"non-numeric", map[string]interface{}{"replaygain_track_gain": "loud"}},
		{"units only", map[string]interface{}{"replaygain_track_gain": "dB"}},
		{"out of range", map[string]interface{}{"replaygain_track_gain": "99 dB"}},
		{"infinity", map[string]interface{}{"replaygain_track_gain": "1e999"}},
		{"wrong type", map[string]interface{}{"replaygain_track_gain": []string{"-1 dB"}}},
		{"comm without gain", map[string]interface{}{"TXXX": &tag.Comm{Description: "MOOD", Text: "-1 dB"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			gain := replayGainFromRaw(test.raw)
			if gain.HasTrack || gain.HasAlbum || gain.TrackDB != 0 || gain.AlbumDB != 0 {
				t.Fatalf("malformed metadata produced a gain: %+v", gain)
			}
		})
	}
}

func TestParseGainText(t *testing.T) {
	cases := []struct {
		text string
		want float64
		ok   bool
	}{
		{"-7.23 dB", -7.23, true},
		{"-7.23db", -7.23, true},
		{"+2.0DB", 2, true},
		{" 3 ", 3, true},
		{"0.00 dB", 0, true},
		{"-0.00 dB", 0, true},
		{"", 0, false},
		{"nan", 0, false},
		{"25 dB", 0, false},
		{"-25 dB", 0, false},
	}
	for _, test := range cases {
		got, ok := parseGainText(test.text)
		if ok != test.ok || (ok && math.Abs(got-test.want) > 1e-9) {
			t.Errorf("parseGainText(%q) = (%v, %t), want (%v, %t)", test.text, got, ok, test.want, test.ok)
		}
	}
}

// TestReadReplayGainParsesID3Tag exercises the real open+read path against a
// synthetic ID3v2.3 file carrying a TXXX ReplayGain frame. tag.ReadFrom parses
// a bare ID3 tag without requiring valid audio after it.
func TestReadReplayGainParsesID3Tag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagged.mp3")
	writeID3v2TXXX(t, path,
		[2]string{"REPLAYGAIN_TRACK_GAIN", "-6.70 dB"},
		[2]string{"REPLAYGAIN_ALBUM_GAIN", "-5.10 dB"},
	)

	gain := ReadReplayGain(path)
	if !gain.HasTrack || math.Abs(gain.TrackDB-(-6.7)) > 1e-9 {
		t.Fatalf("track gain = %v (has %t), want -6.7", gain.TrackDB, gain.HasTrack)
	}
	if !gain.HasAlbum || math.Abs(gain.AlbumDB-(-5.1)) > 1e-9 {
		t.Fatalf("album gain = %v (has %t), want -5.1", gain.AlbumDB, gain.HasAlbum)
	}
}

func TestReadReplayGainMissingOrUntaggedFile(t *testing.T) {
	if gain := ReadReplayGain(filepath.Join(t.TempDir(), "nope.mp3")); gain.HasTrack || gain.HasAlbum {
		t.Fatalf("missing file produced gain metadata: %+v", gain)
	}
	path := filepath.Join(t.TempDir(), "plain.wav")
	if err := os.WriteFile(path, []byte("not really a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gain := ReadReplayGain(path); gain.HasTrack || gain.HasAlbum {
		t.Fatalf("untagged file produced gain metadata: %+v", gain)
	}
}

// writeID3v2TXXX writes a bare ID3v2.3 tag (no audio) containing one TXXX frame
// per pair. It is enough for tag.ReadFrom, which detects the ID3 magic and
// parses the tag without validating the audio that would follow.
func writeID3v2TXXX(t *testing.T, path string, pairs ...[2]string) {
	t.Helper()
	var frames bytes.Buffer
	for _, pair := range pairs {
		var body bytes.Buffer
		body.WriteByte(0) // ISO-8859-1 text encoding
		body.WriteString(pair[0])
		body.WriteByte(0)
		body.WriteString(pair[1])

		frames.WriteString("TXXX")
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(body.Len()))
		frames.Write(size[:])
		frames.Write([]byte{0, 0}) // frame flags
		frames.Write(body.Bytes())
	}

	tagSize := frames.Len()
	header := []byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 0}
	// ID3v2.3 tag size is a 28-bit syncsafe integer (7 bits per byte).
	header[6] = byte((tagSize >> 21) & 0x7f)
	header[7] = byte((tagSize >> 14) & 0x7f)
	header[8] = byte((tagSize >> 7) & 0x7f)
	header[9] = byte(tagSize & 0x7f)

	var out bytes.Buffer
	out.Write(header)
	out.Write(frames.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
