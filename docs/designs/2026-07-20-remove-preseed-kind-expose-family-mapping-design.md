# Design — Remove the raw `preseed` config kind (#59) and expose the OS-family → config-kind mapping over the API (#61)

- **Date:** 2026-07-20
- **Issues:** [#59](https://github.com/jacaudi/booty/issues/59) (remove `preseed` kind end-to-end), [#61](https://github.com/jacaudi/booty/issues/61) (expose `familyAllowsKind` over the API)
- **Status:** Approved (design phase)
- **Prerequisite (met):** #60 `booty convert-preseed` merged to main (PR #68, `2b910e9`) — the migration aid an operator uses to turn a live raw-preseed config into a `debianconfig` before upgrading.

## Architecture / tech stack

Go 1.26 backend (`pkg/http`, `pkg/db` with `modernc.org/sqlite`, huma v2, cobra/viper), embedded React 18 + AntD 5 UI (`web/`). Both issues center on a single guard, `familyAllowsKind` (`pkg/http/render.go:34-43`) — the one authoritative statement of which authored config `kind` may serve a host of a given OS family. #59 *simplifies* its `preseed` arm; #61 *exposes* its knowledge. They are one coherent change to one seam, shipped as two sequenced phases.

> **For Claude:** REQUIRED EXECUTION WORKFLOW (follow in order):
> 1. `superpowers:using-git-worktrees` — Isolate work in a dedicated worktree
> 2. `superpowers:subagent-driven-development` — Dispatch a fresh subagent per task
> 3. `superpowers:test-driven-development` — All subagents use TDD
> 4. `superpowers:verification-before-completion` — Verify all tests pass per task
> 5. `superpowers:requesting-code-review` — Code review after each task (built in)
> 6. After all tasks: comprehensive code review on full diff from branch point (automatic). Because this is Go + a DB migration + a wire-contract change, the whole-branch review MUST be an `sr-go-engineer` pass that EXERCISES output (drives the built binary), per the booty convention that this catches cross-package/vocabulary/DRY seam bugs generic reviewers miss.
> 7. `superpowers:finishing-a-development-branch` — Complete the branch
>
> Skills carry their own model and effort settings. Do not override them.

## Sequencing and deliverable shape

**#59 before #61.** #59 changes `familyAllowsKind`'s `preseed` arm from `{preseed, debianconfig}` to `{debianconfig}`. If #61 shipped first it would expose the pre-removal mapping and then immediately have to change it, churning the wire contract and the frontend derivation twice. Doing #59 first means #61 exposes the settled, post-removal mapping exactly once.

**One design → one implementation plan → one PR, two phases.** The two issues are inseparable around `familyAllowsKind`, and #61's clean implementation (the `authoringKindsForFamily` refactor) is the natural place to also land #59's arm change. One execution branch closes both issues.

---

## Phase A — Remove the raw `preseed` config kind (#59)

### The problem

`preseed` and `debianconfig` converge on the same serve surface — both render to `text/plain` served at `/preseed` (`render.go:71-81`). They differ only in authoring format: `debianconfig` is the structured, booty-owned format (translated via `translateDebianConfig`); raw `preseed` is a flat d-i preseed pasted verbatim. The UI already stopped offering raw `preseed` as a create option (`OS_CHOICES`). #59 retires the kind from the backend now that the converter gives operators a migration path.

### A1 — Migration `0009` + fail-fast pre-flight (the crux)

A new `pkg/db/migrations/0009_drop_preseed_kind.sql` rebuilds the `configs` table (copy → drop → rename, exactly as `0004`/`0005`/`0006` did, because SQLite cannot `ALTER` a `CHECK`) with `'preseed'` dropped from the `kind` CHECK:

```sql
kind TEXT NOT NULL CHECK (kind IN ('butane','machineconfig','schematic','taloscluster','debianconfig'))
```

**Existing `kind='preseed'` rows.** The rebuild's `INSERT INTO configs_new … SELECT … FROM configs` copies every existing row. A surviving `preseed` row would fail the new CHECK — a *fail-fast*, which is the desired safety property, but the raw SQLite error (`CHECK constraint failed: configs_new`) is cryptic and would strand an operator: exactly the failure #59 warns against ("an operator with a live raw-preseed config loses the ability to edit it").

**Decision (chosen):** a **Go pre-flight in `migrate()`**, run before the migration loop, that produces a *helpful* abort:

- Gate: only when `0009` is pending (`current < 9`) **and** the `configs` table exists. The table-existence probe is required, not optional: on a fresh DB the pre-flight fires with `current = 0 < 9` *before* `0003` creates `configs`, so a bare `SELECT … FROM configs` would error `no such table`. Probe first with `SELECT 1 FROM sqlite_master WHERE type='table' AND name='configs'` and skip the check when it returns no row.
- Query `SELECT id, name FROM configs WHERE kind = 'preseed'`.
- If any rows: return an error that names `booty convert-preseed` and lists the offending config IDs/names, e.g.:
  `startup blocked: N config(s) use the removed 'preseed' kind [id=3 "web-node", id=7 "db-node"]; convert each with 'booty convert-preseed', re-create it as a debianconfig and rebind, then upgrade`
- If none: the loop proceeds and `0009` applies cleanly.

Once `0009` is applied (`current >= 9`) the pre-flight is skipped forever after — by then no `preseed` rows can exist (they would have blocked the migration).

**Documented tradeoff.** This couples one guard to the `0009` ordinal, a small and explicit dent in `migrate()`'s otherwise-generic loop. It is a single, named, well-commented pre-flight — deliberately **not** a generic "migration guard hook" framework (YAGNI/KISS): there is exactly one present consumer. The alternative (rely on the natural CHECK failure) is rejected only for its cryptic message; the fail-fast semantics are identical.

Rationale for **not** auto-converting in-migration: the converter is Go logic (`parsePreseed → mapScalars → recognizeDisk → verifyRoundTrip`) with warnings and lossy edge cases — not expressible or safe to run inside a SQL migration.

### A2 — Delete the `case "preseed"` arm in `renderConfig`

Remove `render.go:71-72` (`case "preseed": return rendered, "text/plain", "", nil`). Nothing else depends on it once A3 preserves the file path.

### A3 — Preserve the rung-4 `--preseedFile` default path via a raw-render helper

`preseed.go:57` hardcodes `renderConfig("preseed", src, vars)` for the operator-supplied server-default file (`config/preseed.cfg`, `--preseedFile`; **not shipped** in the repo — operator-supplied raw preseed text with no kind marker). This served to *unbound* Debian hosts (rung 4) is a distinct concern from the config **kind** named `preseed`.

Extract a small private helper in `pkg/http`:

```go
// renderPreseedFile executes the operator-supplied server-default preseed FILE
// (rung 4, --preseedFile) as a text/template and serves it verbatim as
// text/plain. The default file carries no config-kind marker — it is raw d-i
// preseed text — so it does NOT go through renderConfig's kind switch; this is
// its dedicated render path after the 'preseed' config kind was removed (#59).
func renderPreseedFile(source []byte, vars TemplateVars) ([]byte, error) { … }
```

`preseed.go`'s rung-4 block calls `renderPreseedFile` instead of `renderConfig("preseed", …)`. The current caller consumes `ct` from `renderConfig` and does `w.Header().Set("Content-Type", ct)`; since `renderPreseedFile` returns no content-type, the caller **hardcodes `"text/plain"`** — behavior-identical (the removed `preseed` arm always returned exactly that), and intentional, not an oversight. `withDVDMirror`, `preseedVars(store, host)` (nil-host-safe), and the template-execute path are all preserved unchanged. The `--preseedFile` flag and unbound-host serving are unchanged; `--preseedFile` is intentionally **not** retired — that would be a larger behavior change than #59 asks for.

### A4 — `familyAllowsKind` preseed arm

The `preseed` arm becomes `debianconfig`-only. This lands as part of B1's refactor (`authoringKindsForFamily`), so A4 and B1 are the same edit rather than an edit-then-re-edit.

### A5 — huma create-config enum

`api_configs.go:75`: drop `preseed` from `enum:"butane,machineconfig,preseed,schematic,taloscluster,debianconfig"` → `enum:"butane,machineconfig,schematic,taloscluster,debianconfig"`. This is the pre-handler JSON-schema gate; it and the DB CHECK are the two admission points for the kind — both must change.

### A6 — Comment / doc cleanup

Reword now-stale `preseed`-family references so they describe the single-kind reality:
- `preseed.go:21-35` (handler doc + the `familyAllowsKind("preseed", kind)` dispatch comment — the family is no longer 1:many).
- `validateConfigSource` default-arm comment (`api_configs.go:345`) and `previewVars` (`api_configs.go:409`) — drop `preseed` from the "renderable kinds" enumerations.
- `pkg/db/configs.go:14` — the struct comment `// Kind string // 'butane' | 'machineconfig' | 'preseed'` is stale; drop `preseed` (or replace with the current full set).
- `docs/CONFIGURATION.md` — `:114` (kind dialect list `butane | machineconfig | preseed`), `:240-248` ("Raw `preseed` configs remain fully supported"), and the `:40`/`:94`/`:107` `--preseedFile` mentions. The `--preseedFile` rung-4 *fallback* stays (A3), but the doc must stop presenting raw `preseed` as an authorable **config kind** and clarify the file default is now the only raw-preseed surface.
- Any other surviving docs/README references to authoring a raw `preseed`.

Note: `pkg/ostype` (`ostype.go:61`, `debian_test.go:12`, `ostype_test.go:12`) retains `ConfigKind: "preseed"` — that is the family/**serving-mechanism** name, NOT the config kind being removed. It is correct and must stay untouched; `authoringKindsForFamily`'s `case "preseed"` (B1) keys off exactly this retained family name.

### Phase A test coverage

- Migration test: a DB seeded with a `preseed` row → `migrate()` returns the helpful pre-flight error naming the converter and the row; a DB with no `preseed` rows → `0009` applies and the CHECK now rejects a direct `INSERT … kind='preseed'`.
- `renderConfig` returns the unknown-kind error for `"preseed"` (arm gone).
- `renderPreseedFile` renders + serves the default file unchanged (golden/round-trip against a representative raw preseed with a template var).
- `create-config` with `kind:"preseed"` is rejected by the huma enum (422) before the handler runs.

---

## Phase B — Expose the mapping over the API (#61)

### The problem

The OS-family → authored-kind compatibility rule lives only in `familyAllowsKind`. The frontend keeps a hand-maintained copy in `web/src/api/configKinds.ts` (whose header comment already flags the duplication and names this issue as the fix). Drift fails safe — an unknown server kind is simply not offered; a tightened guard yields a loud 422 on bind — so this is a duplication-retirement, not a data-integrity emergency. Don't over-build the API.

### B1 — Single-source the mapping as an enumerable

Today `familyAllowsKind` answers a boolean. To *enumerate* the allowed kinds for the API, invert the authoritative representation into a list and derive the boolean from it. Add, beside the guard in `render.go`:

```go
// authoringKindsForFamily is the AUTHORITATIVE list of authored config kinds a
// family accepts (family ConfigKind == serving mechanism). familyAllowsKind and
// the /families API both derive from this single source.
func authoringKindsForFamily(familyConfigKind string) []string {
	switch familyConfigKind {
	case "ignition":
		return []string{"butane"}        // author butane, serve ignition
	case "preseed":
		return []string{"debianconfig"}  // #59: raw preseed retired
	default:
		return []string{familyConfigKind} // machineconfig, …
	}
}

func familyAllowsKind(familyConfigKind, kind string) bool {
	return slices.Contains(authoringKindsForFamily(familyConfigKind), kind)
}
```

`familyAllowsKind` keeps its exact signature and behavior across all four production call sites (`resolve.go:30`, `resolve.go:65`, `api_hosts.go:38`, `preseed.go:35`); the mapping stays single-sourced. Representing the value as a *list* is the honest shape of the knowledge (the guard is inherently set-membership, and `preseed` was 1:many within living memory), not speculative generality.

### B2 — Extend `FamilyDTO` on the existing `/families` endpoint

No new endpoint. Add one field to `api_catalog.go`'s `FamilyDTO`:

```go
type FamilyDTO struct {
	Name           string   `json:"name"`
	ConfigKind     string   `json:"configKind"`     // serving mechanism (unchanged)
	AuthoringKinds []string `json:"authoringKinds"` // NEW: authored kinds this family accepts
}
```

The existing `list-families` handler populates it via `authoringKindsForFamily(f.ConfigKind)`. `/os` (OS → family + requiredParams) already exists and is unchanged. Together `/os` + `/families` give the frontend everything it needs.

### B3 — Frontend derives the compatibility rule from the API; deletes the duplication

`web/src/api/configKinds.ts` stops hard-coding the *compatibility rule* and derives it from `/os` + `/families`:

- **`BOOT_CONFIG_KINDS`** (servable/authorable kinds) = union of every family's `authoringKinds`.
- **Bind-compatibility** (`HOST_OS_KINDS`: host OS → allowed kinds) = host OS → its family (`/os`) → that family's `authoringKinds` (`/families`). This is `familyAllowsKind ∘ osFamily`, now sourced from the server.
- **Create-picker kind values** come from the same derivation (OS → family → `authoringKinds`).

**What stays in the frontend — UI presentation, not server knowledge:**
- Display labels (`KIND_OS_NAMES`, e.g. "Flatcar / Fedora CoreOS").
- The create-picker's grouping of `flatcar` + `fedora-coreos` into a single butane entry (a UI grouping, not a server fact — the server lists them as two OSes in one `ignition` family).
- `SCHEMATIC_KIND` / `TALOSCLUSTER_KIND` — non-servable, page-owned constants that no serving path renders.

So #61 retires the **mapping** duplication; it does **not** flatten the presentation layer. The create-picker's *labels and grouping* remain UI copy while its *kind values* become API-derived.

**Vocabulary gap — the `coreos` boot-name alias must be preserved (do not let it vanish).** `HOST_OS_KINDS` today keys on booty's *boot* vocabulary and includes `coreos` (`configKinds.ts:92`, `coreos: ['butane']`) because a booted CoreOS host has `host.OS == "coreos"`. But `/os` and `/families` emit the *canonical* ostype names (`flatcar`, `fedora-coreos`, `talos`, `debian`) — the `CacheNameToCanonical` bridge that reconciles `coreos → fedora-coreos` lives **server-side only** (`resolve.go:90`). A naive "host OS → `/os` family → `authoringKinds`" derivation therefore has **no entry for `coreos`**, silently degrading `kindsForHostOS("coreos")` from today's precise `['butane']` to the permissive full union. This "fails safe" (union + a loud 422 on a wrong bind) but is a real fidelity regression for exactly the mapping #61 sets out to preserve. **Resolution:** the frontend keeps a tiny declared `coreos → fedora-coreos` alias as UI vocabulary — the same class of frontend-owned boot-name knowledge as the `flatcar`+`fedora-coreos` grouping it already keeps — and maps `host.OS` through it before the API lookup. (The alternative — teaching `/os` to emit the boot-vocabulary alias — pushes UI vocabulary into the server contract and is not preferred.)

**Dead `preseed` presentation entries to remove.** Post-#59 no `preseed`-kind rows can exist (the migration blocks upgrade while any remain), so these become unreachable and are dropped: the `'preseed'` member of `BOOT_CONFIG_KINDS` (`configKinds.ts:26`), `HOST_OS_KINDS.debian`'s `'preseed'` (`:89` → `['debianconfig']`), and `KIND_OS_NAMES.preseed` (`:67`). The existing web tests assert the old shape and are updated with the code: `configKinds.test.ts:15` (`BOOT_CONFIG_KINDS` no longer contains `preseed`), `:27`/`:46` (`isBootConfigKind('preseed')`), `:65` (`kindsForHostOS('debian')` → `['debianconfig']`), and `:53` (`osNameForKind('preseed')`) — the last needs a keep-as-legacy-fallback-vs-delete decision made explicit when the plan lands (recommend delete, since the value is now unreachable).

**One real consequence:** `configKinds.ts` moves from static/synchronous to depending on a fetch (a loader/query for `/os` + `/families`). This is a contained frontend change; the create picker and bind Selects gain an async data dependency. Existing fail-safe behavior is preserved — while the fetch is pending or on a miss, the UI can fall back to offering the full union (as `kindsForHostOS` does today for unknown OSes).

**Post-#59 the mapping is 1:1 per family**, so OS → single authoring kind is unambiguous for the picker. If a family ever accepts >1 authoring kind again, the picker would need a disambiguation rule — a latent constraint, explicitly **not** built now (YAGNI).

### Phase B test coverage

- Go: `authoringKindsForFamily` returns the expected list per family; `familyAllowsKind` is unchanged for every production call-site pair, with the single **intended** exception that `("preseed","preseed")` tightens `true → false` (A4/#59). The existing `render_test.go:18` assertion (`{"preseed","preseed",true}`) is updated to `false` to record that tightening.  `/families` response includes `authoringKinds` matching the guard.
- Web: `configKinds` derivation maps a `/os` + `/families` fixture to the correct `BOOT_CONFIG_KINDS`, bind-compat sets, and picker options; the duplicated static tables are gone; pending/miss falls back to the union. `tsc` clean.

---

## Existing tests and fixtures to update (not just net-new coverage)

Removing the kind breaks fixtures across Go and web suites that currently create or assert `preseed`. The plan must treat these as first-class tasks — an implementer that only adds new tests will hit red suites:

- `pkg/db/migrate_test.go:333-357` — seeds `INSERT INTO configs … kind 'preseed'` at `user_version=5`, then `Open()` applies through `0009`. Once `0009` + the pre-flight exist, that `Open()` now **aborts** on the seeded row. Rework this test to assert the *new* behavior (pre-flight blocks with the converter-naming error), and add a companion that seeds only `debianconfig` and verifies clean migration.
- `pkg/http/render_test.go` — `:18` (`{"preseed","preseed",true}` → `false`, per B1), and `:69`/`:99` which call `renderConfig("preseed", …)` directly (the arm is gone → now the unknown-kind error). Move the render-success (`:69`) and bad-template (`:99`) intents onto `renderPreseedFile`.
- `pkg/http/serving_test.go:160`, `pkg/http/preseed_test.go:76,144` — `CreateConfig(…, "preseed")` fixtures; the `0009` CHECK rejects them. Migrate to `debianconfig` (keeping the bound-vs-unbound / DVD-mirror intents these tests exercise).
- `pkg/http/api_configs_test.go:66,81,99,133` — POST `kind:"preseed"` fixtures; the huma enum (A5) now 422s them. Migrate to `debianconfig`, and add one test asserting `kind:"preseed"` is rejected by the enum.
- `web/src/api/configKinds.test.ts:15,27,46,53,65` — assert the pre-removal shape; updated alongside B3 (see the "Dead `preseed` presentation entries" list above).

## What this design deliberately does NOT do (YAGNI)

- No generic "migration pre-flight guard" framework — one named check for `0009`.
- No new API endpoint — `/families` is extended.
- No auto-conversion of `preseed` rows — the CLI converter is the operator-run migration aid.
- No retirement of `--preseedFile` or the rung-4 default-file serving path.
- No frontend disambiguation UI for multi-kind families — none exist post-#59.

## Risks / call-outs

- **Two admission points for the kind enum** (booty gotcha): both the huma `enum:` tag (A5) and the DB CHECK (A1) must drop `preseed`, or the two gates disagree.
- **`preseed.go:57` is the highest-risk removal site**: removing the `renderConfig` `preseed` arm (A2) without the `renderPreseedFile` extraction (A3) breaks the unbound-Debian-host default path. A3 must land with A2.
- **Wire contract change** (`FamilyDTO` gains a field): additive and backward-compatible; existing consumers ignore the new field.
- **Whole-branch review must drive the binary** (booty convention): exercise `booty convert-preseed`-blocked startup, an unbound-host `/preseed` fetch (default file), and the `/families` response shape.
