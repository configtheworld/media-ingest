# Security policy

media-ingest writes to your media library, so authentication and path handling are treated as
security-critical.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately through GitHub:
[Security → Report a vulnerability](https://github.com/configtheworld/media-digest/security/advisories/new).

Include the version (`media-ingest version` or the footer of the web UI), your `AUTH_MODE`, the reverse
proxy you use and steps to reproduce. You will get an answer within a week. Fixes are released as a
patch version and credited in the advisory unless you prefer otherwise.

## Supported versions

Only the latest minor release receives security fixes. The project is pre-1.0; breaking changes are
listed in the [changelog](CHANGELOG.md).

## Security model

What the app trusts, and how it is enforced. Each point is covered by automated tests (Go tests in
`internal/`, browser tests in `e2e/`), except the container hardening, which comes from the image and
the provided compose file.

**Authentication** (`AUTH_MODE`)
- `proxy` (default): the user comes from `Remote-User`, accepted **only** from `TRUSTED_PROXIES` and
  only as a single value. Missing configuration stops the server; missing or repeated headers, or other
  source addresses, get `401` — including the static files.
- `basic`: argon2id password hash from the environment (there is no "first visitor creates the
  account" flow), server-side sessions, `HttpOnly` + `SameSite=Lax` cookie, login rate limit.
- `none`: only with `AUTH_NONE_CONFIRM=true`.
- Every state-changing request needs a CSRF token (double submit) and same-origin Fetch Metadata /
  `Origin`, in all modes — a proxy session cookie is ambient too.

**Files**
- All file access goes through Go's `os.Root` opened at `MEDIA_ROOT`: `..`, absolute paths and symlinks
  pointing outside are rejected by the runtime. Paths are additionally validated (no empty, `.` or `..`
  segments, no NUL bytes, valid UTF-8).
- Library paths are built on the server from provider metadata. File names from the browser are used for
  the extension (checked against an allow-list) and display only; names are sanitized (control, bidi and
  zero-width characters, slash look-alikes, Windows reserved names, 255-byte limit). Quick upload keeps
  names but sanitizes every segment.
- Publishing never overwrites: `link(2)` fails atomically if the destination exists. "Replace" and
  "undo" move files to `.trash`; the only deletions are of the app's own staging files and of trash
  entries older than `TRASH_RETENTION_DAYS`.
- Uploads must match a planned item of the same user, with the planned size; bodies are bounded.

**Other**
- Parser rules (including user-supplied YAML) are validated with Go's RE2 engine; lookarounds and
  backreferences are rejected, so matching is linear-time in the browser too.
- The TMDB key never reaches browsers; posters are proxied with strictly validated paths. Errors and logs
  never contain keys, passwords, cookies or headers.
- No shell is ever executed. Strict JSON decoding (unknown fields rejected, size limits) on every API
  payload. Per-IP rate limits. Security headers: CSP without inline scripts or styles, `nosniff`,
  `frame-ancestors 'none'`, `Referrer-Policy: no-referrer`, `COOP`.
- The container runs as non-root, read-only, with all capabilities dropped.

## Your part

- Make sure **only the reverse proxy can reach the app's port**, and that the proxy overwrites
  `Remote-User`. If `TRUSTED_PROXIES` is a NAT/VPN address, everything behind it can claim to be any user.
- Keep `AUTH_MODE=none` off any network other people can reach.
- Treat `/data` (database, API key file) as sensitive; it is created with restrictive permissions.
