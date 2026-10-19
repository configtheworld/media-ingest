# Naming

All names are built on the server from provider metadata (TMDB id, title, year, season and episode
numbers, episode titles) and the file extension. File names sent by the browser are only used for the
extension and for display. Quick upload is the exception: it keeps the original names, sanitized.

## Presets

Choose in **Settings → Naming**. The extension is always appended automatically.

| Preset | Movie | TV episode |
|---|---|---|
| Jellyfin (default) | `{title} ({year}) [tmdbid-{id}]/{title} ({year}) [tmdbid-{id}]` | `{show} ({year}) [tmdbid-{id}]/Season {season:02}/{show} - S{season:02}E{episode:02} - {episode_title}` |
| Plex | `{title} ({year}) {tmdb-{id}}/…` | `{show} ({year}) {tmdb-{id}}/Season {season:02}/{show} ({year}) - s{season:02}e{episode:02} - {episode_title}` |
| Emby | `{title} ({year}) [tmdbid={id}]/{title} ({year})` | `{show} ({year}) [tmdbid={id}]/Season {season:02}/{show} - S{season:02}E{episode:02} - {episode_title}` |

Results, Jellyfin preset:

```
movies/The Clockwork Garden (2015) [tmdbid-900001]/The Clockwork Garden (2015) [tmdbid-900001].mkv
movies/The Clockwork Garden (2015) [tmdbid-900001]/The Clockwork Garden (2015) [tmdbid-900001] - 2160p.mkv     (version)
movies/The Clockwork Garden (2015) [tmdbid-900001]/The Clockwork Garden (2015) [tmdbid-900001].en.forced.srt   (subtitle)
movies/The Clockwork Garden (2015) [tmdbid-900001]/Trailers/Official Trailer.mp4                     (extra)
tv/Harbor Lights (2019) [tmdbid-900100]/Season 01/Harbor Lights - S01E02 - Fog.mkv
tv/Harbor Lights (2019) [tmdbid-900100]/Season 01/Harbor Lights - S01E03-E04 - Undertow & Low Tide.mkv
tv/Harbor Lights (2019) [tmdbid-900100]/Season 00/Harbor Lights - S00E01 - Behind the Harbor.mkv                          (special)
```

## Template syntax

| Syntax | Meaning |
|---|---|
| `{name}` | variable |
| `{name:02}` | integer, zero-padded to 2 digits |
| `<…>` | optional: left out when a variable inside is empty, e.g. `< ({year})>` |
| `{{` `}}` | literal braces (Plex ids) |
| `/` | folder separator |

`<` and `>` cannot appear in Windows file names, so they never clash with literal text; square brackets
are literal (`[tmdbid-{id}]`). A trailing `.{ext}` is accepted and ignored.

Variables — movies: `title`, `original_title`, `year`, `id`, `provider`. TV: `show`, `original_show`,
`year`, `id`, `provider`, `season`, `episode`, `episode_end`, `episode_title`, `air_date`.

## Versions, extras, subtitles

- **Movie versions**: `<name> - <label>.<ext>` (Jellyfin's multi-version convention).
- **Alternate episode versions**: two files for one episode need different labels, because Jellyfin
  merges them into one item: `… - S01E02 - Title - 1080p.mkv`.
- **Multi-episode files**: `S01E03-E04`, titles joined with ` & `.
- **Extras** go into the preset's folders: `Behind The Scenes`, `Deleted Scenes`, `Featurettes`,
  `Interviews`, `Scenes`, `Shorts`, `Trailers`, and `Extras` (Jellyfin) / `Other` (Plex, Emby).
- **Subtitles**: `<video name>.<lang>[.forced][.sdh].<ext>`. For an episode that already exists in the
  library under a different name, the subtitle takes *that* file's name, so the media server pairs them.

## Existing folders

- A folder that already carries the same id (`[tmdbid-N]`, `{tmdb-N}` or `[tmdbid=N]`) is reused.
- Folders whose title matches but carry no id are offered as a choice, never used silently.
- Reuse works when the first template segment is the per-title folder.

## Sanitizing

Applied to every name segment: NFC normalization; control, bidi and zero-width characters removed;
`:` becomes ` - `, `/` `\` and look-alikes become `-`, `?*<>` are removed, `"` becomes `'`; leading dots
and trailing dots/spaces are removed; Windows reserved names (`CON`, `NUL`, `COM1`, …) get `_`; each
segment is at most 255 bytes (long titles are shortened, never the id).

## Conflicts

Nothing is overwritten silently. For every destination that exists — or episode that exists under
another name — you choose **skip**, **replace** (the old file goes to `.trash`, undo restores it) or
**keep both** (`… - Version 2`).
