package mpris

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/etokiyra/vynl/library"
	"github.com/godbus/dbus/v5"
)

// coverNames are the conventional album-art filenames checked beside a track.
// MPRIS clients (waybar, quickshell/end-4, GNOME, KDE) fetch mpris:artUrl for
// the now-playing thumbnail; VYNL has no embedded-art extractor, so a folder
// image is the best available source.
var coverNames = []string{
	"cover.jpg", "cover.jpeg", "cover.png",
	"folder.jpg", "folder.jpeg", "folder.png",
	"front.jpg", "front.jpeg", "front.png",
	"album.jpg", "album.jpeg", "album.png",
	"albumart.jpg", "albumart.jpeg", "albumart.png",
}

// trackIDFor derives a stable MPRIS track ID from the file path. Stability
// matters: clients use it to tell a metadata refresh apart from a track change,
// and VYNL's tag scan legitimately refreshes metadata without changing tracks.
func trackIDFor(path string) dbus.ObjectPath {
	if path == "" {
		return noTrack
	}
	sum := sha256.Sum256([]byte(path))
	return dbus.ObjectPath("/org/vynl/vynl/track/" + hex.EncodeToString(sum[:16]))
}

// metadataFor builds the MPRIS metadata dictionary. Text fields are sanitized
// because tags are untrusted input and godbus refuses invalid UTF-8/NUL.
func metadataFor(track library.Track, trackID dbus.ObjectPath, durationUS int64) map[string]dbus.Variant {
	if trackID == "" {
		trackID = noTrack
	}
	metadata := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(trackID),
	}
	if track.Path == "" {
		return metadata
	}

	title := library.SanitizeText(track.Title)
	if title == "" {
		title = library.SanitizeText(strings.TrimSuffix(filepath.Base(track.Path), filepath.Ext(track.Path)))
	}
	if title != "" {
		metadata["xesam:title"] = dbus.MakeVariant(title)
	}
	if artist := library.SanitizeText(track.Artist); artist != "" {
		metadata["xesam:artist"] = dbus.MakeVariant([]string{artist})
	}
	if album := library.SanitizeText(track.Album); album != "" {
		metadata["xesam:album"] = dbus.MakeVariant(album)
	}
	if durationUS > 0 {
		metadata["mpris:length"] = dbus.MakeVariant(durationUS)
	}
	metadata["xesam:url"] = dbus.MakeVariant(fileURL(track.Path))
	if art := coverArtURL(track.Path); art != "" {
		metadata["mpris:artUrl"] = dbus.MakeVariant(art)
	}
	return metadata
}

// fileURL renders a local path as a file:// URI. MakeAbsolute is what makes the
// URL well-formed when the music folder itself was configured as a relative
// path; a failure leaves the original path (still a valid URI path on Unix).
func fileURL(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// coverArtURL looks for a conventional cover image beside the track and returns
// its file:// URI, or "" when none exists.
func coverArtURL(trackPath string) string {
	if trackPath == "" {
		return ""
	}
	dir := filepath.Dir(trackPath)
	for _, name := range coverNames {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return fileURL(candidate)
		}
	}
	return ""
}
