package playlist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	first := touch(t, filepath.Join(root, "music", "a.mp3"))
	second := touch(t, filepath.Join(root, "music", "b.flac"))
	file := FileName(Dir(filepath.Join(root, "config.toml")), "Chill")

	original := Playlist{Name: "Chill", File: file, Paths: []string{first, second}}
	if err := Save(original); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "Chill" {
		t.Fatalf("name = %q, want Chill", loaded.Name)
	}
	if len(loaded.Paths) != 2 || loaded.Paths[0] != first || loaded.Paths[1] != second {
		t.Fatalf("loaded paths = %v, want [%s %s]", loaded.Paths, first, second)
	}
}

func TestSaveWritesRelativePathsForInDirectoryFiles(t *testing.T) {
	dir := t.TempDir()
	nested := touch(t, filepath.Join(dir, "Artist - Song.mp3"))
	file := FileName(dir, "local")

	// Absolute input under the playlist directory must be stored relative.
	if err := Save(Playlist{Name: "local", File: file, Paths: []string{nested}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "#EXTM3U") {
		t.Fatalf("missing M3U header:\n%s", text)
	}
	if !strings.Contains(text, "Artist - Song.mp3") || strings.Contains(text, nested) {
		t.Fatalf("in-directory path was not stored relative:\n%s", text)
	}

	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 1 || loaded.Paths[0] != nested {
		t.Fatalf("relative path did not resolve back: %v", loaded.Paths)
	}
}

func TestLoadResolvesRelativePathsAndIgnoresDirectives(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "mix")
	content := "#EXTM3U\n" +
		"#EXTINF:123,Some Title\n" +
		"song-one.mp3\n" +
		"\n" +
		"# a comment\n" +
		"sub dir/song two.ogg\r\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "song-one.mp3"),
		filepath.Join(dir, "sub dir", "song two.ogg"),
	}
	if len(loaded.Paths) != len(want) {
		t.Fatalf("paths = %v, want %v", loaded.Paths, want)
	}
	for i := range want {
		if loaded.Paths[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, loaded.Paths[i], want[i])
		}
	}
}

func TestLoadHandlesUnicodeAndFileURLs(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "mix")
	unicodePath := filepath.Join(dir, "音楽", "café.flac")
	absolute := "/opt/music/rock.mp3"
	content := "音楽/café.flac\nfile:///opt/music/rock.mp3\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 2 || loaded.Paths[0] != unicodePath || loaded.Paths[1] != absolute {
		t.Fatalf("paths = %v, want [%s %s]", loaded.Paths, unicodePath, absolute)
	}
}

func TestLoadKeepsMissingEntries(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "mix")
	if err := os.WriteFile(file, []byte("gone.mp3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 1 {
		t.Fatalf("missing entry was discarded: %v", loaded.Paths)
	}
	if IsPlayable(loaded.Paths[0]) {
		t.Fatal("a missing path was reported playable")
	}
}

func TestLoadDirMissingDirectoryIsEmpty(t *testing.T) {
	lists, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil || lists != nil {
		t.Fatalf("missing dir = %v, %v; want nil, nil", lists, err)
	}
}

func TestLoadDirSortsAndSkipsCorrupt(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "song.mp3"))
	for name, content := range map[string]string{
		"B.m3u8":   "#EXTM3U\nsong.mp3\n",
		"A.m3u8":   "#EXTM3U\nsong.mp3\n",
		"C.m3u":    "song.mp3\n",
		"D.m3u8":   strings.Repeat("x", maxLineBytes+10),
		"note.txt": "ignored",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lists, err := LoadDir(dir)
	if err == nil {
		t.Fatal("corrupt playlist produced no error")
	}
	names := make([]string, len(lists))
	for i, list := range lists {
		names[i] = list.Name
	}
	if strings.Join(names, ",") != "A,B,C" {
		t.Fatalf("loaded playlists = %v, want [A B C]", names)
	}
}

func TestLoadRejectsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "huge")
	if err := os.WriteFile(file, []byte(strings.Repeat("a", maxLineBytes+1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("oversized line was accepted")
	}
}

func TestLoadRejectsTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "many")
	var builder strings.Builder
	for i := 0; i <= MaxEntries; i++ {
		builder.WriteString("song.mp3\n")
	}
	if err := os.WriteFile(file, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("over-long playlist was accepted")
	}
}

func TestSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "p")
	first := touch(t, filepath.Join(dir, "one.mp3"))
	second := touch(t, filepath.Join(dir, "two.mp3"))
	if err := Save(Playlist{Name: "p", File: file, Paths: []string{first}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Playlist{Name: "p", File: file, Paths: []string{second}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 1 || loaded.Paths[0] != second {
		t.Fatalf("overwrite = %v, want [%s]", loaded.Paths, second)
	}
	// No stray temp files must remain.
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "playlist-") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	file := FileName(dir, "bye")
	if err := Save(Playlist{Name: "bye", File: file}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("playlist still exists after delete: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"My Mix", "My Mix", true},
		{"  spaced  ", "spaced", true},
		{"a/b\\c", "abc", true},
		{"tab\tname", "tabname", true},
		{"", "", false},
		{"   ", "", false},
		{".", "", false},
		{"..", "", false},
		{strings.Repeat("x", maxNameRunes+20), strings.Repeat("x", maxNameRunes), true},
	}
	for _, test := range cases {
		got, ok := SanitizeName(test.in)
		if got != test.want || ok != test.ok {
			t.Errorf("SanitizeName(%q) = (%q, %t), want (%q, %t)", test.in, got, ok, test.want, test.ok)
		}
	}
}

func TestFileNameAndNameFromFile(t *testing.T) {
	dir := Dir("/home/user/.config/vynl/config.toml")
	if dir != filepath.Join("/home/user/.config/vynl", "playlists") {
		t.Fatalf("Dir = %q", dir)
	}
	file := FileName(dir, "Road Trip")
	if file != filepath.Join(dir, "Road Trip.m3u8") {
		t.Fatalf("FileName = %q", file)
	}
	if got := NameFromFile(file); got != "Road Trip" {
		t.Fatalf("NameFromFile = %q", got)
	}
}
