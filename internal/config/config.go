// Package config loads all runtime configuration from environment variables.
// There are no config files: everything else is chosen in the web UI and
// stored in the database under DATA_DIR.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Auth modes.
const (
	AuthProxy = "proxy"
	AuthBasic = "basic"
	AuthNone  = "none"
)

type Config struct {
	ListenAddr string
	DataDir    string

	MediaRoot  string
	TVDir      string // relative to MediaRoot
	MoviesDir  string // relative to MediaRoot
	StagingDir string // relative to MediaRoot
	TrashDir   string // relative to MediaRoot

	PUID, PGID int // -1 = do not chown
	Umask      int
	FileMode   os.FileMode
	DirMode    os.FileMode

	TMDBAPIKey       string
	TMDBBaseURL      string
	TMDBImageBaseURL string

	AuthMode        string
	AuthHeader      string
	GroupsHeader    string
	TrustedProxies  []netip.Prefix
	AuthNoneConfirm bool
	BasicUser       string
	BasicHash       string
	AdminUsers      []string
	AdminGroup      string
	CookieSecure    bool

	MaxUploadSize  int64
	AllowedExts    map[string]bool
	UploadExpiry   time.Duration
	TrashRetention time.Duration // 0 = keep trash forever

	JellyfinURL        string
	JellyfinAPIKey     string
	JellyfinPathPrefix string // MEDIA_ROOT as Jellyfin sees it

	LogLevel string
}

// DefaultVideoExts and DefaultSubtitleExts form the default extension allow-list.
var (
	DefaultVideoExts    = []string{"mkv", "mp4", "m4v", "avi", "mov", "wmv", "mpg", "mpeg", "ts", "m2ts", "webm", "flv", "vob", "ogv", "3gp"}
	DefaultSubtitleExts = []string{"srt", "ass", "ssa", "vtt", "sub", "idx", "sup", "smi"}
)

// Load reads the configuration from the environment and validates it.
func Load() (*Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (*Config, error) {
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	var errs []error
	c := &Config{
		ListenAddr:       env("LISTEN_ADDR", ":8097"),
		DataDir:          env("DATA_DIR", "/data"),
		MediaRoot:        env("MEDIA_ROOT", "/media"),
		TVDir:            env("TV_DIR", "tv"),
		MoviesDir:        env("MOVIES_DIR", "movies"),
		StagingDir:       env("STAGING_DIR", ".incoming"),
		TrashDir:         env("TRASH_DIR", ".trash"),
		TMDBAPIKey:       strings.TrimSpace(getenv("TMDB_API_KEY")),
		TMDBBaseURL:      strings.TrimRight(env("TMDB_BASE_URL", "https://api.themoviedb.org/3"), "/"),
		TMDBImageBaseURL: strings.TrimRight(env("TMDB_IMAGE_BASE_URL", "https://image.tmdb.org/t/p"), "/"),
		AuthMode:         strings.ToLower(env("AUTH_MODE", AuthProxy)),
		AuthHeader:       env("AUTH_HEADER", "Remote-User"),
		GroupsHeader:     env("AUTH_GROUPS_HEADER", "Remote-Groups"),
		BasicUser:        strings.TrimSpace(getenv("BASIC_AUTH_USER")),
		BasicHash:        strings.TrimSpace(getenv("BASIC_AUTH_HASH")),
		AdminGroup:       strings.TrimSpace(getenv("ADMIN_GROUP")),
		LogLevel:         strings.ToLower(env("LOG_LEVEL", "info")),
	}

	var err error
	if c.PUID, err = optInt(getenv("PUID")); err != nil {
		errs = append(errs, fmt.Errorf("PUID: %w", err))
	}
	if c.PGID, err = optInt(getenv("PGID")); err != nil {
		errs = append(errs, fmt.Errorf("PGID: %w", err))
	}
	if c.Umask, err = octal(env("UMASK", "022"), 0o777); err != nil {
		errs = append(errs, fmt.Errorf("UMASK: %w", err))
	}
	fm, err := octal(env("FILE_MODE", "0644"), 0o777)
	if err != nil {
		errs = append(errs, fmt.Errorf("FILE_MODE: %w", err))
	}
	c.FileMode = os.FileMode(fm) //nolint:gosec // octal() bounds it to 0o777
	dm, err := octal(env("DIR_MODE", "0755"), 0o7777)
	if err != nil {
		errs = append(errs, fmt.Errorf("DIR_MODE: %w", err))
	}
	c.DirMode = os.FileMode(dm) //nolint:gosec // octal() bounds it to 0o7777
	if c.DirMode&0o700 != 0o700 {
		errs = append(errs, errors.New("DIR_MODE must give the owner rwx"))
	}

	for _, kv := range []struct {
		name string
		p    *string
	}{{"TV_DIR", &c.TVDir}, {"MOVIES_DIR", &c.MoviesDir}, {"STAGING_DIR", &c.StagingDir}, {"TRASH_DIR", &c.TrashDir}} {
		clean, err := relDir(*kv.p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", kv.name, err))
		}
		*kv.p = clean
	}
	dirs := map[string]string{"TV_DIR": c.TVDir, "MOVIES_DIR": c.MoviesDir, "STAGING_DIR": c.StagingDir, "TRASH_DIR": c.TrashDir}
	for an, a := range dirs {
		for bn, b := range dirs {
			if an < bn && (a == b || within(a, b) || within(b, a)) {
				errs = append(errs, fmt.Errorf("%s and %s must not overlap", an, bn))
			}
		}
	}
	if !strings.HasPrefix(c.MediaRoot, "/") {
		errs = append(errs, errors.New("MEDIA_ROOT must be an absolute path"))
	}

	if c.TrustedProxies, err = parsePrefixes(getenv("TRUSTED_PROXIES")); err != nil {
		errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
	}
	c.AuthNoneConfirm = isTrue(getenv("AUTH_NONE_CONFIRM"))
	c.CookieSecure = !isFalse(getenv("COOKIE_SECURE"))
	for _, u := range strings.Split(getenv("ADMIN_USERS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.AdminUsers = append(c.AdminUsers, u)
		}
	}

	switch c.AuthMode {
	case AuthProxy:
		if len(c.TrustedProxies) == 0 {
			errs = append(errs, errors.New("AUTH_MODE=proxy requires TRUSTED_PROXIES (comma-separated IPs or CIDRs)"))
		}
		if !validHeaderName(c.AuthHeader) || !validHeaderName(c.GroupsHeader) {
			errs = append(errs, errors.New("AUTH_HEADER/AUTH_GROUPS_HEADER contain invalid characters"))
		}
	case AuthBasic:
		if c.BasicUser == "" || c.BasicHash == "" {
			errs = append(errs, errors.New("AUTH_MODE=basic requires BASIC_AUTH_USER and BASIC_AUTH_HASH (generate with: media-ingest hash-password)"))
		}
	case AuthNone:
		if !c.AuthNoneConfirm {
			errs = append(errs, errors.New("AUTH_MODE=none requires AUTH_NONE_CONFIRM=true"))
		}
	default:
		errs = append(errs, fmt.Errorf("AUTH_MODE must be proxy, basic or none, got %q", c.AuthMode))
	}

	maxGiB, err := strconv.ParseFloat(env("MAX_UPLOAD_GB", "200"), 64)
	if err != nil || maxGiB <= 0 {
		errs = append(errs, errors.New("MAX_UPLOAD_GB must be a positive number"))
	}
	c.MaxUploadSize = int64(maxGiB * (1 << 30))

	exts := env("ALLOWED_EXTENSIONS", strings.Join(append(append([]string{}, DefaultVideoExts...), DefaultSubtitleExts...), ","))
	c.AllowedExts = map[string]bool{}
	for _, e := range strings.Split(exts, ",") {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e == "" {
			continue
		}
		if !isAlnum(e) || len(e) > 8 {
			errs = append(errs, fmt.Errorf("ALLOWED_EXTENSIONS: invalid extension %q", e))
			continue
		}
		c.AllowedExts[e] = true
	}

	hours, err := strconv.Atoi(env("UPLOAD_EXPIRY_HOURS", "72"))
	if err != nil || hours < 1 {
		errs = append(errs, errors.New("UPLOAD_EXPIRY_HOURS must be a positive integer"))
	}
	c.UploadExpiry = time.Duration(hours) * time.Hour

	days, err := strconv.Atoi(env("TRASH_RETENTION_DAYS", "30"))
	if err != nil || days < 0 {
		errs = append(errs, errors.New("TRASH_RETENTION_DAYS must be 0 (keep forever) or a positive integer"))
	}
	c.TrashRetention = time.Duration(days) * 24 * time.Hour

	c.JellyfinURL = strings.TrimRight(strings.TrimSpace(getenv("JELLYFIN_URL")), "/")
	c.JellyfinAPIKey = strings.TrimSpace(getenv("JELLYFIN_API_KEY"))
	c.JellyfinPathPrefix = strings.TrimRight(env("JELLYFIN_PATH_PREFIX", c.MediaRoot), "/")
	if c.JellyfinPathPrefix == "" && strings.HasPrefix(env("JELLYFIN_PATH_PREFIX", c.MediaRoot), "/") {
		c.JellyfinPathPrefix = "/" // Jellyfin mounts movies/ and tv/ directly at the root
	}
	if c.JellyfinURL != "" {
		u, err := url.Parse(c.JellyfinURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			errs = append(errs, errors.New("JELLYFIN_URL must be an http(s) URL without credentials"))
		}
		if c.JellyfinAPIKey == "" {
			errs = append(errs, errors.New("JELLYFIN_URL requires JELLYFIN_API_KEY"))
		}
		if !strings.HasPrefix(c.JellyfinPathPrefix, "/") {
			errs = append(errs, errors.New("JELLYFIN_PATH_PREFIX must be an absolute path"))
		}
	}

	return c, errors.Join(errs...)
}

// IsVideo reports whether ext (lowercase, no dot) is a known video extension.
func IsVideo(ext string) bool { return contains(DefaultVideoExts, ext) }

// IsSubtitle reports whether ext (lowercase, no dot) is a known subtitle extension.
func IsSubtitle(ext string) bool { return contains(DefaultSubtitleExts, ext) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func optInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1<<31-1 {
		return -1, errors.New("must be a non-negative integer")
	}
	return n, nil
}

func octal(s string, max int64) (int, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 8, 32)
	if err != nil || n < 0 || n > max {
		return 0, fmt.Errorf("invalid octal value %q", s)
	}
	return int(n), nil
}

// relDir validates a directory relative to MEDIA_ROOT.
func relDir(s string) (string, error) {
	if s == "" || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00") {
		return s, errors.New("must be a relative path inside MEDIA_ROOT")
	}
	clean := path.Clean(s)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return s, errors.New("must be a relative path inside MEDIA_ROOT")
	}
	return clean, nil
}

func within(child, parent string) bool { return strings.HasPrefix(child, parent+"/") }

func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", part)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func isAlnum(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func isFalse(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}
