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
| `task lint` | golangci-lint on the whole tree (exits non-zero today, see below) |
| `task lint:new` | golangci-lint on code changed since `origin/main` (must be clean) |
| `task vuln` | `govulncheck ./...` |
| `task ipxe` | rebuild the embedded iPXE binaries in `boot/` (container, or `IPXE_NATIVE=1`) |
| `task check:ipxe` | verify the `boot/` binaries against `SHA256SUMS` |
| `task image` | build the container image (`task image:buildx` for multi-arch) |

`task build` does not depend on `ipxe`, so it works on a fresh clone without Docker.

### Installing golangci-lint v2.14.0

The binary release is used, because golangci-lint's docs advise against `go install`.

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

### Known findings

golangci-lint (golden config, `.golangci.yml`) reports 1,176 issues on the whole tree (`task lint` prints at most 50 per linter, so it shows 547; run with `--max-issues-per-linter=0 --max-same-issues=0` for the full count; largest groups: govet, revive, sloglint, goimports, goconst, golines, noctx); new and changed code must be clean (`task lint:new`). govulncheck reports none called after the go1.26.9 bump.

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
