### Stage One
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

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o bin/booty -ldflags "-X main.version=$BOOTY_VERSION -X main.timestamp=$BOOTY_TIMESTAMP" ./cmd


### Stage Two
FROM node:lts-alpine AS build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm install
COPY web/ .
RUN npm run build

### Final Stage
FROM gcr.io/distroless/base-debian12

COPY --from=build-golang /app/bin/booty /
COPY --from=build-web /app/dist/ /web/dist/

ENTRYPOINT [ "/booty" ]
