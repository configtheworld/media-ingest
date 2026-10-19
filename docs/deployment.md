# Deployment

The image is small (≈14 MB), multi-arch (`linux/amd64`, `linux/arm64`), runs as a non-root user,
works with a read-only root file system and needs only two volumes: the media tree and `/data`.

## Images

| Tag | Built from |
|---|---|
| `ghcr.io/configtheworld/media-digest:latest`, `:X.Y.Z`, `:X.Y` | release tags `vX.Y.Z` |
| `ghcr.io/configtheworld/media-digest:edge` | every commit on `main` |

Or build it yourself: `docker build -t media-ingest .`

## docker compose (recommended)

```sh
git clone https://github.com/configtheworld/media-digest.git && cd media-digest
cp .env.example .env && chmod 600 .env
```

Edit `.env` (see the [configuration reference](configuration.md)), at least:

```env
MEDIA_PATH=/srv/media          # the folder that contains movies/ and tv/
PUID=1000                      # owner of your media files (ls -ln /srv/media)
PGID=1000
AUTH_MODE=proxy
TRUSTED_PROXIES=192.0.2.10     # your reverse proxy
TMDB_API_KEY=…                 # or leave empty and use the setup page
```

Prepare the folders, then start:

```sh
mkdir -p data /srv/media/.incoming /srv/media/.trash
chown 1000:1000 data /srv/media/.incoming /srv/media/.trash && chmod 700 data
docker compose up -d --build        # or set image: ghcr.io/configtheworld/media-digest:latest
docker compose logs -f              # wait for "listening"
curl -s http://127.0.0.1:8097/healthz
```

Update: `git pull && docker compose up -d --build`.

## docker run

```sh
docker run -d --name media-ingest \
  --user 1000:1000 \
  -p 8097:8097 \
  -v /srv/media:/media \
  -v /srv/media-ingest/data:/data \
  --env-file /srv/media-ingest/media-ingest.env \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --restart unless-stopped \
  ghcr.io/configtheworld/media-digest:latest
```

## Checklist

- **One file system.** Mount the folder that contains the libraries *and* lets the app create
  `.incoming` and `.trash` next to them. Separate bind mounts for `movies/` and `tv/` break atomic
  publishing (the app refuses with a clear error instead of copying).
- **Write access.** The container user needs write access to `MEDIA_ROOT` (or to pre-created
  `.incoming` and `.trash`), to `movies/` and `tv/`, and to existing show folders when you add episodes.
  Check without sudo: `setpriv --reuid=1000 --regid=1000 --clear-groups touch /srv/media/movies/.t && rm /srv/media/movies/.t`.
- **Only the proxy may reach the port.** In proxy mode the app trusts `Remote-User` from
  `TRUSTED_PROXIES`. If that address is a NAT gateway (VPN, tunnel), everything behind it looks like the
  proxy. Firewall the port so that only the proxy can connect, e.g. `PUBLISH=192.0.2.20:8097` plus a
  host firewall rule.
- **Backups.** `/data` holds the history database; the media tree holds your files and `.trash`.

## Permissions and the unprivileged-LXC pitfall

Published files get `FILE_MODE` (0644) and folders `DIR_MODE` (0755) before they become visible, so
Jellyfin, Plex or Emby can read them whoever owns them. Ownership is set by running the container as
`PUID:PGID`; if they differ from the process user the app logs a warning and shows it per file.

In an **unprivileged LXC container** (Proxmox default), uid `N` inside the container is uid
`N + 100000` on the host. Files written as uid 1000 inside the LXC show up as uid 101000 on the host
and on NFS/SMB exports, and as "nobody" in other containers. Either map the uid through (`lxc.idmap`
plus `/etc/subuid`/`/etc/subgid`), or — when Docker runs on the host and your other apps run in
unprivileged LXCs — run media-ingest as `101000:101000` so those apps see the files as their uid 1000.
Use `stat -c '%u:%g %n' /srv/media/movies` to see what owns your library today.

## Health, logs and shutdown

- `GET /healthz` (no auth) returns `{"status":"ok"}` when the database and `MEDIA_ROOT` are usable; the
  image's `HEALTHCHECK` runs `media-ingest healthcheck`.
- Logs are JSON lines on stdout (`docker compose logs -f`). Set `LOG_LEVEL=debug` to see why a request
  was rejected (for example the client IP when `TRUSTED_PROXIES` is wrong).
- `SIGTERM` stops accepting requests, waits up to 30 s for running ones and pending Jellyfin
  notifications. Interrupted uploads resume from where they stopped.

## Reverse proxy

See [reverse-proxy.md](reverse-proxy.md) for Traefik + Authelia, Nginx and Caddy, including the
settings large uploads need.
