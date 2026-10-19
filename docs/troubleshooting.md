# Troubleshooting

Logs are JSON lines: `docker compose logs -f media-ingest`. `LOG_LEVEL=debug` adds the reason for
rejected requests.

## "Not signed in" / every request is 401

- In proxy mode the request must come from `TRUSTED_PROXIES` and carry exactly one `Remote-User`.
  The `"ip"` field of each log line shows the address the app sees; put that address (or its subnet) in
  `TRUSTED_PROXIES`.
- Check that the proxy forwards the header: Traefik `authResponseHeaders`, Nginx
  `proxy_set_header Remote-User`, Caddy `copy_headers` (see [reverse-proxy.md](reverse-proxy.md)).

## The server refuses to start

The first log line names the problem, e.g. `AUTH_MODE=proxy requires TRUSTED_PROXIES` or
`MOVIES_DIR: must be a relative path inside MEDIA_ROOT`. See the [configuration reference](configuration.md).

## "staging directory and library are on different file systems"

`.incoming` and the libraries must be on one file system, because finished files are moved into place
with an atomic link/rename. Mount the parent folder that contains `movies/` and `tv/` as `MEDIA_ROOT`
instead of mounting each library separately.

## Published, but "ownership could not be set"

A non-root process can only chown to itself. Run the container as `PUID:PGID` (`user:` in compose,
`--user` for docker run). In unprivileged LXC containers see the
[uid-mapping pitfall](deployment.md#permissions-and-the-unprivileged-lxc-pitfall).

## Uploads stop at the same point or fail on slow connections

- Traefik v3 closes requests after 60 s by default (`readTimeout`); Nginx defaults `client_max_body_size`
  to 1 MB. Apply the [large-upload settings](reverse-proxy.md).
- Uploads resume automatically after network loss or when the phone wakes up. After a page reload, select
  the same files again; parts already uploaded are kept for `UPLOAD_EXPIRY_HOURS`.
- A proxy session that expires mid-upload makes requests fail with a redirect; reload the page, sign in,
  and select the files again to resume.

## "Free space" looks wrong

Docker Desktop on macOS/Windows reports a fake size for shared folders. On Linux the value is the free
space of the staging file system.

## Jellyfin is not notified

History shows the result per batch. `failed` usually means `JELLYFIN_URL` is not reachable *from the
container* (use the internal address, not the public one behind the auth proxy) or the API key is wrong:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: MediaBrowser Token="KEY"' http://jellyfin:8096/System/Info
```

If Jellyfin is notified but does not show new items, check `JELLYFIN_PATH_PREFIX` against the paths
Jellyfin uses (see the configuration reference).

## Episodes are suggested wrongly

Use **Suggest from file names again** after enabling rule packs, or **Assign in order** for names without
numbers. `media-ingest rules` prints the effective rules; [parser-rules.md](parser-rules.md) explains how to
add your own.

## The web app cannot be installed on the phone

Browsers fetch the manifest and icons without cookies. Add the Authelia bypass rule for
`/manifest.webmanifest` and `/icons/` from [reverse-proxy.md](reverse-proxy.md#authelia-access-rules).
