# P2 — Management-plane UI shell + Hosts view — Design

**Date:** 2026-07-01
**Type:** Design
**Feature:** p2-ui-shell-hosts-view
**Slice:** P2 of the v1 management-plane roadmap (`docs/plans/2026-06-28-v1-management-plane-design.md` §2.7, §304-306)
**Issues:** #31 (shell), #12, #11
**Status:** Approved (design phase) — pending written-spec review, then `superpowers:writing-plans`

---

## 1. Context & problem

P2 is the **UI-shell seam** of the v1 roadmap: the first slice that puts a real
management-plane UI on the already-built backend, and the foundation every later
vertical UI slice (P3 Cache, P4 Configs/Roles, P5 Schematics, P6 Talhelper, P7
ops/dashboard) hangs off. P0/P1a/P1b/P1c and #44 are all merged to `main` (green),
so the backend the UI drives already exists:

- `/api/v1/hosts` is fully wired (Huma): `GET /hosts?approved=`, `POST /hosts/{mac}/approve`,
  `POST /hosts/{mac}/revoke`, `POST /hosts/{mac}/menu`; `PUT`/`DELETE /hosts/{mac}` are
  wired but return **403 until auth (P10)**.
- The response contract is `hardware.Host` (`pkg/hardware/mac.go`), wrapped as
  `{ "hosts": [ <Host>, ... ] }` for the list.

The **current `web/` frontend is a throwaway stock Vue 3 scaffold**: Bootstrap-from-CDN,
four thin Options-API views, one broken Cypress scaffold test, unused Pinia. Its
`HostsView` calls the *legacy* root routes (`/booty.json`, `/register`, `/unregister`)
and carries an obsolete `ostreeImage` field (UBlue/ostree-era, removed by PR6). There is
**no `embed.FS` for the UI** — it is served off disk from `./web/dist`, populated at
container build time by a Docker `build-web` stage. The only `//go:embed` in the repo is
SQL migrations.

So P2 is effectively a **greenfield UI rewrite** with near-zero existing investment to
preserve.

## 2. Goals / non-goals

**Goals**
- Replace the Vue scaffold with a **React + Ant Design (antd)** app.
- **Embed** the built UI in the Go binary via `embed.FS`; drop the final-stage web copy.
- An **app shell + data-driven nav** built as a **No-Wall seam** so P3–P7 slot in additively.
- A **Hosts view** (Pending / Approved) with approve / revoke / boot-menu wired to
  `/api/v1/hosts`.
- A thin **typed API client** as the DRY contract seam for all future slices.
- Minimal **Home** and **About** pages so the shell is real.
- Self-documenting-repo doc updates (§8).

**Non-goals (YAGNI)**
- The P7 **Home dashboard** (P2 Home is a minimal landing).
- Any **Cache / Boot Configs / Database** views (P3–P8).
- Host **edit / delete** UI — `PUT`/`DELETE` are 403 until P10, so no buttons ship now.
- **Auth** (P10).
- **OpenAPI → TypeScript codegen** (noted future seam, not built).
- **Playwright/e2e** UI tests (deferred; the UI is CRUD over an already-tested API).

## 3. Framework decision (resolved)

**React 18 + Ant Design 5** (Vite + TypeScript + React Router 6; Vitest + React Testing
Library for tests).

Rationale: the canonical design §2.7 mandates "AntD components only," and Ant Design's
**first-class/reference implementation is React (`antd`)** — the fullest component set and
best-maintained, versus the community `ant-design-vue` port. Because the existing Vue
scaffold has **near-zero value to preserve** (disposable scaffold, one broken test), the
usual "keep the existing framework" counter-weight does not apply — we rewrite the views
against `/api/v1` either way. The deciding factor (React vs Vue long-term maintenance
comfort) was put to the maintainer, who chose React + antd.

**Delete** in the rewrite: Vue, Bootstrap, Pinia, Cypress, `vue-router`, and the scaffold
`components/icons/*`, `stores/counter.ts`. The obsolete `ostreeImage` field disappears with
the old `HostsView` (the new `Host` model has no such field).

## 4. Serving & embedding

### 4.1 embed.FS
- New `web/embed.go` (`package web`): `//go:embed all:dist` → `var distFS embed.FS`, with an
  exported accessor returning `fs.Sub(distFS, "dist")`.
- `pkg/http/http.go` swaps the `/ui/` handler from `http.Dir("./web/dist")` to
  `http.FS(embedded)`. **Routing is otherwise unchanged**: `/` continues to 302 → `/ui/`
  (`handleRequest`, `pkg/http/request.go`), and the UI is served under `/ui/`. The Huma
  `/api/v1` mount and all legacy routes are untouched.

### 4.2 Compile-everywhere (the committed-placeholder decision)
`//go:embed` requires the embedded directory to exist **at Go compile time**, but `dist` is
a gitignored build artifact — a fresh checkout and the **CI `go build` / `go test -race`
gate** run with no `npm run build`. Decision:

- Commit `web/dist/.gitkeep`; `web/.gitignore` ignores `dist/*` **except** `!dist/.gitkeep`.
- Use `//go:embed all:dist` (the `all:` prefix embeds dotfiles, so the committed `.gitkeep`
  satisfies the directive). A fresh checkout / CI therefore **compiles**; the UI 404s until
  `npm run build` populates real (gitignored) assets, which is fine because CI runs `go test`,
  not the UI, and real builds always build the web app first.

This keeps generated JS/CSS **out of git** (matching the repo's gitignore-artifacts
convention) with no "modified index.html" churn.

### 4.3 SPA client-side routing
React Router uses **BrowserRouter** with `basename="/ui"`. A small **SPA-fallback** handler
serves the embedded `index.html` for any `/ui/*` path that does not resolve to a real
embedded asset, so client routes (`/ui/hosts`, `/ui/about`) deep-link and refresh correctly.
Vite keeps `base: '/ui/'`.

*(Alternative considered: HashRouter needs zero server-side fallback but yields `/ui/#/hosts`
URLs. The ~10-line fallback + clean URLs was preferred; this is the only reversible piece if
it proves troublesome.)*

### 4.4 Dockerfile
The node `build-web` stage **stays**, but its `dist` output is copied **into the Go build
stage before `go build`** so the embed directive picks it up. The **final distroless stage
drops** `COPY --from=build-web /app/dist /web/dist` — the assets now live inside the binary.
This is the precise meaning of the roadmap's "drop the `COPY --from` web stage": the web
build moves *earlier* (to feed embedding), and the final-image web copy is removed.

## 5. App shell + nav — the No-Wall seam

A single **data-driven registry** (`web/src/nav.tsx`): an array of
`{ path, label, element }`. The antd `Layout` renders its `Menu` from that array, and React
Router builds its routes from the same array. **Adding a later slice's view = one new view
file + one registry entry; the shell and sibling views are never edited.** That is the entire
seam — deliberately **no** plugin system, **no** generic view framework, and **no**
pre-stubbed "coming soon" nav items (those would be speculative generality / YAGNI).

P2 ships the entries it has pages for: **Home · Hosts · About**. Later slices append
**Boot Configs · Cache · Database** additively (the full design-§2.7 nav is
`Home | Hosts | Boot Configs | Cache | Database | About`).

**Shell layout:** antd `Layout` (header with the Booty brand + nav `Menu`; content region
renders the routed view). No custom CSS beyond what antd needs — "AntD components only."

## 6. Hosts view (the deliverable — #12 / #11)

- **Data:** one `GET /api/v1/hosts` fetch, **partitioned client-side** by the `approved`
  flag (simpler than two `?approved=` round-trips; the filter still exists server-side for
  future use).
- **Two antd `Table`s:** *Pending* (`approved=false`) and *Approved* (`approved=true`).
- **Columns** (from `hardware.Host`): MAC, Hostname, IP, OS (self-reported `os`), Booted.
  The Approved table additionally shows **Boot Mode** (`bootMode`: `assigned` | `menu`) and
  **Assigned OS** (`assignedOS`).
- **Actions:**
  - *Pending row* → **Approve** (`POST …/approve` — the backend auto-assigns the
    self-reported OS, encoding `schematic` for Talos; no UI input needed) and **Boot menu**
    (`POST …/menu` — approve directly into interactive-menu mode).
  - *Approved row* → **Revoke** (`POST …/revoke`) and a **Boot menu** action to flip an
    approved host into menu mode.
  - **No edit / delete** buttons (`PUT`/`DELETE` are 403 until P10).
- **States:** antd `Table` `loading` while fetching; errors surfaced via `Alert` / `message`;
  explicit empty state. Mutations **re-fetch** the list (replacing the old buggy optimistic
  local mutation) so the UI always reflects server truth.

## 7. API client — DRY / No-Wall contract seam

`web/src/api/` holds:
- A **`Host` TypeScript type** mirroring `hardware.Host` (`mac`, `hostname`, `ip`, `booted`,
  `os`, `approved`, `bootMode`, `assignedOS`, `assignedParams`, `schematic`, …).
- A single **fetch wrapper** owning the `/api/v1` base path and error handling, plus
  `listHosts()`, `approveHost(mac)`, `revokeHost(mac)`, `setMenuMode(mac)`.

Every later slice adds its endpoint functions **here**, alongside — the DRY single source for
the API contract. Types are **hand-written** for P2 (one struct, four calls). **OpenAPI-driven
codegen** (`openapi-typescript` against `/api/v1/openapi`) is a noted future seam if the
surface grows enough to warrant it — the seam (a central `api/` dir) is free; the codegen
tooling is not built now.

## 8. Documentation gate (self-documenting repo, roadmap §6)

P2 adds **no new API endpoints** (they exist from P1c/#44), so:
- `docs/schema/API.md` — note that the management UI consumes the hosts API (no surface change).
- `README.md` — add a **"Management UI"** section (how to reach it: `/` → `/ui/`).
- `docs/` — a **frontend/development note**: building `web/`, the `embed.FS` pipeline, and the
  Vite dev server.
- Fix stale `jeefy` → `jacaudi` links in the About page.

## 9. Testing

- **Go (TDD, per project convention):** a test for the embed serving + SPA fallback — index
  served, an asset served, and an unknown `/ui/x` path falls back to `index.html`.
- **React:** **Vitest + React Testing Library** component tests for the Hosts view — renders
  both tables from mocked data; Approve / Revoke / Boot-menu invoke the correct client
  function (fetch mocked); loading and error states render.
- **Dropped/deferred:** delete the broken Cypress scaffold; Playwright e2e is deferred (the UI
  is CRUD over an already-tested API; boot-path e2e remains the netboot lab).

## 10. Principles check

- **KISS:** one fetch + client-side partition; re-fetch instead of optimistic mutation; no
  edit/delete machinery for 403 endpoints; hand-written types; antd components, minimal custom
  CSS.
- **YAGNI:** no dashboard, no future views, no disabled placeholder nav, no codegen, no e2e —
  each deferred to the slice that needs it.
- **DRY:** the `api/` client is the single source for the API contract; the nav registry is the
  single source for routes + menu.
- **No-Wall:** the nav/route registry and the `api/` dir are free seams (built to ship P2
  cleanly) that make P3–P7 additive — a new view file + one registry entry + that slice's own
  client functions, siblings untouched. No abstraction is added for a future variant that
  doesn't yet exist.

## 11. Constraints

- Module path stays `github.com/jeefy/booty` (unchanged; not in scope to fix). **PR to
  `jacaudi/booty`.**
- Mutating API stays **open** in the trust window (auth is P10); `PUT`/`DELETE` remain 403.
- Go 1.26, CGO-free backend; the embed change is pure stdlib (`embed`, `io/fs`, `net/http`).
- Per-slice **doc gate** (§8) must be satisfied for the slice to be "done."

## 12. Acceptance criteria

1. The binary serves the React UI from `embed.FS` with **no `./web/dist` required on disk**.
2. `go build ./...` and the CI `go test -race` gate are **green in a fresh checkout** (no
   `npm run build` needed to compile).
3. `/` → `/ui/` → an antd shell with **Home / Hosts / About** nav; client routes deep-link and
   refresh (SPA fallback works).
4. The **Hosts** view lists Pending / Approved hosts and performs **approve / revoke / boot-menu**
   against `/api/v1/hosts` with real, re-fetched state changes.
5. The Vue scaffold, `ostreeImage`, and Cypress are **gone**; the nav/route registry and `api/`
   client seams are in place.
6. Docs updated per §8.

---

## Appendix — reference (current backend contract)

**`hardware.Host` JSON** (`pkg/hardware/mac.go`) — the exact shape the UI consumes:

```
mac, hostname, ip, booted            // always present
ignitionFile, os, doInstall, schematic   // omitempty
approved, bootMode, assignedOS,          // omitzero
assignedArch, assignedParams, uuid, serial
```

`bootMode` ∈ { `assigned`, `menu` }. List wrapper: `{ "hosts": [ <Host>, ... ] }`.
Approve/menu return the updated `Host` as the body; revoke returns no body.

**Hosts endpoints** (`pkg/http/api_hosts.go`, group prefix `/api/v1`):
`GET /hosts?approved=` · `POST /hosts/{mac}/approve` · `POST /hosts/{mac}/revoke` ·
`POST /hosts/{mac}/menu` · `PUT|DELETE /hosts/{mac}` → 403 (until P10).
