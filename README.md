# media-ingest

Self-hosted web app that files your movies and TV episodes into a
Jellyfin/Plex/Emby library: pick the title from TMDB, drop your files, review the exact
final paths, upload. The app does all the naming; you never rename files on your devices.

> **Status: milestone M3**: authentication, movie and TV flows, library awareness,
> history with undo, the Jellyfin hook, localized parser rule packs and quick upload.
> PWA, more UI languages, full docs and CI follow in M4.

![screenshot placeholder](docs/screenshot.png)

## Quick start

```sh
git clone https://github.com/<you>/media-ingest.git && cd media-ingest
cp .env.example .env     # set MEDIA_PATH, PUID/PGID, AUTH_MODE + TRUSTED_PROXIES, TMDB_API_KEY
mkdir -p data && chown 1000:1000 data      # the PUID:PGID from .env
docker compose up -d --build
docker compose logs -f   # JSON logs; look for "listening"
```

Put it behind your reverse proxy (it must set `Remote-User` for `AUTH_MODE=proxy`) and open
`https://media.example.com`. Without `TMDB_API_KEY`, an admin enters the key once in the browser;
it is stored in `/data/tmdb_api_key` (mode 0600) and never sent to browsers.

Update: `git pull && docker compose up -d --build`.

## How files are published

1. Uploads (tus 1.0.0, 8 MiB chunks, resumable) go to `<MEDIA_ROOT>/.incoming`, outside the
   library folders.
2. When a file is complete it is fsynced, given `FILE_MODE` and `PUID:PGID`, and then made
   visible under its final name with a single hard link (fails atomically if the name exists),
   after which the staging name is removed. The media server never sees partial or misnamed files.
3. Existing files are never overwritten silently. You choose per file: **skip**, **replace**
   (the old file is moved to `<MEDIA_ROOT>/.trash/…`) or **keep both** (`… - Version 2`).

Staging and libraries must be on the **same file system** (mount one volume containing both).
All file access is confined to `MEDIA_ROOT` with Go's `os.Root`; paths are built on the server
from provider metadata only — file names sent by the browser are used for the extension and for
display, never as paths.

Default layout (Jellyfin preset; Plex and Emby presets and custom templates in Settings):

```
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].en.forced.srt
movies/Inception (2010) [tmdbid-27205]/Trailers/Official Trailer.mp4
```

## TV episodes

1. Pick the show, then one or more seasons (Specials are Season 0). The season list shows how many
   episodes of each season are already in your library.
2. Add files. Episode numbers are suggested from the file names (`S01E03`, `1x03`,
   `Season 1 Episode 3`, `E03`, `Show - 03`, `03 - Title`, …); a missing season comes from the
   parent folder (`Season 2/`, `S02/`, `Specials/`) or the only selected season. If no video file
   parses, files are assigned in natural order from a start episode you choose.
3. Fix anything: per-file episode dropdown, drag a file onto an episode (desktop), or tap **Pick**
   and then an episode (touch). Dropping onto an occupied episode swaps the two files. A file can
   cover several episodes (`S01E03-E04`). Two files for one episode must be marked as alternate
   versions with different labels, because Jellyfin merges them into one item.
4. Episodes already in the library are marked. Publishing one again requires a choice: skip,
   replace (every existing file of that episode goes to `.trash`) or keep both.

Subtitles take the name of their episode's video — including a video that is already in the
library under a different name, so adding subtitles to existing episodes works.

The parser rules are served by the server ([internal/parser/default.json](internal/parser/default.json))
and validated with Go's RE2 engine, so they can never cause catastrophic backtracking in the browser.

## History and undo

**History** lists every batch you published. **Undo** moves the batch's files to
`<MEDIA_ROOT>/.trash/<time>-<batch>-undo/` (never deletes them), puts back any files the batch had
replaced, and removes folders that became empty. A file that was changed after publishing (its
size differs) is left in place. Trash entries older than `TRASH_RETENTION_DAYS` (default 30,
`0` = forever) are deleted by a background job; only entries the app created are touched.

## Jellyfin

Set `JELLYFIN_URL` and `JELLYFIN_API_KEY` (an API key from Dashboard → API Keys). After each batch
the app calls `POST /Library/Media/Updated` with the new, replaced and removed paths, and falls back
to `POST /Library/Refresh` (full scan). If Jellyfin mounts the media at another path, set
`JELLYFIN_PATH_PREFIX` to the path that corresponds to `MEDIA_ROOT`. Failures are logged and shown
in History; they never affect publishing.

## Episode name rules in other languages

Rule packs for Turkish (`1. Sezon 3. Bölüm`), German (`Staffel 1 Folge 3`) and Spanish
(`Temporada 1 Capítulo 3`, `Cap.103`) ship in [internal/parser/packs](internal/parser/packs) and
are enabled by default (Settings → Episode number rules). Add your own as
`<DATA_DIR>/rules/<name>.yaml` (same format; a file named like a built-in pack replaces it), or paste
custom rules in Settings. Every pattern is validated with Go's RE2 engine; lookarounds and
backreferences are rejected. Print the effective rules with `media-ingest rules --dir /data/rules`.

## Quick upload

For files without metadata: pick any folder under `MEDIA_ROOT` (the staging and trash folders are
hidden), optionally name a new sub-folder, add files. Names are kept, with characters that are
illegal on Windows/SMB replaced. Dropped folders can keep their structure.

## Permissions and the unprivileged-LXC pitfall

Run the container as the owner of your media (`--user PUID:PGID`). A non-root process can only
chown to itself, so published files get the right owner by being created by that user; if
`PUID`/`PGID` differ from the process user the app logs a warning and shows it per file.

In an **unprivileged LXC container**, uid `N` inside the container is uid `N + 100000` on the
host (default idmap). Files written as uid 1000 inside the LXC appear as uid 101000 on the host
and on NFS/SMB exports. Either map the uid through (`lxc.idmap` + `/etc/subuid`), or choose
`PUID`/`PGID` so they match what Jellyfin sees.

## Configuration

Environment variables only (see [.env.example](.env.example)): `TMDB_API_KEY`, `MEDIA_ROOT`,
`TV_DIR`, `MOVIES_DIR`, `PUID`, `PGID`, `UMASK`, `FILE_MODE`, `DIR_MODE`, `AUTH_MODE`
(`proxy` | `basic` | `none`), `TRUSTED_PROXIES`, `AUTH_HEADER`, `ADMIN_USERS`, `ADMIN_GROUP`,
`BASIC_AUTH_USER`, `BASIC_AUTH_HASH`, `AUTH_NONE_CONFIRM`, `MAX_UPLOAD_GB`, `ALLOWED_EXTENSIONS`,
`UPLOAD_EXPIRY_HOURS`, `TRASH_RETENTION_DAYS`, `JELLYFIN_URL`, `JELLYFIN_API_KEY`,
`JELLYFIN_PATH_PREFIX`, `LOG_LEVEL`. Everything else is chosen in the web UI.

## Development

```sh
go test -race ./...          # Go unit and HTTP integration tests
npm ci && npm run lint:js && npm run test:unit
npx playwright install chromium && npm run test:e2e   # mobile-viewport wizard test, fake TMDB
```

## Attribution

This product uses the TMDB API but is not endorsed or certified by TMDB.

## License

[MIT](LICENSE)
