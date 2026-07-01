# Development

## Backend

Standard Go: `go build ./...`, `go test ./... -race`. The backend is CGO-free
(`CGO_ENABLED=0`).

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
