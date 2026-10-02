package library

import (
	"strings"
	"testing"
)

func TestSanitizeTextStripsControlSequencesAndBoundsLength(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Normal Title", "Normal Title"},
		{"escape", "evil\x1b[31mred\x1b[0m", "evil[31mred[0m"},
		{"bell", "bell\x07", "bell"},
		{"newline", "new\nline", "newline"},
		{"tab", "tab\tsep", "tabsep"},
		{"del and c1", "a\x7fb\x9fc", "abc"},
		{"trim", "  padded  ", "padded"},
		{"empty", "", ""},
		{"bounded", strings.Repeat("x", maxLabelRunes+50), strings.Repeat("x", maxLabelRunes)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := SanitizeText(test.in); got != test.want {
				t.Fatalf("SanitizeText(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestFallbackTrackSanitizesFilename(t *testing.T) {
	track := fallbackTrack("/music/evil\x1b[2Jname.mp3")
	if strings.ContainsRune(track.Title, 0x1b) {
		t.Fatalf("fallback title leaked a control character: %q", track.Title)
	}
	if track.Title != "evil[2Jname" {
		t.Fatalf("fallback title = %q, want sanitized name", track.Title)
	}
}
