# Episode number rules

The mapping screen suggests an episode for every file from its name. The rules are regular expressions
served by the server and applied in the browser, in this order:

1. **Custom rules** pasted in Settings → Episode number rules.
2. **Enabled rule packs**: built-in [Turkish](../internal/parser/packs/tr.yaml),
   [German](../internal/parser/packs/de.yaml) and [Spanish](../internal/parser/packs/es.yaml), plus files
   in `<DATA_DIR>/rules/*.yaml`. A file named like a built-in pack (`de.yaml`) replaces it.
3. **Built-in rules** ([default.json](../internal/parser/default.json)): `S01E03`, `S01E03-E04`,
   `S01E01-02`, `1x03`, `Season 1 Episode 3`, `Episode 3` / `Ep.03` / `E03` / `Part 3`, `Show - 03`,
   `03 - Title`, `Title 03`.

The first episode rule that matches wins. Before matching, **junk** patterns (resolutions, codecs,
years, release tags, language tags) are removed from the name. When a rule finds no season, the season
comes from the file name (`Season 2 - 05`), then from parent folders (`Season 2/`, `S02/`, `Specials/`,
or a pack's forms such as `Staffel 2/`), then from the only selected season. If no video file parses at
all, files are assigned in natural order from the start episode you choose.

## File format

```yaml
name: Italiano                  # optional, shown in Settings
description: Stagione / Episodio
episode:                        # must capture "episode"; may capture "season" and "episodeEnd"
  - name: Stagione 1 Episodio 3
    pattern: 'stagione[ ._-]*(?<season>\d{1,3})[ ._-]*episodio[ ._-]*(?<episode>\d{1,4})'
  - name: Puntata 3
    pattern: '(?:^|[^a-z0-9])puntata[ ._-]*(?<episode>\d{1,4})(?:[^0-9]|$)'
season:                         # must capture "season", or "specials" for Season 0
  - name: Stagione 1
    pattern: '(?:^|[^a-z0-9])stagione[ ._-]*(?<season>\d{1,3})(?:[^0-9]|$)'
  - name: Speciali
    pattern: '^(?<specials>speciali)$'
junk:                           # removed before matching
  - '(?:^|[^a-z0-9])(?:ita|italiano|sub ita)(?:[^a-z0-9]|$)'
```

Rules:

- Patterns are matched **case-insensitively** and must work in both Go's RE2 and JavaScript: named
  groups `(?<name>…)`, character classes, alternation, quantifiers. **No lookarounds and no
  backreferences** — the server rejects them, which also guarantees linear-time matching.
- Use `(?:^|[^a-z0-9])` and `(?:[^0-9]|$)` instead of `\b` or lookarounds to anchor numbers.
- Write letters whose case folding differs between engines as classes, e.g. Turkish `[iıİ]`.
- Unknown keys are rejected; a file may be at most 64 KB.

Check what the server will use:

```sh
docker exec media-ingest media-ingest rules --dir /data/rules | less
```

## Contributing a pack

Add `internal/parser/packs/<language>.yaml` and a block of real-world names to
[web/test/parser.test.mjs](../web/test/parser.test.mjs) (`localized` table). The tests also re-run every
English case with all packs enabled, so a new pack cannot break existing names. See
[CONTRIBUTING.md](../CONTRIBUTING.md).
