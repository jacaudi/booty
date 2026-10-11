### Stage One: build the web UI (feeds the Go embed below)
# The UI assets are architecture-neutral; build them natively so arm64 images build without emulation.
FROM --platform=$BUILDPLATFORM node:lts-alpine AS build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

### Stage Two: build the Go binary (embeds the web assets and boot/ iPXE files)
FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS build-golang

# Cross-compilation targets. BuildKit/buildx auto-populates TARGETOS/TARGETARCH
# per target platform. Do NOT default these: a default (e.g. amd64) overrides
# buildx's per-platform value, compiling an amd64 binary into the arm64 image.
ARG TARGETOS
ARG TARGETARCH

ARG BOOTY_VERSION
ARG BOOTY_TIMESTAMP

WORKDIR /app

# Module downloads in their own layer so source edits do not refetch them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Bring the built UI in BEFORE `go build` so //go:embed all:dist embeds it.
COPY --from=build-web /app/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${BOOTY_VERSION} -X main.timestamp=${BOOTY_TIMESTAMP}" \
    -o bin/booty ./cmd

### Final stage: static distroless (booty is pure Go; no libc needed)
FROM gcr.io/distroless/static-debian13

ARG BOOTY_VERSION
ARG BOOTY_TIMESTAMP
LABEL org.opencontainers.image.source="https://github.com/jacaudi/booty" \
      org.opencontainers.image.version="${BOOTY_VERSION}" \
      org.opencontainers.image.created="${BOOTY_TIMESTAMP}" \
      org.opencontainers.image.licenses="MIT AND GPL-2.0-only AND GPL-2.0-or-later"

COPY --from=build-golang /app/bin/booty /

EXPOSE 8080/tcp 69/udp
ENTRYPOINT [ "/booty" ]
