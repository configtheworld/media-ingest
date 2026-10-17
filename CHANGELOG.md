# Changelog

All notable changes to this project are documented here. The project follows
[Semantic Versioning](https://semver.org/).

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
