# Built with the BuildKit frontend that ships inside Docker, so building this
# image never has to fetch a separate frontend from Docker Hub first.

# ---------------------------------------------------------------- build ----
FROM golang:1.23-alpine AS build

WORKDIR /src

# Dependencies first, so a code-only change reuses the cached layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# CGO_ENABLED=0 gives a static binary: SQLite is the pure-Go modernc driver and
# the timezone database is embedded, so the runtime image needs nothing at all.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
	-o /out/dinners ./cmd/server

# An empty data directory owned by the runtime user, so the unprivileged
# process can create the SQLite file even on a fresh named volume.
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

# -------------------------------------------------------------- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Rudbeckia middagar" \
      org.opencontainers.image.description="Anmälan till kollektivhusets gemensamma middagar" \
      org.opencontainers.image.source="https://github.com/kollektivhuset-rudbeckia/dinner" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/dinners /dinners
# A default configuration so the image runs out of the box; mount your own over
# /config.yaml, or point CONFIG_PATH somewhere else.
COPY --from=build /src/config.yaml /config.yaml

# The database lives here. Mount a volume so registrations survive a redeploy.
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME ["/data"]

ENV CONFIG_PATH=/config.yaml \
    DB_PATH=/data/dinners.db \
    LISTEN_ADDR=:8080

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/dinners"]
