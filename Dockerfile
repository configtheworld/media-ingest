# syntax=docker/dockerfile:1
# Multi-arch build: docker buildx build --platform linux/amd64,linux/arm64 .
# The builder runs natively and cross-compiles (pure Go, no cgo), so no QEMU is needed.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS build
ARG TARGETOS
ARG TARGETARCH
# Optional: overrides the version compiled into the binary (default: main.Version).
ARG VERSION=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w ${VERSION:+-X main.Version=$VERSION}" -o /out/media-ingest ./cmd/media-ingest

# distroless/static: CA certificates and tzdata, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-ingest /usr/local/bin/media-ingest
# Default non-root user; override with --user PUID:PGID to match your media files.
USER 65532:65532
ENV MEDIA_ROOT=/media DATA_DIR=/data LISTEN_ADDR=:8097
EXPOSE 8097
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/media-ingest", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/media-ingest"]
