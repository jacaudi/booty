# Phase 10: Publish workflows, Taskfile, lint/vuln tasks, Dockerfile (items 11, 13) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:**
- The broken publish workflows are gone.
- `taskfile.yml` replaces the `Makefile`, including `ipxe`, `lint` and `vuln` tasks.
- The Dockerfile builds reproducibly onto `gcr.io/distroless/static-debian13`.
- The docs say `task …` instead of `make …`.

**Architecture:** Configuration files only: `taskfile.yml`, `.golangci.yml`, `Dockerfile`, plus doc edits.

**Tech Stack:** go-task v3.54.0, golangci-lint v2.14.0, govulncheck v1.9.0 (all verified in `research/tooling-qemu.md`).

**Spec:** design items 11 and 13, decisions §6.7 (amended by §8.10) and §8.7.

## Global Constraints

See `README.md`. Phase 7 must be done first: go-task v3.54.0 needs go ≥ 1.26.4 (`research/tooling-qemu.md` §1), and phase 7 moves the toolchain to go1.26.9.

## Review Focus

1. `task build` on a fresh clone must work without Docker: the iPXE binaries are committed (phase 5), so `ipxe` is not a dependency of `build`. Task 2 check.
2. `task lint` exits non-zero today (132 known findings). It must not be a dependency of `test` or `build`. Task 2 check.
3. `docker build` must still produce a binary for each `TARGETARCH`; the no-default `TARGETOS`/`TARGETARCH` comment and behaviour stay. Task 3 check.
4. A precondition inside a task with `dir: web` runs inside `web/` (verified go-task behaviour), so paths in preconditions are relative to that dir. Task 2 content.
5. The README Quickstart must not promise a published image that no workflow builds any more. Task 4.

---

### Task 1: Remove the publish workflows (item 11)

- [ ] **Step 1:** `git rm .github/workflows/docker-publish.yml .github/workflows/image.yml`
- [ ] **Step 2:** `ls .github/workflows`. Expected: `go-test.yml  web-test.yml`.
- [ ] **Step 3:** Commit: `git commit -m "ci: remove the publish workflows (actions pinned to @main); CI publishing will be rebuilt"`.

### Task 2: `taskfile.yml`, `.golangci.yml`, delete `Makefile`

**Files:** Create `taskfile.yml`, `.golangci.yml`; Delete `Makefile`.

- [ ] **Step 1: Install the tools.** Every later step in this phase assumes `$HOME/go/bin` is on `PATH`.

```bash
export PATH=$HOME/go/bin:$PATH
GOBIN=$HOME/go/bin go install github.com/go-task/task/v3/cmd/task@v3.54.0
V=2.14.0
TMP=$(mktemp -d) && cd "$TMP"
curl -sSfLO https://github.com/golangci/golangci-lint/releases/download/v$V/golangci-lint-$V-linux-amd64.tar.gz
curl -sSfLO https://github.com/golangci/golangci-lint/releases/download/v$V/golangci-lint-$V-checksums.txt
grep "golangci-lint-$V-linux-amd64.tar.gz" golangci-lint-$V-checksums.txt | sha256sum -c
tar xzf golangci-lint-$V-linux-amd64.tar.gz && mv golangci-lint-$V-linux-amd64/golangci-lint $HOME/go/bin/
cd - >/dev/null && rm -rf "$TMP"
task --version && golangci-lint version
```

Expected:
- `sha256sum -c` prints `golangci-lint-2.14.0-linux-amd64.tar.gz: OK`;
- `task --version` prints `3.54.0`;
- `golangci-lint version` prints `golangci-lint has version 2.14.0 …`.

The binary install follows golangci-lint's docs, which advise against `go install` (`install/local.md:116-118`). govulncheck was installed in phase 7.
- [ ] **Step 1b: Make the tree gofmt-clean**, since `task fmt` gates the exit check. Eight files were not gofmt-clean at `f36482c`: `pkg/cache/debiandvd_test.go`, `pkg/cache/list.go`, `pkg/db/db.go`, `pkg/db/host.go`, `pkg/hardware/mac.go`, `pkg/http/api_cache.go`, `pkg/http/api_targets_test.go` and `pkg/tftp/menu_test.go`. Run `gofmt -l $(git ls-files '*.go')`, then `gofmt -w` on exactly the files it lists, then `go build ./... && go test ./...`. Commit with `git commit -am "style: gofmt"`. Expected afterwards: `gofmt -l $(git ls-files '*.go')` prints nothing.
- [ ] **Step 2: Create `.golangci.yml`** containing exactly:

```yaml
version: "2"
```

  Then run `golangci-lint config verify`. Expected: exit 0.
- [ ] **Step 3: Create `taskfile.yml`** with exactly this content:

```yaml
version: '3'

vars:
  VERSION:
    sh: git describe --tags --always --dirty 2>/dev/null || echo dev
  TIMESTAMP:
    sh: git log -1 --format=%cI 2>/dev/null || echo unknown
  IMAGE: booty:dev
  PLATFORMS: linux/amd64,linux/arm64

tasks:
  web:
    desc: Build the web UI into web/dist (embedded by the Go build)
    dir: web
    cmds:
      - npm ci
      - npm run build

  build:
    desc: Build bin/booty (static, stripped, version-stamped)
    deps: [web]
    env:
      CGO_ENABLED: '0'
    cmds:
      - go build -trimpath -ldflags "-s -w -X main.version={{.VERSION}} -X main.timestamp={{.TIMESTAMP}}" -o bin/booty ./cmd

  run:
    desc: Build and run against ./data
    deps: [build]
    cmds:
      - bin/booty --dataDir=data/

  test:
    desc: Go tests (race) and web tests
    cmds:
      - go test -race ./...
      - task: test:web

  test:web:
    dir: web
    cmds:
      - npm ci
      - npx tsc -b
      - npm test

  vet:
    desc: go vet
    cmds:
      - go vet ./...

  fmt:
    desc: Fail if any Go file is not gofmt-formatted
    cmds:
      - test -z "$(gofmt -l $(git ls-files '*.go'))"

  lint:
    desc: golangci-lint (findings are known and not yet fixed; see docs/development.md)
    preconditions:
      - sh: command -v golangci-lint
        msg: "install golangci-lint v2.14.0 (see docs/development.md)"
    cmds:
      - golangci-lint run ./...

  vuln:
    desc: govulncheck against the repo's toolchain
    preconditions:
      - sh: command -v govulncheck
        msg: "install govulncheck: GOBIN=$HOME/go/bin go install golang.org/x/vuln/cmd/govulncheck@v1.9.0"
    cmds:
      - govulncheck ./...

  ipxe:
    desc: Rebuild the embedded iPXE binaries in boot/ (container, or IPXE_NATIVE=1)
    cmds:
      - go generate ./boot/

  check:ipxe:
    desc: Verify boot/ binaries against SHA256SUMS
    cmds:
      - go test ./boot/

  clean:
    desc: Remove build outputs
    cmds:
      - rm -rf bin web/dist/assets web/dist/index.html

  image:
    desc: Build the container image
    cmds:
      - docker build --build-arg BOOTY_VERSION={{.VERSION}} --build-arg BOOTY_TIMESTAMP={{.TIMESTAMP}} -t {{.IMAGE}} .

  image:push:
    deps: [image]
    cmds:
      - docker push {{.IMAGE}}

  image:buildx:
    desc: Multi-arch image build
    cmds:
      - docker buildx build --platform {{.PLATFORMS}} --build-arg BOOTY_VERSION={{.VERSION}} --build-arg BOOTY_TIMESTAMP={{.TIMESTAMP}} -t {{.IMAGE}} .

  image:run:
    desc: Run the image with the least privileges booty needs (TFTP/proxyDHCP ports, ARP fallback)
    deps: [image]
    cmds:
      - docker run -ti --rm --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW -v {{.ROOT_DIR}}/data:/data -p 8080:8080 -p 69:69/udp {{.IMAGE}} --debug=true --dataDir=/data
```

  `clean` deletes only build outputs and keeps `web/dist/.gitkeep`, which `//go:embed all:dist` needs (`docs/development.md`). `{{.ROOT_DIR}}` is a go-task built-in variable.
- [ ] **Step 4: Verify.**
  - `task --list` lists every task with a `desc`.
  - `task fmt && task vet && task build` succeed, and `bin/booty --help` prints usage.
  - `task check:ipxe` passes, if phase 5 is done.
  - `task lint` exits 1 with errcheck/staticcheck findings. Record the summary line (`N issues`) in the commit message.
- [ ] **Step 5:** `git rm Makefile`.
- [ ] **Step 6:** Commit: `git add taskfile.yml .golangci.yml && git commit -m "build: taskfile.yml replaces the Makefile; add lint and vuln tasks (lint findings recorded, not fixed: <N> issues)"`.

### Task 3: Dockerfile

**Files:** Modify `Dockerfile`.

- [ ] **Step 1:** Replace the Dockerfile with exactly this. It keeps the existing comments' intent: `TARGETOS`/`TARGETARCH` have no defaults, and web is built before Go.

```dockerfile
### Stage One: build the web UI (feeds the Go embed below)
FROM node:lts-alpine AS build-web
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
      org.opencontainers.image.licenses="<built from boot/LICENCE.txt, see below>"

COPY --from=build-golang /app/bin/booty /

EXPOSE 8080/tcp 69/udp
ENTRYPOINT [ "/booty" ]
```

  The licence label: booty is MIT, and the embedded iPXE binaries carry iPXE's licence. **Before committing, build the label from `boot/LICENCE.txt` (phase 5).** Map each `The overall licence for this file is: …` line:

  | text in `LICENCE.txt` | SPDX id |
  |---|---|
  | `GPL version 2 only` | `GPL-2.0-only` |
  | `GPL version 2 (or, at your option, any later version)` | `GPL-2.0-or-later` |

  The label is `MIT AND ` followed by the distinct ids, **sorted alphabetically**, joined with ` AND ` (for example `MIT AND GPL-2.0-only AND GPL-2.0-or-later`). Replace the placeholder text in the `LABEL` line with it. If `LICENCE.txt` contains any other licence text, stop and report it; do not invent an SPDX id. If `boot/LICENCE.txt` does not exist yet, finish phase 5 first.
- [ ] **Step 2: Verify** (requires Docker; if Docker is not available, start `dockerd` as phase 5 does, or report that this step could not run):
  - `task image` succeeds.
  - `docker run --rm booty:dev --help` prints usage.
  - `docker inspect booty:dev --format '{{.Config.User}}|{{json .Config.ExposedPorts}}'` prints `0|{"69/udp":{},"8080/tcp":{}}`. `static-debian13:latest` sets `User` to `0`, root (`research/tooling-qemu.md` §4).
- [ ] **Step 3:** Commit: `git add Dockerfile && git commit -m "build(docker): npm ci, cached module layer, stripped binary, OCI labels, distroless static-debian13"`.

### Task 4: Docs

- [ ] **Step 1:** README section `## Building from source`:
  - replace the `make` block with `task build`, `task run`, `task image` and `task image:buildx`, each with the same comments;
  - add the line `Install go-task: go install github.com/go-task/task/v3/cmd/task@v3.54.0`;
  - change "distroless image" to "distroless static image".
- [ ] **Step 2:** In the README Quickstart, after the `docker run … ghcr.io/jacaudi/booty:latest` block, add: `> Images are not currently published by CI; build one locally with task image and use booty:dev.`
- [ ] **Step 3:** `docs/development.md`:
  - add a "Tasks" section listing `task build|run|test|vet|fmt|lint|vuln|ipxe|check:ipxe|image`;
  - add the golangci-lint install commands from Task 2 Step 1;
  - add a "Known findings" paragraph: `golangci-lint (default linters) reports <N> issues, mostly errcheck; govulncheck reports none called after the go1.26.9 bump. Lint findings are not yet fixed.`
- [ ] **Step 4:** `docs/standards.md`:
  - §1.1: replace the line `- Go version: _TBD_` with `- Go version: go1.26.9 toolchain (go.mod)`;
  - change `base-debian12` to `static-debian13` and `make docker-buildx` to `task image:buildx`;
  - lines 254-255: append the sentence `2026-10-11: moved to gcr.io/distroless/static-debian13 (debian12 images are no longer rebuilt upstream).`
- [ ] **Step 5:** Commit: `git add README.md docs && git commit -m "docs: task-based build, lint/vuln, static-debian13"`.

### Phase exit check

`task fmt && task vet && task test && task build` all succeed.
