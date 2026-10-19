# Configuration reference

media-ingest is configured with environment variables only. Everything else (naming preset,
templates, parser rule packs, custom rules) is chosen in the web UI and stored in the database
under `DATA_DIR`. Invalid values stop the server at startup with a clear message; secrets are
never printed.

## Required

| Variable | Default | Description |
|---|---|---|
| `TRUSTED_PROXIES` | — | Comma-separated IPs or CIDRs of the reverse proxy, e.g. `192.0.2.10` or `10.0.0.0/8`. Required for `AUTH_MODE=proxy` (the default). The auth header is accepted **only** from these addresses. |
| `TMDB_API_KEY` | — | TMDB API key (v3) or read access token (v4). If empty, an admin enters it once on the setup page; it is then stored in `DATA_DIR/tmdb_api_key` with mode `0600`. |

## Paths

| Variable | Default | Description |
|---|---|---|
| `MEDIA_ROOT` | `/media` | Absolute path of the media volume inside the container. All file access is confined to it. |
| `MOVIES_DIR` | `movies` | Movie library, relative to `MEDIA_ROOT`. |
| `TV_DIR` | `tv` | TV library, relative to `MEDIA_ROOT`. |
| `STAGING_DIR` | `.incoming` | Upload staging area, relative to `MEDIA_ROOT`. Must be on the same file system as the libraries. |
| `TRASH_DIR` | `.trash` | Where replaced and undone files go, relative to `MEDIA_ROOT`. |
| `DATA_DIR` | `/data` | Database, API key file, poster cache, `rules/*.yaml` packs. |

The four relative directories must not overlap. Staging and libraries must be on one file system:
publishing is an atomic hard link / rename and never a copy (it fails with a clear error otherwise).

## Ownership and permissions

| Variable | Default | Description |
|---|---|---|
| `PUID` / `PGID` | unset | Owner applied to published files and new folders. With `docker compose` the container also runs as `PUID:PGID`. A non-root process can only chown to itself, so run the container as this user. |
| `UMASK` | `022` | Process umask. |
| `FILE_MODE` | `0644` | Mode of published files (applied before they appear in the library). |
| `DIR_MODE` | `0755` | Mode of folders the app creates. Must include owner `rwx`. |

## Authentication

| Variable | Default | Description |
|---|---|---|
| `AUTH_MODE` | `proxy` | `proxy`, `basic` or `none`. |
| `AUTH_HEADER` | `Remote-User` | Header carrying the signed-in user (proxy mode). Exactly one value is required. |
| `AUTH_GROUPS_HEADER` | `Remote-Groups` | Comma-separated groups (proxy mode). |
| `ADMIN_USERS` | unset | Comma-separated users allowed to use Settings and the setup page. |
| `ADMIN_GROUP` | unset | Group that grants admin (proxy mode). If neither is set, every signed-in user is admin. |
| `BASIC_AUTH_USER` | — | User name for `AUTH_MODE=basic`. |
| `BASIC_AUTH_HASH` | — | argon2id hash for `AUTH_MODE=basic`; create it with `docker run --rm -i <image> hash-password`. |
| `AUTH_NONE_CONFIRM` | `false` | Must be `true` for `AUTH_MODE=none`. Anyone who can reach the port can then write to your library. |
| `COOKIE_SECURE` | `true` | Set `false` only for plain-HTTP testing without TLS. |

Proxy mode fails closed: requests from other addresses, without the header, or with several header
values get `401`, including the static files (except `/healthz`, `/manifest.webmanifest` and
`/icons/*`, which browsers fetch without cookies). State-changing requests in every mode also need a
CSRF token and same-origin Fetch Metadata.

## Uploads

| Variable | Default | Description |
|---|---|---|
| `MAX_UPLOAD_GB` | `200` | Largest single file. |
| `ALLOWED_EXTENSIONS` | video + subtitle list | Comma-separated allow-list. Default: `mkv,mp4,m4v,avi,mov,wmv,mpg,mpeg,ts,m2ts,webm,flv,vob,ogv,3gp,srt,ass,ssa,vtt,sub,idx,sup,smi`. |
| `UPLOAD_EXPIRY_HOURS` | `72` | Unfinished uploads older than this are removed from staging. |
| `TRASH_RETENTION_DAYS` | `30` | Trash entries older than this are deleted. `0` keeps them forever. Only entries the app created are touched. |

Uploads use [tus 1.0](https://tus.io/protocols/resumable-upload) with 8 MiB chunks, so reverse proxies
only need to accept bodies a little larger than 8 MiB.

## Jellyfin (optional)

| Variable | Default | Description |
|---|---|---|
| `JELLYFIN_URL` | unset | e.g. `http://jellyfin:8096` — the address **this container** uses to reach Jellyfin directly (not through the auth proxy). |
| `JELLYFIN_API_KEY` | — | From Jellyfin → Dashboard → API Keys. Required with `JELLYFIN_URL`. |
| `JELLYFIN_PATH_PREFIX` | `MEDIA_ROOT` | `MEDIA_ROOT` as Jellyfin sees it. Example: Jellyfin mounts the drive at `/data/media` → set `/data/media`; it mounts `movies/` and `tv/` directly at `/movies` and `/tv` → set `/`. |

After each batch and each undo, the app calls `POST /Library/Media/Updated` with the created,
modified and deleted paths, falling back to `POST /Library/Refresh`. It authenticates with
`Authorization: MediaBrowser Token="…"`. Failures are logged and shown in History; they never affect
publishing. To find the paths Jellyfin uses:

```sh
curl -s -H 'Authorization: MediaBrowser Token="KEY"' http://jellyfin:8096/Library/VirtualFolders | grep -o '"Locations":\[[^]]*\]'
```

## Server

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8097` | Listen address. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON on stdout; headers, cookies and query strings are never logged. |
| `TMDB_BASE_URL` | `https://api.themoviedb.org/3` | Override for testing. |
| `TMDB_IMAGE_BASE_URL` | `https://image.tmdb.org/t/p` | Override for testing. |

## docker compose only

These are read by `docker-compose.yml`, not by the app:

| Variable | Default | Description |
|---|---|---|
| `MEDIA_PATH` | `./media` | Host folder mounted at `MEDIA_ROOT`. |
| `DATA_PATH` | `./data` | Host folder mounted at `/data`. |
| `PUBLISH` | `8097` | Published port, e.g. `127.0.0.1:8097` to expose it on this machine only. |
| `VERSION` | — | Version string compiled into a locally built image. |

## Commands

```sh
media-ingest                     # run the server
media-ingest hash-password       # read a password, print an argon2id hash for BASIC_AUTH_HASH
media-ingest healthcheck         # exit 0 if /healthz answers (used by the Docker HEALTHCHECK)
media-ingest rules [--dir DIR] [--packs tr,de,es]   # print the effective parser rules as JSON
media-ingest version
```
