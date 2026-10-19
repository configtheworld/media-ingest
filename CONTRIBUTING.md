# Contributing

Thanks for helping! Bug reports, rule packs for more languages, translations and fixes are all welcome.
By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Security issues go through the
[security policy](SECURITY.md), not public issues.

## Development setup

You need **Go** (the toolchain in `go.mod` is downloaded automatically) and **Node.js 22** (only for the
tests; the app has no npm dependencies). Docker is optional.

```sh
git clone https://github.com/configtheworld/media-digest.git && cd media-digest
npm ci && npx playwright install chromium
make test          # Go tests with the race detector + JS unit tests
make e2e           # browser tests (phone and desktop viewports) against a fake TMDB
make lint          # gofmt, go vet, golangci-lint, JS syntax check
make run           # run locally on http://127.0.0.1:8097 with AUTH_MODE=none (needs TMDB_API_KEY)
```

See [docs/architecture.md](docs/architecture.md) for how the pieces fit together.

## Guidelines

- **Keep it generic.** No hard-coded languages, show names or paths; anything user-facing goes through
  `web/static/i18n/*.json`, anything deployment-specific through environment variables.
- **Never trust the browser** for paths, names or sizes. The server derives paths from structured data.
- **Test what you change.** Go: table tests next to the code. Frontend logic lives in pure modules
  (`parser.js`, `tvmap.js`, `movie.js`) with `node --test` tests in `web/test/`. User flows: Playwright in
  `e2e/`. CI runs everything, including an accessibility scan (WCAG 2.1 AA).
- **Small dependencies.** Prefer the standard library; new dependencies need a reason in the PR.
- Format Go with `gofmt`; keep the existing style (comments explain *why*, not *what*).
- Commit messages: imperative mood, short subject (`Add Italian rule pack`), details in the body.

## Adding a translation

1. Copy `web/static/i18n/en.json` to `<code>.json` (ISO 639-1, e.g. `fr.json`) and translate the values.
   Keep every `{placeholder}` exactly as in English.
2. Add the language to `UI_LANGUAGES` in `web/static/js/i18n.js` with its native name.
3. `npm run test:unit` checks that keys and placeholders match English.

## Adding an episode rule pack

1. Create `internal/parser/packs/<code>.yaml` (format: [docs/parser-rules.md](docs/parser-rules.md)).
2. Add real-world names to the `localized` table in `web/test/parser.test.mjs`.
3. `npm run test:unit` checks your names *and* re-runs every English case with all packs enabled.

## Pull requests

- One topic per PR; describe what changed and how you tested it.
- CI must be green (lint, tests, dependency scan, image build).
- Update [CHANGELOG.md](CHANGELOG.md) under *Unreleased* for user-visible changes.

## Releases (maintainers)

Versions follow [Semantic Versioning](https://semver.org/). To release: update `Version` in
`cmd/media-ingest/main.go` and the changelog, commit, then `git tag vX.Y.Z && git push --tags`. CI builds
and publishes `ghcr.io/configtheworld/media-digest:X.Y.Z`, `:X.Y` and `:latest`.
