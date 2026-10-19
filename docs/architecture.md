# Architecture

One Go binary serves the API and the embedded frontend (plain ES modules, no build step, no CDN).

```
browser ──HTTPS──▶ reverse proxy (auth) ──HTTP──▶ media-ingest
                                                   ├─ /api/*      JSON API (+ tus uploads)
                                                   ├─ /*          embedded web app (PWA)
                                                   ├─ SQLite      /data/media-ingest.db
                                                   ├─ os.Root     MEDIA_ROOT (all file access)
                                                   ├─ TMDB API    metadata + posters (server side only)
                                                   └─ Jellyfin    optional notification
```

## Flow of a batch

1. **Plan** (`POST /api/plan/{movie,tv,quick}`): the browser sends structured data — provider id, season
   and episode numbers, roles, sizes, original names. The server fetches title, year and episode titles
   from the provider, renders the naming template, checks the library and returns the exact paths,
   conflicts and warnings. Nothing on disk changes.
2. **Create** (`POST /api/batches/…`): the same request plus a decision for every conflict. The server
   plans again (the dry run is never trusted) and stores the batch.
3. **Upload** (`/api/uploads`, tus 1.0): each file is streamed in 8 MiB chunks into
   `MEDIA_ROOT/.incoming/<id>.part`. Uploads resume after interruptions; a new request for the same upload
   interrupts a stalled one.
4. **Publish**: when the last byte arrives the file is fsynced, given its mode and owner, and linked to its
   final name with `link(2)`, which fails atomically if the name exists — so nothing is ever overwritten
   silently and the media server never sees a partial or misnamed file. "Replace" first keeps the old file
   reachable in `.trash`.
5. **Notify** Jellyfin in the background; record the result.
6. **Undo** moves published files to `.trash` and restores what they replaced.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/media-ingest` | startup, subcommands, graceful shutdown |
| `internal/config` | environment parsing and validation |
| `internal/auth` | proxy/basic/none authentication, CSRF, client IP, argon2id |
| `internal/server` | HTTP routes, middleware (security headers, rate limits, logging), static files, poster cache |
| `internal/ingest` | planning (movie, TV, quick), batches, tus server, publishing, history, undo, janitor |
| `internal/mediafs` | all file-system operations, confined by `os.Root`; atomic no-replace publish |
| `internal/naming` | templates, presets, sanitizing |
| `internal/parser` | episode rules, rule packs (validated with RE2) |
| `internal/provider` | metadata provider interface; `tmdb` implementation; `fake` for tests |
| `internal/jellyfin` | Jellyfin notification client |
| `internal/store` | SQLite (pure Go, no cgo): settings, sessions, batches, items, uploads |
| `web/static` | frontend: wizard views, tus client, parser, mapping logic, i18n, service worker |

## Security model

See [SECURITY.md](../SECURITY.md#security-model).

## Adding a metadata provider

Implement `provider.Provider` (`internal/provider/provider.go`) — search, details, seasons, `ValidID`, and
`Image`/`ValidImage` for posters, which the server proxies and caches — and register it in
`cmd/media-ingest/main.go`. The rest of the app is provider-neutral; the provider name is part of every API
path (`/api/meta/{provider}/…`, `/api/img/{provider}/…`).
