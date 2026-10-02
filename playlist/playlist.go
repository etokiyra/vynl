// Package playlist stores VYNL playlists as extended M3U (M3U8) files.
//
// A playlist is a UTF-8 text file: an optional #EXTM3U header, optional
// #EXTINF/-style comment lines, and one audio path per line. Paths are resolved
// relative to the playlist file's directory on load, and written relative when
// they stay inside that directory (otherwise absolute), so a playlist directory
// can be moved as a unit.
//
// The package treats playlist files as untrusted input: it bounds line length
// and entry count, ignores directives, strips file:// schemes, and never opens
// or decodes the referenced audio (that is the player's job, behind its own
// format check).
package playlist

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	// MaxEntries bounds how many paths a single playlist may hold, so a
	// hostile or corrupt file cannot exhaust memory.
	MaxEntries = 10000
	// maxLineBytes bounds a single line; longer lines are truncated rather
	// than read into memory unbounded.
	maxLineBytes = 4096
	// maxNameRunes bounds a playlist's display/file name.
	maxNameRunes = 80
	// extension is the on-disk format VYNL writes. M3U (plain) files can also
	// be read.
	extension = ".m3u8"
)

// Playlist is an ordered list of audio file paths. Name is the display name
// (the file base without extension) and File is the .m3u8 file it lives in
// (empty for a playlist that has not been saved yet).
type Playlist struct {
	Name  string
	File  string
	Paths []string
}

// supported reports whether path has an audio extension VYNL can play. It is
// duplicated from the library package on purpose: playlist parsing must not
// import the audio/decoding side just to validate an extension.
func supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".flac", ".wav", ".ogg":
		return true
	default:
		return false
	}
}

// IsPlayable reports whether path is a regular file with a supported audio
// extension. The UI uses it to flag unavailable entries without handing the
// path to the decoder.
func IsPlayable(path string) bool {
	if path == "" || !supported(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Dir returns the directory playlists live in, beside the config file.
func Dir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "playlists")
}

// FileName returns the on-disk path for a named playlist.
func FileName(dir, name string) string {
	return filepath.Join(dir, name+extension)
}

// SanitizeName cleans a user-provided playlist name into something safe to use
// as a file name: control characters and path separators are removed, the name
// is trimmed and length-bounded, and empty/dot names are rejected. It returns
// false when nothing usable remains.
func SanitizeName(name string) (string, bool) {
	name = strings.ToValidUTF8(name, "")
	var builder strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
	}
	result := strings.TrimSpace(builder.String())
	runes := []rune(result)
	if len(runes) > maxNameRunes {
		result = strings.TrimSpace(string(runes[:maxNameRunes]))
	}
	switch result {
	case "", ".", "..":
		return "", false
	}
	return result, true
}

// NameFromFile derives a display name from a playlist file path.
func NameFromFile(file string) string {
	return strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
}

// Load reads an M3U/M3U8 playlist. Every resolvable, non-empty path is kept in
// order; comment/directive lines (including #EXTINF) are ignored. Paths are
// resolved to absolute, cleaned paths. Unreadable files and oversized playlists
// return an error; individual entries that do not exist are still returned so
// the UI can show them as unavailable.
func Load(file string) (Playlist, error) {
	playlist := Playlist{Name: NameFromFile(file), File: file}
	handle, err := os.Open(file)
	if err != nil {
		return playlist, err
	}
	defer handle.Close()

	dir := filepath.Dir(file)
	scanner := bufio.NewScanner(handle)
	// Bound each token so a hostile single-line file cannot exhaust memory;
	// Scanner stops with ErrTooLong instead of reading the rest.
	scanner.Buffer(make([]byte, 0, 4096), maxLineBytes)
	for scanner.Scan() {
		if path, ok := parseLine(dir, scanner.Text()); ok {
			if len(playlist.Paths) >= MaxEntries {
				return playlist, fmt.Errorf("playlist has more than %d entries", MaxEntries)
			}
			playlist.Paths = append(playlist.Paths, path)
		}
	}
	if err := scanner.Err(); err != nil {
		return playlist, err
	}
	return playlist, nil
}

// parseLine turns one raw M3U line into a resolved path, or ok=false for a
// blank line, a comment/directive, or an empty path.
func parseLine(dir, raw string) (string, bool) {
	line := strings.TrimSpace(strings.TrimRight(raw, "\r\n"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	// Some writers use file:// URLs. Strip the scheme and percent-decode.
	if len(line) >= 7 && strings.EqualFold(line[:7], "file://") {
		if decoded, err := url.PathUnescape(line[7:]); err == nil {
			line = decoded
		}
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	if !filepath.IsAbs(line) {
		line = filepath.Join(dir, line)
	}
	return filepath.Clean(line), true
}

// Save writes a playlist atomically (temp file + rename) so an interrupted
// write cannot leave a half-written file. The parent directory is created if
// needed. Paths are written relative to the playlist when they stay inside its
// directory.
func Save(playlist Playlist) error {
	if playlist.File == "" {
		return errors.New("playlist has no file path")
	}
	dir := filepath.Dir(playlist.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var builder strings.Builder
	builder.WriteString("#EXTM3U\n")
	for _, path := range playlist.Paths {
		builder.WriteString(relativePath(dir, path))
		builder.WriteByte('\n')
	}
	return writeAtomic(playlist.File, []byte(builder.String()))
}

// Delete removes a playlist file.
func Delete(file string) error {
	if file == "" {
		return errors.New("playlist has no file path")
	}
	return os.Remove(file)
}

// LoadDir loads every M3U/M3U8 file in dir, sorted by name. A missing directory
// yields no playlists and no error (VYNL starts with none). Individually
// corrupt files are skipped, and their errors are returned together so the
// caller can surface a notice without losing the good playlists.
func LoadDir(dir string) ([]Playlist, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var playlists []Playlist
	var loadErrors []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".m3u", ".m3u8":
		default:
			continue
		}
		playlist, err := Load(filepath.Join(dir, entry.Name()))
		if err != nil {
			loadErrors = append(loadErrors, fmt.Errorf("%s: %w", entry.Name(), err))
			continue
		}
		playlists = append(playlists, playlist)
	}
	sort.Slice(playlists, func(i, j int) bool { return playlists[i].Name < playlists[j].Name })
	return playlists, errors.Join(loadErrors...)
}

// relativePath returns path relative to dir when that stays inside dir, so a
// self-contained playlist folder survives being moved. Otherwise the absolute
// path is returned. Forward slashes are used for portability.
func relativePath(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}

// writeAtomic writes data to path via a temp file in the same directory and a
// rename, matching the state-file convention.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "playlist-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
