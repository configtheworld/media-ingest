# Developer shortcuts. CI runs the same commands.
GOLANGCI_LINT_VERSION ?= v2.14.0
GOVULNCHECK_VERSION   ?= v1.8.0

.PHONY: all build run test test-go test-js e2e lint vuln image icons screenshots clean

all: lint test

build:
	go build -trimpath -o media-ingest ./cmd/media-ingest

run: build
	mkdir -p .dev/media .dev/data
	LISTEN_ADDR=127.0.0.1:8097 MEDIA_ROOT=$(CURDIR)/.dev/media DATA_DIR=$(CURDIR)/.dev/data \
	AUTH_MODE=none AUTH_NONE_CONFIRM=true COOKIE_SECURE=false ./media-ingest

test: test-go test-js

test-go:
	go test -race -count=1 ./...

test-js:
	npm run -s test:unit

e2e:
	npm run -s test:e2e

lint:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi
	go vet ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	npm run -s lint:js

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	npm audit --audit-level=high

image:
	docker build -t media-ingest:local .

icons:
	node scripts/render-icons.mjs

screenshots:
	npx playwright test --project=docs

clean:
	rm -rf media-ingest .dev e2e/.tmp test-results playwright-report
