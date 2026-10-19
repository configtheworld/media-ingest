# Changelog

All notable changes to this project are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] - 2026-10-17

### Added
- Installable web app (PWA): manifest, icons (incl. maskable), service worker that keeps the app shell
  usable offline and never touches API calls or uploads.
- UI translations: German, Turkish, Spanish, with a language switcher (follows the browser by default).
- Documentation: configuration reference, deployment, reverse proxies (Traefik + Authelia, Nginx,
  Caddy, large uploads), naming, episode rules, troubleshooting, architecture; screenshots.
- Community files: security policy, contributing guide, code of conduct, issue and PR templates.
- CI for GitHub Actions and GitLab: gofmt, go vet, golangci-lint (incl. gosec), race-enabled tests,
  JavaScript unit tests, Playwright end-to-end and accessibility (WCAG 2.1 AA) tests, govulncheck and
  npm audit, multi-arch image build pushed to GHCR (`edge`, `X.Y.Z`, `X.Y`, `latest`). Dependabot.
- `Makefile` with the common development tasks.

### Changed
- Go module path is now `github.com/configtheworld/media-digest`.
- Go toolchain pinned to 1.26.9 (security fixes since 1.26.0).
- `JELLYFIN_PATH_PREFIX=/` is accepted (Jellyfin mounting `movies/` and `tv/` at the root).
- The web app manifest and icons are served without authentication (browsers fetch them without cookies).

### Fixed
- Link colour contrast below WCAG AA in the footer.

## [0.3.0] - 2026-10-17

### Added
- History of published batches with **Undo**: files move to the trash, replaced files are restored,
  emptied folders are removed; files changed after publishing are left alone.
- Trash retention (`TRASH_RETENTION_DAYS`, default 30; 0 keeps forever).
- Optional Jellyfin hook (`JELLYFIN_URL`, `JELLYFIN_API_KEY`, `JELLYFIN_PATH_PREFIX`):
  `POST /Library/Media/Updated` with fallback to `POST /Library/Refresh`; fails soft.
- Parser rule packs for Turkish, German and Spanish; custom rules and pack selection in Settings;
  drop-in packs in `<DATA_DIR>/rules/*.yaml`; `media-ingest rules` command.
- Quick upload: no metadata, choose a folder under `MEDIA_ROOT`, keep file names.

## [0.2.0] - 2026-10-17

### Added
- TV flow: season picker (Specials = Season 0), file-to-episode mapping with suggestions from
  file names, drag and drop (desktop), tap to assign (touch), swap, skip, next free episode,
  sequential fallback, multi-episode files, alternate versions.
- Library awareness: episodes already in the library are shown and need an explicit decision.
- Subtitles named after their episode, including episodes already in the library.

## [0.1.0] - 2026-10-16

### Added
- Authentication: reverse-proxy header (default, fail closed), built-in login (argon2id) or none.
- Movie flow: TMDB search, file roles (movie, versions, extras, subtitles), dry-run review,
  resumable tus uploads to a staging folder, atomic publish without overwriting, permissions.

[Unreleased]: https://github.com/configtheworld/media-digest/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/configtheworld/media-digest/releases/tag/v0.4.0
