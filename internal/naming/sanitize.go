// Package naming turns structured metadata into safe library paths.
package naming

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxComponentBytes is the longest file or directory name we produce.
// 255 bytes is the limit on ext4, XFS, Btrfs and ZFS.
const MaxComponentBytes = 255

var replacer = strings.NewReplacer(
	": ", " - ", // "Title: Subtitle" -> "Title - Subtitle"
	":", "-",
	"/", "-", "\\", "-",
	"∕", "-", "⁄", "-", "／", "-", "⧸", "-", // slash look-alikes
	"＼", "-", "∖", "-", "⧹", "-", // backslash look-alikes
	"?", "", "*", "", "<", "", ">", "", "|", "-",
	"\"", "'",
)

// SanitizeComponent makes s safe to use as a single path component on Linux,
// macOS and Windows (SMB) clients. It never returns ".", ".." or a string
// containing a separator. The result may be empty; callers must check.
func SanitizeComponent(s string) string {
	return TruncateBytes(cleanComponent(s), MaxComponentBytes)
}

// cleanComponent is SanitizeComponent without the length limit.
func cleanComponent(s string) string {
	s = norm.NFC.String(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError:
			return -1
		case r == '\t' || r == '\n' || r == '\r':
			return ' '
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r): // control, bidi, zero-width
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, s)
	s = replacer.Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	// Leading dots hide files from media servers; trailing dots and spaces
	// are stripped by Windows and break SMB access.
	s = strings.TrimLeft(s, ". ")
	s = strings.TrimRight(s, ". ")
	if isReservedWindowsName(s) {
		s += "_"
	}
	return s
}

// TruncateBytes cuts s to at most n bytes on a rune boundary and trims
// trailing dots and spaces left by the cut.
func TruncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s { // i walks rune start offsets
		if i > n {
			break
		}
		cut = i
	}
	return strings.TrimRight(s[:cut], ". ")
}

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}

func isReservedWindowsName(s string) bool {
	base := s
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimSpace(base))
	if reserved[base] {
		return true
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		rest := base[3:]
		switch rest {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}

// IsJunkName reports macOS/Windows metadata files that must never be ingested.
func IsJunkName(name string) bool {
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	lower := strings.ToLower(name)
	return strings.HasPrefix(name, "._") || lower == ".ds_store" || lower == "thumbs.db" ||
		lower == "desktop.ini" || lower == ".localized" || strings.HasPrefix(lower, ".spotlight-") ||
		lower == ".trashes" || lower == ".fseventsd"
}

// Ext returns the lowercase extension of a file name without the dot, or "".
func Ext(name string) string {
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// Stem returns a file name without directories and extension.
func Stem(name string) string {
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[:i]
	}
	return name
}
