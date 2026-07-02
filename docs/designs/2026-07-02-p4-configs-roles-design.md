# P4 — Configs + Roles — Design

**Date:** 2026-07-02 · **Slice:** P4 (v1 roadmap) · **Issues:** [#23](https://github.com/jacaudi/booty/issues/23) (rest), [#31](https://github.com/jacaudi/booty/issues/31) (Boot Configs view) · **PR target:** `jacaudi/booty:main` · ships **after** P3b; rebase base = `main` at merge (`61ffa6e`).

> **Session note:** this design was synthesized from two independent outlines
> (data-model-first and operator-workflow-first) against the live code on `main`,
> SGE-reviewed with findings folded. **All decisions D1–D12 are USER-APPROVED
> (2026-07-02, each as recommended)** — walked through individually; alternatives
> remain on file in the §14 appendix.

---

## 1. Context & problem

Boot configs today are **on-disk templates read per request**, one per family, server-wide:

- `handleIgnitionRequest` (`pkg/http/ignition.go`) parses `<dataDir>/<ignitionFile>`
  via `template.ParseFiles`, executes against `{JoinString, ServerIP, Hostname}`,
  then `butaneConfig.TranslateBytes` → Ignition JSON. A per-host override exists only
  as the legacy `hosts.ignition_file` column (`if host.IgnitionFile != "" {...}`).
- `handleMachineConfigRequest` (`pkg/http/machineconfig.go`) parses
  `<dataDir>/<talosConfigFile>`, executes against the Talos var set, and serves the
  rendered YAML **as-is** — there is **no Talos translation library in the serving
  path today** (this matters for §6/§14-D6).
- There is **no `/preseed` route** at all (`pkg/http/http.go` mounts only
  `/ignition.json` and `/machineconfig`).

Consequences: there is no operator-editable config store, no versioning/rollback, and
no per-host or per-role assignment beyond the single legacy `ignition_file` column.
Every node of a family gets the same server-wide template.

P4 makes boot configs **first-class DB state** — authored, validated, versioned,
rolled back, and bound per-host or per-role through the API and the AntD Boot Configs
view — while keeping the on-disk file as a **zero-migration terminal fallback** so an
unbound host boots exactly as it does today.

### 1.1 The operator workflows this slice must serve

The API and UI shape are derived from five concrete operator actions (canonical §5-P4):

1. **Author** — create a config (`name` + `kind` + `source`); it is validated before it can go live.
2. **Preview / validate** — render a config against a specific host's vars (or stub vars) and see the exact translated output *before* anything boots.
3. **Bind** — attach a config to a host directly (`hosts.config_id`) or to a **role** the host holds (fleet-wide default).
4. **Attach-config + Allow** — a pending host is in the holding pattern; in one action the operator attaches a config/role and approves ("Allow") it.
5. **Rollback** — a bad revision is live; point the config back at a prior **immutable** revision, no re-upload.

---

## 2. Goals / non-goals

**Goals**

1. `configs` + `config_revisions` + `roles` + `host_roles` tables and a `hosts.config_id` column (all additive; no host migration — the host store is already SQLite from P1a).
2. `/api/v1` surface: config CRUD, `POST /configs/{id}/preview` (subsumes validate), `GET /configs/{id}/revisions`, `POST /configs/{id}/rollback`; role CRUD; host binding (`POST /hosts/{mac}/bind`) and an **extended** `POST /hosts/{mac}/approve` that attaches config/roles atomically.
3. Boot-time config resolution by precedence (explicit host → role default → legacy per-host file → server-default file), reusing the existing render pipeline against DB source instead of a disk file.
4. Runtime translation reusing the **already-vendored** Butane path (`github.com/coreos/butane`); machineconfig and preseed served by passthrough (no new heavy dependency).
5. AntD **Boot Configs** view (Configs + Roles tabs) added additively via the `nav.tsx` seam.

**Non-goals (YAGNI — see §13)**

- Diff-based revisions (revisions are immutable full copies).
- Vendored Talos machineconfig **validation/generation** machinery — that is **P6 talhelper**, not P4 (the live serving path already does zero Talos translation).
- Multi-config merge / layering per host; role priority/ordinal weighting.
- Binding configs to **targets** (only hosts and roles).
- Config search/pagination; visual diff UI; import-existing-file endpoint; kind auto-detection.
- `DELETE` enabled (wired-but-403 until P10).
- `AutomaticEnv` / config file — booty has neither; every new knob is an explicit cobra flag.

---

## 3. Data model (the spine)

New migration `pkg/db/migrations/000N_configs_roles.sql` (N = next lexicographic slot
**after** whatever P3a/P3b land; the runner is positional/filename-ordered, so only
ordering matters — coordinate the number at plan time). All additive; a new migration
file requires **no runner code change** (No-Wall seam already in place).

**`configs`** — the logical config identity:

| column | type | notes |
|---|---|---|
| `id` | INTEGER PK | |
| `name` | TEXT NOT NULL UNIQUE | operator label; stable UI identity |
| `kind` | TEXT NOT NULL CHECK(kind IN ('butane','machineconfig','preseed')) | **source dialect** — see §3.1 |
| `active_revision_id` | INTEGER NULL REFERENCES config_revisions(id) | live pointer |
| `created_at`, `updated_at` | TEXT DEFAULT (datetime('now')) | |

**`config_revisions`** — immutable, append-only, full-copy (no diffs):

| column | type | notes |
|---|---|---|
| `id` | INTEGER PK | |
| `config_id` | INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE | |
| `revision` | INTEGER NOT NULL | per-config, monotonic, 1-based |
| `source_b64` | TEXT NOT NULL | base64 of raw source bytes (canonical §227; sidesteps YAML whitespace/quoting drift) |
| `source_sha256` | TEXT NOT NULL | integrity / dedup |
| `created_at` | TEXT DEFAULT (datetime('now')) | |
| | `UNIQUE(config_id, revision)` | |

**`roles`:**

| column | type | notes |
|---|---|---|
| `id` | INTEGER PK | |
| `name` | TEXT NOT NULL UNIQUE | |
| `default_config_id` | INTEGER NULL REFERENCES configs(id) ON DELETE SET NULL | fleet-wide default |
| `created_at`, `updated_at` | TEXT | |

**`host_roles`** — many-to-many (also feeds the `.Roles` template var):

| column | type | notes |
|---|---|---|
| `host_mac` | TEXT NOT NULL REFERENCES hosts(mac) ON DELETE CASCADE | |
| `role_id` | INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE | |
| | `PRIMARY KEY(host_mac, role_id)` | |

**`hosts`** column add: `ALTER TABLE hosts ADD COLUMN config_id INTEGER DEFAULT NULL`
— explicit per-host override, plain nullable column (**no** inline `REFERENCES`
clause: SQLite `ALTER … ADD COLUMN` cannot portably carry a foreign key with an
`ON DELETE` action). Referential cleanup when a config is deleted lands with P10
(when `DELETE /configs` is un-gated); until then delete is 403, so no dangling
`config_id` can be created.

> **Note (roles modeling):** P4 models host↔role as the `host_roles` **many-to-many join
> table**, not a literal `hosts.roles` column. This is a deliberate reconciliation of two
> canonical statements: roadmap §5-P4 / canonical §3 (hosts) speak loosely of a "host
> roles column," while canonical §3's separate `roles`/`host_roles` entry and §2.6's
> plural-`roles` / "first role" semantics require the join table. The join table is the
> correct model (multi-role hosts, ordered `.Roles` var); it is a wording reconciliation,
> not a scope drop.

### 3.1 `kind` is the source dialect, not the family mechanism — the bridge map

The one subtlety neither source outline surfaced, and the most load-bearing
correctness point in the API: **`config.kind` and `ostype.Family.ConfigKind` use
different vocabularies.**

- `pkg/ostype/ostype.go` `families` map: `Family.ConfigKind ∈ {ignition, machineconfig, preseed}` — this names the **boot-config-URL mechanism** (`ignition.config.url=`, `talos.config=`, `auto url=`).
- Canonical §227 mandates `config.kind ∈ {butane, machineconfig, preseed}` — this names the **source dialect the operator authors** (you write Butane; Ignition is its compiled output).

These are genuinely different knowledge (source vs mechanism), so per DRY they stay
distinct. The relationship between them is **single-sourced** as one 1:1 map used by
the family-match guard (§5) — only the `ignition ↔ butane` pair differs; the other two
are identity:

```go
// configKindForFamily maps an OS family's ConfigKind to the config source
// dialect. Single source of the family<->kind relationship (§3.1). Guards
// against serving a talos machineconfig to a flatcar host.
func configKindForFamily(familyConfigKind string) string {
    if familyConfigKind == "ignition" {
        return "butane"
    }
    return familyConfigKind // machineconfig, preseed are identity
}
```

The family-match guard is therefore built from the host's family **looked up via
`ostype.Lookup(host.OS)`** — `hardware.Host` stores `OS` as a name string
(`pkg/hardware/mac.go`) and has **no `Family()` method**; the family comes from
`ostype.Lookup(host.OS).Family()`. The guard is
`cfg.Kind == configKindForFamily(fam.ConfigKind)`, **not** naive equality against
`Family.ConfigKind` (which would fail every ignition-family bind). The lookup can
**miss** for an unidentified or empty-`OS` host — that branch is explicit: a lookup miss
means no family constraint can be evaluated, so resolution **falls through to the
server-default file** (§5) rather than serving a possibly-mismatched config.
`config.kind` alone is the compatibility key — **no `os`/`family`
column** is stored on `configs`, because kind↔family is 1:1 and a `butane` config
serves *any* ignition-family host (both `flatcar` and `fedora-coreos`). See §14-D3.

### 3.2 Revision semantics (correctness-critical)

- **Edit** a config = INSERT a new `config_revisions` row (`revision = max+1`) + advance `configs.active_revision_id`. Sources are never mutated in place (that is why `PUT /configs/{id}` takes a whole new `source`).
- **Rollback to revision R** = repoint `active_revision_id` to the **existing** rev-R row. Revisions stay immutable; no content is copied. A subsequent edit branches forward as `max+1`. (§14-D7)
- **Prune** (`--configRevisionsKeep`, default 10): on each new revision, keep the **newest N by `revision` UNION the currently-active revision**. Pruning must **never** delete `active_revision_id`'s row — otherwise a rollback-to-an-old-rev followed by an edit could evict the live config. `PruneRevisions` enforces the union. (§14-D8)

### 3.3 Store accessors

`pkg/db/configs.go` and `pkg/db/roles.go`, thin and typed, mirroring existing
`pkg/db` style:

- Configs: `CreateConfig`, `GetConfig`, `ListConfigs`, `AddConfigRevision`, `SetActiveRevision`, `ListRevisions`, `PruneRevisions`.
- Roles: `CreateRole`, `GetRole`, `ListRoles`, `UpdateRole`.
- Binding: `SetHostConfig(mac string, configID *int64)`, `SetHostRoles(mac string, roleIDs []int64)`, `ListHostRoles(mac string)`.

**Host-row mutations stay behind the `pkg/hardware` seam.** `hosts.config_id` and
`host_roles` are host state, and the canonical invariant is *all host access goes through
`pkg/hardware` accessors*. So the `pkg/db` binding accessors above are the SQL layer, but
the API calls them through **new `hardware.SetHostConfig` / `hardware.SetHostRoles`
wrappers** — mirroring the existing `hardware.SetAssignment`, which already wraps
`db.Store` — rather than reaching into `pkg/db` directly. Role *reads* (the serving
path's `db.Store.ListHostRoles`) go via `deps.Store` — the invariant guards host
**mutations**; a read-only `hardware.ListHostRoles` wrapper had no production consumer
and was cut at plan review (YAGNI). This preserves the host-accessor invariant that
§4's bind/approve path depends on.

**Not-found sentinel:** reuse `db.ErrNotFound` (the existing `pkg/db/cache.go`
sentinel) for these accessors. Do **not** introduce a third not-found sentinel —
converge on the cache-layer one so `pkg/http` checks `errors.Is(err, db.ErrNotFound)`
uniformly for configs/roles.

---

## 4. API surface (`/api/v1`, Huma-typed)

New files `pkg/http/api_configs.go` (`registerConfigs(grp, deps)`) and
`pkg/http/api_roles.go` (`registerRoles(grp, deps)`), wired additively in
`registerOperations` (`pkg/http/api.go`) alongside the existing catalog/targets/hosts/
cache registrars — siblings untouched (No-Wall). DTOs are Huma `Body`-wrapped,
camelCase-tagged, and live beside their registrar (matching `TargetDTO`/`CacheEntryDTO`).

**Configs**

| method + path | op | auth | body / result |
|---|---|---|---|
| `GET /configs` | list-configs | open | `{configs: [ConfigDTO]}` |
| `POST /configs` | create-config (201) | open | `{name, kind, source}` → validate (stub-var render, §5) → create config + revision 1 + set active |
| `GET /configs/{id}` | get-config | open | `ConfigDTO` + active `source` (b64-decoded) |
| `PUT /configs/{id}` | update-config | open | `{source}` → append revision, advance active, prune to N |
| `POST /configs/{id}/preview` | preview-config | open | `{mac?}` → `{rendered, translated, contentType, report}`. **No `mac` = stub-var validation** (subsumes `/validate`, §14-D2) |
| `GET /configs/{id}/revisions` | list-revisions | open | `{revisions: [RevisionDTO]}` |
| `POST /configs/{id}/rollback` | rollback-config | open | `{revision}` → repoint active (422 if absent); no new revision |
| `DELETE /configs/{id}` | delete-config | **403** | wired-but-403 until P10 (standard 403 string) |

`ConfigDTO{ID int64, Name, Kind string, ActiveRevision int, RevisionCount int, UpdatedAt string}`.
`RevisionDTO{Revision int, SHA256, CreatedAt string, Active bool}`.

**Roles**

| method + path | op | auth | body / result |
|---|---|---|---|
| `GET /roles` | list-roles | open | `{roles: [RoleDTO]}` |
| `POST /roles` | create-role (201) | open | `{name, defaultConfigId?}` |
| `PUT /roles/{id}` | update-role | open | `{name?, defaultConfigId?}` (pointer fields, per the `PATCH /targets/{id}` idiom) |
| `DELETE /roles/{id}` | delete-role | **403** | wired-but-403 until P10 |

`RoleDTO{ID int64, Name string, DefaultConfigID *int64, HostCount int}`.

**Host binding + approval** (extends `pkg/http/api_hosts.go`; open in the trust window)

- `POST /hosts/{mac}/approve` — **extended** to accept an *optional* body `{configId?, roleIds?[]}`. Approves + assigns (today's behavior) **and** binds config/roles **atomically** = "attach-config + Allow". Empty body = byte-identical to today (backward-compatible). (§14-D5)
- `POST /hosts/{mac}/bind` — `{configId?, roleIds?[]}`; rebind an already-approved host **without** changing approval. Sets `hosts.config_id` and **replaces** `host_roles`.

**Disclosed edit — `registerHosts` gains `deps`.** `api_hosts.go`'s `registerHosts`
currently takes `_ APIDeps` and the approve handler mutates host state exclusively
through `pkg/hardware` accessors (`hardware.GetMacAddress`/`Approve`/`SetAssignment`).
Binding needs the store, so P4 rewires `registerHosts(_ APIDeps → deps APIDeps)` to
consume `deps.Store` — an in-scope, required change parallel to §5's disclosed
`http.go` handler-to-closure rewrite. Crucially, the new handlers still mutate host rows
**through `pkg/hardware`** — via the new `hardware.SetHostConfig` / `hardware.SetHostRoles`
wrappers (§3.3), not by writing the `hosts` columns directly from `pkg/db` — so the
"all host access goes through `pkg/hardware`" invariant holds. (§14-D12)

**Boundary validation** (KISS: these are behavior, not complexity — never stripped):
`kind` ∈ enum; `source` non-empty; `rollback.revision` must exist (422); on bind/approve,
`configId` must exist and its `kind` must satisfy the **family-match guard** (§3.1, §5)
for the host's OS (422 on mismatch). **Config/role `name`s are NOT cache path segments**
— `cache.ValidatePathParam` does **not** apply to them (they never become disk/URL
segments); `UNIQUE` + non-empty is the only name constraint.

Registration is additive; tested via `newTestAPI`/`humatest` (§10).

---

## 5. Boot-time config resolution (precedence)

Resolved per request by the family serving endpoints. Order **[§14-D4]**:

1. **`hosts.config_id`** (explicit per-host override) → that config.
2. else the host's roles ordered **by name asc** (no priority column — YAGNI), first with a non-null `default_config_id` → that config.
3. else the **legacy per-host file column** where one exists — `hosts.ignition_file` for the ignition family (preserves today's `if host.IgnitionFile != ""` behavior). Talos/Debian have no legacy per-host file column, so they skip this rung.
4. else the **server-default file** per family (existing `--ignitionFile` / `--talosConfigFile`, and a new `--preseedFile` if §6/§14-D9 lands). **Zero migration**: an unbound host with no legacy column boots byte-identically to today.

**Family-match guard (validation, not optional):** a resolved DB config's `kind` must
equal `configKindForFamily(fam.ConfigKind)` where `fam = ostype.Lookup(host.OS).Family()`
(§3.1 — `host` has no `Family()` method; the family is looked up from `host.OS`). A
mismatch (e.g. a Talos host somehow bound to a `butane` config) **or a lookup miss**
(unidentified / empty `OS`) → `slog.Warn` and **fall through to the server-default file**
— never serve a cross-family config. A rung-1 **explicit** (`hosts.config_id`) mismatch
**short-circuits directly to the file** — it does **not** fall through to a rung-2 role
default (an explicit-but-wrong per-host bind is an operator error to surface, not silently
paper over); a rung-2 role-default mismatch instead skips to the next role by name.
Bind-time validation (§4) makes the mismatch case unreachable in practice, but the
serve-time guard is the last line of defense against a hand-edited DB.

`resolveConfig(store, host) (source []byte, kind string, ok bool)` and
`renderConfig` (§6) are **unexported helpers in `pkg/http`** (new file, single
consumer package — no new package until a second consumer exists; YAGNI/No-Wall).

**Wiring (disclosed edit):** the serving handlers currently have **no `*db.Store`** —
they reach hosts through the `pkg/hardware` singleton and config through viper globals.
`StartHTTP` already receives `deps APIDeps`. P4 converts the `/ignition.json`,
`/machineconfig`, and new `/preseed` registrations in `pkg/http/http.go` from bare
`HandleFunc`s into **closures capturing `deps.Store`**, so the handlers can call
`resolveConfig`. This is a small, disclosed change to `http.go` and the two existing
handler signatures — necessary for DB-sourced resolution to be implementable at all.

---

## 6. Rendering / translation (reuse-first)

A single shared step, consumed by **both** the serving endpoints and `/preview`:

```go
func renderConfig(kind string, source []byte, vars TemplateVars) (out []byte, contentType, report string, err error)
```

`renderConfig` receives an **already-populated** `TemplateVars`; it does **not** assemble
the vars itself. Each serving family's resolution path fills the struct (see the
per-family `.ServerIP` note in step 3), and `/preview` fills it from a selected host or
stub values. This keeps `renderConfig` a pure render+translate step.

1. `text/template.New().Parse(string(source))` + `Execute(vars)` — **reuse** the exact template mechanics already in `ignition.go`/`machineconfig.go`; the only change is `Parse(string)` instead of `ParseFiles(path)` so DB source works.
2. Per-kind dispatch — a `switch` over 3 present concrete kinds, **no interface** (one impl per arm would be a premature wall):
   - **`butane`** → `butaneConfig.TranslateBytes` (**already imported** in `pkg/http/ignition.go`; `github.com/coreos/butane v0.19.0` confirmed vendored) → Ignition JSON, `application/json`. A fatal report → `err` (mirrors the current handler exactly).
   - **`machineconfig`** → **passthrough** rendered YAML, `text/yaml`. **No Talos library** — the live `handleMachineConfigRequest` already serves the template as-is with zero Talos deps. Canonical §2.6's "machineconfig via vendored Talos machinery" is **P6 talhelper generation**, not P4 author-and-serve (live compiling code wins). (§14-D6)
   - **`preseed`** → **passthrough** rendered text, `text/plain`.
3. **`TemplateVars`** — one struct (single-sourced *shape*), **populated per family** by
   the resolution path, reused by serving + preview:
   `.Hostname .MAC .IP .UUID .Serial .ServerIP .ServerHTTPPort .JoinString .Roles` +
   `.TalosVersion .Schematic` (machineconfig). Two cautions correct the earlier
   over-claim that these are "exactly the fields already assembled in the two handlers":

   - **`.ServerIP` carries different semantics per family and MUST NOT be unified into one
     meaning.** `ignition.go` sets `ServerIP = fmt.Sprintf("%s:%s", config.ServerIP,
     config.ServerHttpPort)` (i.e. **host:port**), and `examples/config/ignition.yaml`
     depends on it as `http://{{ .ServerIP }}/data/config/...`. `machineconfig.go`
     instead sets `ServerIP` **host-only** plus a separate `.ServerHTTPPort`. Because the
     struct is populated per family, the ignition arm keeps `ServerIP = host:port` (and
     does **not** use `.ServerHTTPPort`), while the machineconfig arm keeps host-only +
     `.ServerHTTPPort`. Collapsing `.ServerIP` to a single host-only meaning would drop
     the port from every ignition artifact URL (hosts would fetch `:80` instead of
     `:PORT`) — a **live PXE regression** violating §5 rung-4 / acceptance-criterion #4
     (byte-identical unbound boot). Existing operator ignition files are left untouched.
     (§14-D11)
   - **`.TalosVersion` is a newly-sourced var, not one already assembled.**
     `machineconfig.go` assembles `.Schematic` but has **no** `.TalosVersion` field today
     (the var appears only in canonical §2.6). Like `.Roles`, P4 sources it — from the
     host's target/assignment version — rather than reusing an existing handler field.

   `.Roles` is likewise newly sourced from `host_roles`.

The existing `handleIgnitionRequest`/`handleMachineConfigRequest` are refactored to:
resolve source (§5 precedence, DB → file) → `renderConfig`. The
**reboot-on-unknown-host** ignition behavior and the Talos **render-host-less-at-first-boot**
behavior are both preserved. `/preview` calls the same `renderConfig` with either a
selected host's vars or stub vars.

**Preseed serving gap:** there is **no `/preseed` route today**. For a `preseed`
config to actually boot a Debian host, P4 must add a `/preseed` handler + a
`--preseedFile` server-default. `renderConfig` supports the kind uniformly (near-free);
whether to wire the serving route in P4 or store-only-and-defer is **§14-D9**.

---

## 7. UI — Boot Configs view (antd v5, token-driven / v6-compatible)

**Additive per the `nav.tsx` seam.** New files: `web/src/api/configs.ts` (+`.test.ts`),
`web/src/api/roles.ts` (+`.test.ts`), `web/src/views/BootConfigsView.tsx` (+`.test.tsx`).
**Edit only `nav.tsx`**: one import + one entry
`{ path: '/boot-configs', label: 'Boot Configs', element: <BootConfigsView/> }`.
`App.tsx` and sibling views are untouched (App maps `navEntries` generically).

`BootConfigsView` = antd `Tabs` with items `[{key:'configs'}, {key:'roles'}]` (a plain
array; P5/P6 Schematics/Talhelper tabs append here). Each tab is an existing-pattern
load/act/render view (`useState`/`useCallback load`/`useEffect`/`act` + `Table` +
`message`, matching `HostsView`/`CacheView`):

- **Configs tab:** `Table`(name, kind `Tag`, active revision, updated) + actions:
  - **Create** (`Modal` + `Form`: name, kind `Select`, source `Input.TextArea`).
  - **Edit** (`Modal` + `Input.TextArea` → `PUT` new revision).
  - **Preview** (`Modal`: optional host `Select` → shows translated output + report from `POST /preview`).
  - **Revisions** (`Drawer`/`Modal`: list revisions with per-row **Rollback**).
  - **Delete** — `Tooltip` "available after authentication (P10)" + `disabled` (the existing `CacheView` gating convention).
- **Roles tab:** `Table`(name, default config, host count) + Create/Edit (default-config `Select`); Delete disabled.

`api/configs.ts` and `api/roles.ts` are per-domain wrappers over the shared
`request<T>()` helper (the `api/cache.ts` precedent; interfaces local to each module).

**Cross-slice touch (deliberate, in-scope per canonical §5-P4):** extend P2's
`HostsView.tsx` pending-host approve into an **"Allow"** `Modal` offering optional
config `Select` + role multi-`Select`; on confirm it calls the **extended**
`POST /approve` (one atomic call, §4). This is a backward-compatible additive edit to a
sibling, disclosed here — not a No-Wall violation (the seam is the approve endpoint).

Conventions honored: colors via antd component props only (`Tag`, `Alert type`,
`Button danger`/`type="primary"`) — **no hardcoded hex**; one primary button per row;
`message.*` for feedback (static API lacks `ConfigProvider` theme context — acceptable,
matches existing views; the v6 `ConfigProvider` migration is P7). Unpaginated `Table`s
(matches existing views).

---

## 8. Migration & compatibility with the file-based configs

- **New migration** `000N_configs_roles.sql` (§3): 4 tables + `hosts.config_id`. The runner is filename-ordered/positional → purely additive, **no runner code change** (seam already in place). Idempotent re-run is a `PRAGMA user_version` no-op.
- **No forced migration** of `examples/config/ignition.yaml` / `config/machineconfig.yaml` (contrast issue #48, which relocated cache dirs). The on-disk file is the **implicit terminal fallback** (§5 rung 4); operators opt into DB configs by authoring them. **Unbound hosts are byte-unchanged.**
- The legacy `hosts.ignition_file` column is **retained** in the precedence (§5 rung 3) for compatibility; removing it is a later cleanup slice, not P4.
- **No import-file-into-DB endpoint** (YAGNI) — authoring is via `POST /configs`.

---

## 9. Constraints (unchanged project invariants)

Module `github.com/jeefy/booty`; PR to `jacaudi/booty` base `main`; CGO-free Go 1.26
(`modernc.org/sqlite`); `log/slog`; Huma v2 (`humago`); modern-Go idioms (`cmp.Or`,
`errors.As`/typed sentinels, `slices`/`maps`). Mutating API **open** in the trust
window; `DELETE` (and this slice's `DELETE /configs`, `DELETE /roles`) **wired-but-403**
until P10. Every new knob is an explicit cobra flag bound via `viper.BindPFlags` —
booty has **no** `AutomaticEnv`/config file. New flags this slice:
`--configRevisionsKeep` (default 10), and `--preseedFile` **iff** §14-D9 wires preseed
serving. Each = a `const` in `pkg/config/config.go` + `viper.SetDefault` +
`flags.IntVar`/`StringVar` in `cmd/main.go`.

---

## 10. Testing (against the real harnesses)

- **`pkg/db`** (table-driven, real `modernc.org/sqlite` on `t.TempDir()` via the existing store test helper): config CRUD; revision append monotonicity; rollback = active-pointer move (no new revision); **prune keeps newest-N ∪ active** (the rollback-then-edit hazard, §3.2); role CRUD; `host_roles` bind/replace/unbind; cascades (config delete → revisions gone; role delete → `host_roles` gone); `SetHostConfig(nil)` clears.
- **render/translation** (table-driven): real `butaneConfig.TranslateBytes` on a known-good source (asserts Ignition JSON) **and** a known-bad source (asserts fatal report → `err`); machineconfig/preseed passthrough identity; `configKindForFamily` bridge map (§3.1) incl. the `ignition→butane` case.
- **resolution precedence** (table): all 4 rungs incl. legacy `ignition_file` and the **family-mismatch fall-through** to server-default.
- **boot-path byte-identity** (regression guard for acceptance-criterion #4): the rendered ignition JSON for an **unbound** host (falling through to the server-default file) is **byte-identical to pre-P4** — specifically asserting artifact URLs still carry `host:port` via `.ServerIP`, so the §6 per-family var population did not regress the live PXE path.
- **`pkg/http`** (`newTestAPI`/`humatest` — same `registerOperations` entrypoint as `RegisterAPI`): configs + roles CRUD; `/preview` with and without `mac`; `/revisions`; `/rollback`; 422 (bad kind / absent revision / family-mismatch on bind) / 404 (missing) / 403 (`DELETE`); extended `approve` body + `bind`.
- **migration**: apply `000N` on a post-P3b DB (tables + `hosts.config_id` exist); idempotent re-run.
- **UI** (Vitest + RTL, existing view-test pattern): `vi.mock('../api/configs')`/`roles`; render `BootConfigsView`, assert tabs, create flow, preview-modal render, rollback call + reload contract (`listConfigs` called twice — mount + post-action). `HostsView` bind-modal calls the extended `approve`. `api/*.test.ts` fetch-stub pattern (`vi.stubGlobal('fetch', …)`, assert path + init). `App.test.tsx` nav-link regression only if link coverage is explicitly added.
- All green under `go test -race`; `go build ./...`, `go vet`, linter, `npm test`.

---

## 11. Documentation gate (slice incomplete without)

- `docs/schema/API.md` — configs/roles/bind endpoints, DTOs, preview-subsumes-validate semantics, family-match validation rule, wired-but-403 `DELETE`s.
- `docs/schema/DATABASE.md` — 4 new tables + `hosts.config_id`; revision/rollback/prune semantics; the `kind` enum and its relationship (§3.1) to `Family.ConfigKind`.
- `docs/schema/STORAGE.md` — config source is **DB-authoritative**; on-disk file = terminal fallback; no file→row migration.
- `docs/CONFIGURATION.md` — `--configRevisionsKeep` (and `--preseedFile` if D9 lands); the 4-rung precedence order.

---

## 12. Acceptance criteria

1. `configs`/`config_revisions`/`roles`/`host_roles` + `hosts.config_id` land in one additive migration; re-run is a no-op.
2. Config CRUD + `PUT`-appends-immutable-revision + `POST /rollback` (pointer move) + prune (newest-N ∪ active) work via `/api/v1`; `DELETE /configs` & `/roles` return 403.
3. `POST /configs/{id}/preview` renders + translates against a host's vars, and validates (report-only) when `mac` is omitted; bad Butane surfaces a fatal report.
4. Boot resolution honors precedence (host `config_id` → role default by name → legacy `ignition_file` → server-default file); an **unbound** host boots byte-identically to pre-P4; a family-mismatched config never serves.
5. `POST /hosts/{mac}/approve {configId,roleIds}` atomically attaches + approves; `POST /hosts/{mac}/bind` rebinds an approved host; empty approve body = today's behavior.
6. Boot Configs view (Configs + Roles tabs) is reachable via one `nav.tsx` entry; create/edit/preview/revisions/rollback flows work; `DELETE` is Tooltip-disabled.
7. Docs gate (§11) complete; tests (§10) green under `go test -race`.

---

## 13. Explicit YAGNI / KISS cuts

Full-copy revisions (no diff engine) · no Talos validation/generation machinery
(passthrough; generation is P6) · stdlib `text/template` only (no sprig) · `switch`-on-kind
render, **no interface** · no role priority/ordinal column (order by name) · single
resolved config (no merge/layering) · no target-bound configs · no import-file endpoint ·
no kind auto-detection · unpaginated tables · `DELETE` wired-but-403 · file-as-fallback
(no file→row migration) · new flags only where a knob has a present consumer
(`--configRevisionsKeep`; `--preseedFile` iff D9) · `resolveConfig`/`renderConfig` stay
unexported in `pkg/http` (no new package until a second consumer exists).

---

## 14. Appendix — decisions (ALL USER-APPROVED 2026-07-02, each as recommended)

| # | Decision | Recommended | Alternative on file |
|---|----------|-------------|---------------------|
| D1 | **Source storage** | `source_b64` — follows canonical §227; avoids TEXT whitespace/quoting drift; stable sha | raw TEXT column (marginally simpler; risks YAML round-trip drift) |
| D2 | **Preview vs validate** | Single `POST /configs/{id}/preview {mac?}` — no `mac` = stub-var validation; single-sources the render path with serving (DRY/KISS). **Diverges from canonical §256's separate `/validate`.** | Keep a distinct `POST /configs/{id}/validate` per canonical; `/preview` requires a `mac` |
| D3 | **Compatibility key** | `config.kind` as the sole key; **no `os`/`family` column** (kind↔family 1:1; `butane` serves flatcar **and** fcos); bridge map `configKindForFamily` single-sources the `ignition↔butane` relationship (§3.1) | Store an `os`/`family` column on `configs` (redundant with kind; a second source of the same fact) |
| D4 | **Resolution precedence** | 4-rung: `hosts.config_id` → role default (by name) → legacy `hosts.ignition_file` → server-default file; family-mismatch falls through to server-default | 3-rung (drop the legacy `ignition_file` rung) — simpler but silently changes today's per-host-file behavior |
| D5 | **Approval / attach-config UX** | Extend `POST /hosts/{mac}/approve` with optional `{configId,roleIds}` (atomic attach+allow, backward-compatible) **+** add `POST /hosts/{mac}/bind` for rebinding approved hosts; P4 edits `api_hosts.go` + `HostsView.tsx` (disclosed in-scope cross-slice touch) | Separate `PUT /hosts/{mac}/config` + `PUT /hosts/{mac}/roles`, approve unchanged, UI composes 3 non-atomic calls (partial-failure window) |
| D6 | **machineconfig/preseed translation** | Passthrough (template + serve as-is), **no vendored Talos machinery** — matches the live serving code; canonical §2.6's "Talos machinery" is P6 talhelper, not P4 | Vendor Talos config validation now (new heavy dep, no present serving consumer) |
| D7 | **Rollback mechanism** | Pointer move — repoint `active_revision_id` to an existing immutable revision; prune protects the active target | Rollback-as-new-revision (copies old content forward as `max+1`; more rows, same effect) |
| D8 | **Revision pruning** | Keep newest-N by `revision` **∪** the active revision (never evict the live config) | Keep newest-N strictly by `revision` (can evict the active row after rollback-then-edit — a data-loss hazard) |
| D9 | **Preseed serving in P4** | Add a `/preseed` handler + `--preseedFile` server-default so an authored `preseed` config can actually boot Debian (near-free passthrough mirror of `/machineconfig`; completes the canonical §152 serving triad). **YAGNI tension named:** Debian boot is otherwise unexercised in v1 so far | Store-and-validate preseed but **defer serving** (no `/preseed` route this slice) — avoids a route for an untested boot path, but ships an authorable-yet-unbootable config kind |
| D10 | **Migration number** | `000N_configs_roles.sql`, next lexicographic slot **after** P3a/P3b; confirm N at plan time | (mechanical; positional runner means only ordering matters) |
| D11 | **`TemplateVars` population** | One struct as a single-sourced *shape*, but **populated per family** so `.ServerIP` keeps its live per-family semantics (`host:port` for ignition, host-only + `.ServerHTTPPort` for machineconfig); existing operator ignition files untouched. Preserves byte-identical unbound boot (§6, acceptance-criterion #4) | **(b)** Rename to unambiguous fields (`.ServerAddr` = host:port vs `.ServerIP`/`.ServerHTTPPort`) and update `examples/config/ignition.yaml` — fully disambiguates but changes the shipped template and breaks any operator file referencing `{{ .ServerIP }}` as host:port |
| D12 | **Host-binding through the `pkg/hardware` seam** | `hosts.config_id`/`host_roles` mutated via **new `hardware.SetHostConfig`/`SetHostRoles` wrappers** (mirroring `hardware.SetAssignment`) over the `pkg/db` accessors; `registerHosts(_ APIDeps → deps)` rewired to consume `deps.Store` (disclosed in-scope edit). Preserves the "all host access via `pkg/hardware`" invariant | Treat `config_id`/`host_roles` as config-domain state written directly via `pkg/db` from the handlers — fewer wrappers, but an explicit exception to the host-accessor invariant |

**Rebase note:** P4 assumes P3b has landed; base = `main` at merge (`61ffa6e`). §5's
serving-handler refactor and §6's `renderConfig` touch `pkg/http/ignition.go` /
`machineconfig.go` / `http.go`; confirm no conflict with P3b's changes to those files at
plan time (P3b is signature verification on the cache/download path and is *expected*
not to touch the serving handlers, but verify).

---

### SGE review

An SGE design review was performed against the live code on `main` and its findings
folded in. Two substantive corrections:

- **§6 `TemplateVars` (blocking).** The claim that the unified vars were "exactly the
  fields already assembled" was false for `.ServerIP`: `ignition.go` sets it to
  `host:port` (relied on by `examples/config/ignition.yaml`) while `machineconfig.go`
  sets it host-only + `.ServerHTTPPort`. Unifying it to one meaning would have dropped
  the port from ignition artifact URLs — a live PXE regression against
  acceptance-criterion #4. Resolved by populating `TemplateVars` **per family** (§6
  step 3, §14-D11) and adding a byte-identity boot-path test (§10). `.TalosVersion`
  corrected from "already assembled" to a newly-sourced var.
- **Host-binding seam (important).** The host-binding path now routes `hosts.config_id` /
  `host_roles` mutations through new `hardware.SetHostConfig`/`SetHostRoles` wrappers to
  preserve the `pkg/hardware` host-access invariant, and the required
  `registerHosts(_ APIDeps → deps)` rewrite is now explicitly disclosed (§3.3, §4,
  §14-D12).

Minor accuracy folds: §3.1/§5 family-match guard rewritten to `ostype.Lookup(host.OS)`
with an explicit empty/not-found fall-through (there is no `host.Family()` method); §3
notes `host_roles` (join table) is the deliberate model over a literal `hosts.roles`
column.
