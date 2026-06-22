### Stage One
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS build-golang

# Cross-compilation targets. buildx populates TARGETOS/TARGETARCH per target
# platform; the defaults keep them non-empty for a plain `docker build`.
ARG TARGETOS=linux
ARG TARGETARCH=amd64

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
