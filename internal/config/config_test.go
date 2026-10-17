package config

import (
	"net/netip"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := load(envFrom(map[string]string{"TRUSTED_PROXIES": "192.0.2.10"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthMode != AuthProxy || c.MediaRoot != "/media" || c.MoviesDir != "movies" || c.TVDir != "tv" ||
		c.StagingDir != ".incoming" || c.FileMode != 0o644 || c.DirMode != 0o755 || c.PUID != -1 || c.ListenAddr != ":8097" {
		t.Fatalf("%+v", c)
	}
	if !c.TrustedProxies[0].Contains(mustAddr("192.0.2.10")) || c.TrustedProxies[0].Contains(mustAddr("192.0.2.11")) {
		t.Fatal("trusted proxy parsing")
	}
	if !c.AllowedExts["mkv"] || !c.AllowedExts["srt"] || c.AllowedExts["exe"] {
		t.Fatal("extension defaults")
	}
}

func TestFailClosed(t *testing.T) {
	cases := map[string]map[string]string{
		"proxy without trusted proxies": {},
		"none without confirmation":     {"AUTH_MODE": "none"},
		"basic without credentials":     {"AUTH_MODE": "basic"},
		"unknown mode":                  {"AUTH_MODE": "magic"},
		"bad cidr":                      {"TRUSTED_PROXIES": "10.0.0.0/33"},
		"traversal dir":                 {"TRUSTED_PROXIES": "10.0.0.1", "MOVIES_DIR": "../etc"},
		"absolute dir":                  {"TRUSTED_PROXIES": "10.0.0.1", "TV_DIR": "/etc"},
		"staging inside library":        {"TRUSTED_PROXIES": "10.0.0.1", "STAGING_DIR": "movies/.incoming"},
		"same dirs":                     {"TRUSTED_PROXIES": "10.0.0.1", "TV_DIR": "media", "MOVIES_DIR": "media"},
		"bad mode":                      {"TRUSTED_PROXIES": "10.0.0.1", "FILE_MODE": "999"},
		"bad dir mode":                  {"TRUSTED_PROXIES": "10.0.0.1", "DIR_MODE": "0644"},
		"bad puid":                      {"TRUSTED_PROXIES": "10.0.0.1", "PUID": "abc"},
		"bad header":                    {"TRUSTED_PROXIES": "10.0.0.1", "AUTH_HEADER": "Remote User"},
		"bad ext":                       {"TRUSTED_PROXIES": "10.0.0.1", "ALLOWED_EXTENSIONS": "mkv,../x"},
	}
	for name, env := range cases {
		if _, err := load(envFrom(env)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := load(envFrom(map[string]string{"AUTH_MODE": "none", "AUTH_NONE_CONFIRM": "true"})); err != nil {
		t.Errorf("none with confirmation: %v", err)
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	_, err := load(envFrom(map[string]string{"TMDB_API_KEY": "topsecretkey", "BASIC_AUTH_HASH": "secrethash", "AUTH_MODE": "basic"}))
	if err == nil || strings.Contains(err.Error(), "topsecretkey") || strings.Contains(err.Error(), "secrethash") {
		t.Fatalf("%v", err)
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestJellyfinPathPrefix(t *testing.T) {
	base := map[string]string{"TRUSTED_PROXIES": "10.0.0.1", "JELLYFIN_URL": "http://192.0.2.20:8096", "JELLYFIN_API_KEY": "k"}
	for in, want := range map[string]string{"": "/media", "/": "/", "/data/media/": "/data/media", "/mnt/media": "/mnt/media"} {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		if in != "" {
			env["JELLYFIN_PATH_PREFIX"] = in
		}
		c, err := load(envFrom(env))
		if err != nil || c.JellyfinPathPrefix != want {
			t.Errorf("%q: got %q, %v", in, c.JellyfinPathPrefix, err)
		}
	}
	env := map[string]string{"TRUSTED_PROXIES": "10.0.0.1", "JELLYFIN_URL": "http://x:8096", "JELLYFIN_API_KEY": "k", "JELLYFIN_PATH_PREFIX": "relative"}
	if _, err := load(envFrom(env)); err == nil {
		t.Error("relative prefix accepted")
	}
}
