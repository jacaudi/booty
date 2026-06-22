# Booty Coding Standards

**Status:** placeholder. Sections below are TBD pending the stack decisions the user will provide.

This document describes the coding standards, conventions, and toolchain for both the Go backend (`pkg/`, `cmd/`) and the JavaScript/TypeScript frontend (`web/`).

---

## 1. Go (backend)

### 1.1 Toolchain

- Go version: _TBD_
- Module path: _TBD_
- Build flags: _TBD (e.g. `CGO_ENABLED=0`, `-trimpath`, `-ldflags`)_

### 1.2 Project layout

- _TBD: which layout convention (`pkg/`, `internal/`, flat, Standard Go Project Layout, etc.)_
- _TBD: where do `cmd/` binaries live, where do shared types live_

### 1.3 Dependencies

- Module hygiene: _TBD (e.g. `go mod tidy` requirements, vendoring, replace directives policy)_
- Approved libraries: _TBD_
- Forbidden / discouraged libraries: _TBD_

### 1.4 Formatting & linting

- Formatter: _TBD (gofmt / gofumpt / goimports)_
- Linter: _TBD (golangci-lint config)_
- Pre-commit / CI integration: _TBD_

### 1.5 Naming

- Packages: _TBD_
- Types / interfaces: _TBD_
- Functions / methods: _TBD_
- Receivers: _TBD_
- Errors / sentinel values: _TBD_

### 1.6 Error handling

- Error wrapping: _TBD (`fmt.Errorf("%w", ...)`, `errors.Is/As`)_
- Sentinel errors: _TBD_
- Panic policy: _TBD_
- Logging vs returning: _TBD_

### 1.7 Logging

- Library: **`log/slog`** (Go standard library structured logging). No third-party logger.
- Setup: a single process-wide default logger is installed once in `cmd` via
  `slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, ...)))`. One handler,
  writing to stderr (matching the prior std `log` destination). No format knob.
- Levels and when to use each:
  - `slog.Debug` — verbose/diagnostic output (per-request ARP/MAC details, RRQ
    info, "checking remote version"). Emitted unconditionally; the level gate
    suppresses it when not in debug mode.
  - `slog.Info` — normal lifecycle and progress (startup, server started/stopped,
    cron scheduled, download progress, version set, bytes sent, access log).
  - `slog.Warn` — recoverable problems that are logged-and-continue (download
    failures, ARP failures, cache misses, parse errors that fall through to a
    default, skipped invalid records).
  - `slog.Error` — genuine failures, including the path before returning an HTTP
    5xx. Fatal conditions use `slog.Error(...)` immediately followed by
    `os.Exit(1)` (slog has no `Fatal`; this preserves the prior `log.Fatal`
    termination semantics).
- Level control: driven solely by the existing `debug` flag (viper
  `config.Debug`). Debug → `slog.LevelDebug`; otherwise `slog.LevelInfo`. The
  flag is the level, not an `if` guard around each verbose call. No separate
  log-level config knob is introduced.
- Structured fields conventions: prefer key/value attrs over fmt-interpolated
  messages where natural — e.g. `slog.Info("download complete", "url", u,
  "bytes", n)`. Common keys: `err`, `mac`, `url`, `file`, `image`, `path`,
  `version`, `bytes`.
- Context propagation: _TBD_ (no `slog.*Context` call sites yet; revisit when
  request-scoped context carries trace/correlation data).

### 1.8 Concurrency

- Context propagation: _TBD_
- Goroutine lifecycle: _TBD_
- Mutex policy: _TBD_
- Channels vs sync primitives: _TBD_
- Race-free patterns required: _TBD_

### 1.9 Testing

- Framework: _TBD (stdlib `testing` / testify / ginkgo)_
- Coverage targets: _TBD_
- Table-driven tests: _TBD_
- Fixtures and golden files: _TBD_
- Integration vs unit boundaries: _TBD_
- `-race` requirement: _TBD_

### 1.10 HTTP / API

- Framework: _TBD (Huma is the planned choice per PR 8)_
- Handler shape: _TBD (typed input/output via Huma generics)_
- Validation: _TBD_
- Error response format: _TBD_
- OpenAPI spec discipline: _TBD_

### 1.11 Database / persistence

- Driver: _TBD (modernc.org/sqlite per PR 7)_
- Query style: _TBD (`database/sql` / sqlx / sqlc / GORM)_
- Migrations: _TBD_
- Transaction scoping: _TBD_

### 1.12 Configuration

- Library: _TBD (viper / koanf / kong / stdlib flag)_
- File formats supported: _TBD_
- Environment variable conventions: _TBD_
- CLI flag conventions: _TBD_

### 1.13 Documentation

- GoDoc requirements: _TBD_
- Example code: _TBD_
- README expectations: _TBD_

---

## 2. JavaScript / TypeScript (frontend)

### 2.1 Toolchain

- Runtime / package manager: _TBD (Node version, npm / pnpm / yarn / bun)_
- Build tool: _TBD (Vite / esbuild / webpack)_
- TypeScript version: _TBD_

### 2.2 Framework

- UI framework: _TBD (Vue / React / Svelte)_
- Component library: **Ant Design** (locked in per project rule — no custom components without explicit approval)
- Routing: _TBD_
- State management: _TBD (Pinia / Redux / Zustand / Jotai / Vue composables)_

### 2.3 TypeScript configuration

- Strictness: _TBD (`strict`, `noImplicitAny`, `strictNullChecks`, etc.)_
- Module resolution: _TBD_
- Path aliases: _TBD_
- Type-only imports: _TBD_

### 2.4 Code style

- Formatter: _TBD (Prettier config)_
- Linter: _TBD (ESLint config + plugins)_
- Sort order for imports: _TBD_
- Trailing commas / semicolons: _TBD_

### 2.5 Naming

- Components: _TBD_
- Files: _TBD_
- Types / interfaces: _TBD_
- Constants: _TBD_

### 2.6 API client

- Generation: _TBD (openapi-typescript / openapi-fetch / orval / hand-written)_
- Source: Huma-emitted OpenAPI spec at `/api/openapi.json`
- Auth header injection: _TBD_
- Error handling: _TBD_

### 2.7 State management

- Server state: _TBD (TanStack Query / SWR / native)_
- Client state: _TBD_
- Form state: _TBD_
- Cache invalidation policy: _TBD_

### 2.8 Component patterns

- File-per-component vs co-located: _TBD_
- Props typing: _TBD_
- Composition / extraction guidelines: _TBD_
- Event handling conventions: _TBD_

### 2.9 Testing

- Unit test framework: _TBD (Vitest / Jest)_
- Component test framework: _TBD_
- E2E framework: _TBD (Playwright / Cypress)_
- Coverage targets: _TBD_

### 2.10 Accessibility

- WCAG target level: _TBD_
- Required practices: _TBD_
- Tooling: _TBD_

### 2.11 Build & deploy

- Output target: embedded into Go binary via `go:embed` (per existing pattern)
- Asset path: _TBD_
- Source map policy: _TBD_

---

## 3. Cross-cutting

### 3.1 Git

- Branch naming: _TBD_
- Commit message format: _TBD_
- PR title format: _TBD_
- PR body template: _TBD_

### 3.2 Versioning

- Scheme: _TBD (semver / calver)_
- Changelog format: _TBD_

### 3.3 Docker / containers

- Base images: _TBD_
- Build stages: _TBD_
- Image scanning / signing: _TBD_

### 3.4 CI

- Required checks: _TBD_
- Artifact retention: _TBD_

### 3.5 Documentation

- Where docs live: _TBD_
- Markdown style: _TBD_
- Diagram tooling: _TBD_

---

## 4. Decision log

Reserve space for noting *why* a particular standard was picked when the choice is non-obvious. Format: short note + date.

- **2026-06-22 — Logging library: `log/slog`.** Migrated the codebase off the
  standard `log` package to `log/slog` (Go stdlib structured logging). Chosen
  over zap/zerolog/logr to avoid a new dependency (KISS/YAGNI) since slog is in
  the standard library, supports leveled + structured output, and is the modern
  Go default. A single text handler is installed once in `cmd`; the existing
  `debug` flag drives the level (Debug vs Info) rather than gating each verbose
  call with an `if`. `log.Fatal*` had no slog equivalent, so fatal sites became
  `slog.Error(...)` + `os.Exit(1)`, preserving termination semantics.
