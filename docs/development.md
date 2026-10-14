# Development

## Backend

Standard Go: `go build ./...`, `go test ./... -race`. The backend is CGO-free
(`CGO_ENABLED=0`).

## Tasks

Build tooling is [go-task](https://taskfile.dev) (`taskfile.yml`); `task --list` shows every task.
Install it with `GOBIN=$HOME/go/bin go install github.com/go-task/task/v3/cmd/task@v3.54.0`.

| Task | Does |
|---|---|
| `task build` | build the web UI, then `bin/booty` (static, stripped, version-stamped) |
| `task run` | build and run against `./data` |
| `task test` | `go test -race ./...` and the web tests |
| `task vet` | `go vet ./...` |
| `task fmt` | fail if any Go file is not gofmt-formatted |
| `task lint` | golangci-lint with the go-tricorder plugin on the whole tree (exits non-zero today, see below) |
| `task lint:new` | the same on code changed since `origin/main` (must be clean) |
| `task vuln` | `govulncheck ./...` |
| `task ipxe` | rebuild the embedded iPXE binaries in `boot/` (container, or `IPXE_NATIVE=1`) |
| `task check:ipxe` | verify the `boot/` binaries against `SHA256SUMS` |
| `task image` | build the container image (`task image:buildx` for multi-arch) |
| `task image:smoke` | build the image for this machine and smoke-test it: binary arch matches the image, booty starts, `/healthz`, `/version.json` and `/ui/` answer |

`task build` does not depend on `ipxe`, so it works on a fresh clone without Docker.

### Linting (golangci-lint with go-tricorder)

`.golangci.yml` is the [go-tricorder](https://github.com/jacaudi/go-tricorder) baseline, which needs a golangci-lint built with the `tricorder` plugin; the stock binary fails with `plugin "tricorder" not found`. `task lint` and `task lint:new` build that binary as `bin/custom-gcl` (only when go-tricorder, `go.mod` or `.custom-gcl.yml` change) and run it. Point editors at `bin/custom-gcl` too.

go-tricorder is not published yet, so the build takes the plugin from a local checkout:

1. Clone or keep go-tricorder at `../go-tricorder` (next to this repo), or set `TRICORDER_DIR=/path/to/go-tricorder`. A missing directory fails the task with that hint.
2. Have a golangci-lint v2 binary on `PATH` (`golangci-lint custom` builds v2.14.0 plus the plugin; it clones golangci-lint, so the first build needs the network).

`task` renders `.custom-gcl.yml` (a template with an `@TRICORDER_DIR@` placeholder) to the gitignored `bin/custom-gcl-build/.custom-gcl.yml`. When go-tricorder has a tag, replace the template's `path:` line with `version: v0.1.0` and drop the `TRICORDER_DIR` variable, the `sed` step and the precondition from `taskfile.yml`.

golangci-lint's cache is keyed on package sources, not on the plugin binary: after rebuilding `bin/custom-gcl`, run `bin/custom-gcl cache clean` if results look stale.

Installing golangci-lint v2 (the binary release, because golangci-lint's docs advise against `go install`):

```bash
export PATH=$HOME/go/bin:$PATH
V=2.14.0
TMP=$(mktemp -d) && cd "$TMP"
curl -sSfLO https://github.com/golangci/golangci-lint/releases/download/v$V/golangci-lint-$V-linux-amd64.tar.gz
curl -sSfLO https://github.com/golangci/golangci-lint/releases/download/v$V/golangci-lint-$V-checksums.txt
grep "golangci-lint-$V-linux-amd64.tar.gz" golangci-lint-$V-checksums.txt | sha256sum -c
tar xzf golangci-lint-$V-linux-amd64.tar.gz && mv golangci-lint-$V-linux-amd64/golangci-lint $HOME/go/bin/
cd - >/dev/null && rm -rf "$TMP"
golangci-lint version
```

govulncheck: `GOBIN=$HOME/go/bin go install golang.org/x/vuln/cmd/govulncheck@v1.9.0`.

Rules about `//nolint`: name the linter and say why (`//nolint:<linter> // <why>`; for tricorder rules `//nolint:tricorder // <rule>: <why>`). "Ported verbatim" is not a reason. `.golangci.yml` carries a few scoped exclusions, each with its reason and, where one exists, the issue that removes it (#91 global viper, #94 `pkg/` layout; global-logger use and camelCase flags have no issue).

### Known findings

`task lint` (the go-tricorder baseline, `.golangci.yml`) reports 828 issues on the whole tree (tracked in #96; the config is uncapped, so `task lint` prints them all; largest groups: noctx 185, gocritic 105, errcheck 84, goimports 81, tricorder 64, sloglint 51, godoclint 51, revive 50). New and changed code must be clean (`task lint:new` reports 0 issues). govulncheck reports none called after the go1.26.9 bump.

## Web UI (`web/`)

The UI is a React 18 + Ant Design 5 app built with Vite (TypeScript). It is
**embedded into the Go binary** via `//go:embed all:dist` (`web/embed.go`) and
served under `/ui/`.

### Local development

```bash
cd web
npm install
npm run dev      # Vite dev server (proxy /api/v1 to a running booty as needed)
npm run test     # Vitest + React Testing Library
npm run build    # emits web/dist/ (gitignored)
```

### How embedding works

- `web/dist/` bundles are **gitignored**; only `web/dist/.gitkeep` is committed.
- `//go:embed all:dist` — the `all:` prefix is required so the committed
  `.gitkeep` satisfies the directive, letting `go build` / CI compile in a fresh
  checkout without running `npm run build` (the UI 404s until built).
- For a real binary, build the UI first: `cd web && npm run build`, then
  `go build ./cmd`. The Docker build does this automatically (the `build-web`
  stage feeds its `dist` into the Go build stage before `go build`).

### Serving model

`/` → 302 `/ui/`. The `/ui/` handler serves real embedded files directly;
extensionless paths fall back to `index.html` (client-side routing); a missing
path with a file extension returns 404.
