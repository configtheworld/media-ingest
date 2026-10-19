// Command media-ingest is a self-hosted web app that files uploaded media
// into a Jellyfin/Plex/Emby library using provider metadata.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/configtheworld/media-digest/internal/auth"
	"github.com/configtheworld/media-digest/internal/config"
	"github.com/configtheworld/media-digest/internal/ingest"
	"github.com/configtheworld/media-digest/internal/mediafs"
	"github.com/configtheworld/media-digest/internal/parser"
	"github.com/configtheworld/media-digest/internal/provider"
	"github.com/configtheworld/media-digest/internal/provider/tmdb"
	"github.com/configtheworld/media-digest/internal/server"
	"github.com/configtheworld/media-digest/internal/store"
	"github.com/configtheworld/media-digest/web"
)

// Version is set at build time with -ldflags "-X main.Version=...".
var Version = "0.4.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hash-password":
			os.Exit(hashPassword())
		case "healthcheck":
			os.Exit(healthcheck())
		case "rules":
			os.Exit(printRules(os.Args[2:]))
		case "version", "--version":
			fmt.Println(Version)
			return
		default:
			fmt.Fprintf(os.Stderr, "usage: media-ingest [hash-password|healthcheck|rules [--packs a,b] [--dir DIR]|version]\n")
			os.Exit(2)
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func hashPassword() int {
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		pw = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "read password from stdin:", err)
			return 1
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len(pw) < 8 {
		fmt.Fprintln(os.Stderr, "password must be at least 8 characters")
		return 1
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(h)
	return 0
}

// printRules prints the effective parser rules as JSON: the given packs
// (default: all built-in) plus the defaults. Useful to test rule packs.
func printRules(args []string) int {
	packs := strings.Join(parser.BuiltinIDs(), ",")
	dir := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--packs" && i+1 < len(args):
			i++
			packs = args[i]
		case args[i] == "--dir" && i+1 < len(args):
			i++
			dir = args[i]
		default:
			fmt.Fprintln(os.Stderr, "usage: media-ingest rules [--packs tr,de,es] [--dir /data/rules]")
			return 2
		}
	}
	all, err := parser.LoadPacks(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	var ids []string
	for _, id := range strings.Split(packs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	rules := parser.Compose(nil, all, ids)
	if err := rules.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rules)
	return 0
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8097"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz") //nolint:gosec // loopback, port from our own LISTEN_ADDR
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load()
	level := slog.LevelInfo
	if cfg != nil {
		switch cfg.LogLevel {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	syscall.Umask(cfg.Umask)
	if cfg.PUID >= 0 && cfg.PUID != os.Getuid() {
		log.Warn("process uid differs from PUID; chown to PUID only works as root. Run the container with --user PUID:PGID",
			"uid", os.Getuid(), "puid", cfg.PUID)
	}
	if cfg.AuthMode == config.AuthNone {
		log.Warn("AUTH_MODE=none: anyone who can reach this port can write to your library")
	}
	if cfg.AuthMode == config.AuthBasic {
		if err := auth.ValidateHash(cfg.BasicHash); err != nil {
			return fmt.Errorf("BASIC_AUTH_HASH: %w", err)
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("DATA_DIR: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "media-ingest.db"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	keys, err := server.LoadKeyStore(cfg.TMDBAPIKey, cfg.DataDir, log)
	if err != nil {
		return err
	}
	if cfg.JellyfinURL != "" {
		log.Info("jellyfin hook enabled", "url", cfg.JellyfinURL, "path_prefix", cfg.JellyfinPathPrefix)
	}
	if keys.Get() == "" {
		log.Warn("no TMDB API key configured; an admin must complete the setup page")
	}

	mfs, err := mediafs.Open(cfg.MediaRoot, mediafs.Perms{UID: cfg.PUID, GID: cfg.PGID, FileMode: cfg.FileMode, DirMode: cfg.DirMode}, log)
	if err != nil {
		return err
	}
	defer mfs.Close()
	if _, err := mfs.EnsureDir(cfg.StagingDir, false); err != nil {
		return fmt.Errorf("staging directory: %w", err)
	}
	for _, lib := range []string{cfg.MoviesDir, cfg.TVDir} {
		if ok, err := mfs.SameDevice(cfg.StagingDir, lib); err == nil && !ok {
			log.Error("staging directory and library are on different file systems; publishing will fail. Mount one volume containing both.",
				"staging", cfg.StagingDir, "library", lib)
		} else if err != nil {
			log.Info("library directory not present yet; it will be created on first publish", "library", lib)
		}
	}

	tmdbClient := tmdb.New(cfg.TMDBBaseURL, cfg.TMDBImageBaseURL, keys.Get)
	providers := map[string]provider.Provider{tmdbClient.Name(): tmdbClient}
	svc := ingest.New(cfg, mfs, st, providers, log)

	srv, err := server.New(server.Options{
		Config: cfg, Log: log, Store: st, Ingest: svc, Providers: providers, Keys: keys,
		Static: web.FS(), Version: Version,
		NewTMDB: func(key string) provider.Provider {
			return tmdb.New(cfg.TMDBBaseURL, cfg.TMDBImageBaseURL, func() string { return key })
		},
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		svc.Janitor(ctx)
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				svc.Janitor(ctx)
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		// No ReadTimeout/WriteTimeout: upload bodies use per-read idle deadlines.
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "version", Version, "auth_mode", cfg.AuthMode,
			"media_root", cfg.MediaRoot, "movies_dir", cfg.MoviesDir, "tv_dir", cfg.TVDir, "staging_dir", cfg.StagingDir)
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			log.Warn("forced shutdown; interrupted uploads can be resumed", "err", err)
		}
		done := make(chan struct{})
		go func() { svc.WaitHooks(); close(done) }()
		select {
		case <-done:
		case <-shutCtx.Done():
			log.Warn("shutdown before pending Jellyfin notifications finished")
		}
	}
	log.Info("stopped")
	return nil
}
