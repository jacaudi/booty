### Stage One: build the web UI (feeds the Go embed below)
FROM node:lts-alpine AS build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm install
COPY web/ .
RUN npm run build

### Stage Two: build the Go binary (embeds the web assets)
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS build-golang

# Cross-compilation targets. BuildKit/buildx auto-populates TARGETOS/TARGETARCH
# per target platform. Do NOT default these: a default (e.g. amd64) overrides
# buildx's per-platform value, compiling an amd64 binary into the arm64 image
# (arm64 base + amd64 /booty => "exec format error" on arm64).
ARG TARGETOS
ARG TARGETARCH

# Version stamping. Passed via --build-arg; default empty when omitted.
ARG BOOTY_VERSION
ARG BOOTY_TIMESTAMP

WORKDIR /app

COPY . .

# Bring the built UI in BEFORE `go build` so //go:embed all:dist embeds the real
# assets, not just the committed .gitkeep. Omitting this yields a green build
# that serves a 404 UI.
COPY --from=build-web /app/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o bin/booty -ldflags "-X main.version=$BOOTY_VERSION -X main.timestamp=$BOOTY_TIMESTAMP" ./cmd

### Final Stage
FROM gcr.io/distroless/base-debian12

COPY --from=build-golang /app/bin/booty /

ENTRYPOINT [ "/booty" ]
