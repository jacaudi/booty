# Multi-Control-Plane Cluster Import

**Type:** Design
**Date:** 2026-07-18
**Feature:** multi-controlplane-import

**Goal:** Let the cluster **import** flow adopt an existing Talos cluster that has **more than one control-plane host**, providing each control-plane node's own `controlplane.yaml` (per-host configs), in a single import action.

---

## 1. Context & motivation

Booty's cluster **import** flow (`POST /api/v1/clusters/import`, `pkg/http/api_clusters.go`) adopts an existing Talos cluster from an uploaded `controlplane.yaml`. Today it accepts exactly **one** control-plane host: the request body is `{name, controlplane, controlplaneMac}` (`api_clusters.go:616-621`, singular `ControlplaneMAC string`), and the import modal (`web/src/views/ClustersView.tsx:186-187`) has a single MAC `Input` + one config textarea.

Real HA clusters run 3 (or 5) control-plane nodes, and those nodes are **not** byte-identical — a common case (the motivating one) is *"2 nodes share a config except disk IDs, 1 node is separate."* An operator adopting such a cluster must currently import with one control-plane host, then add the rest one-at-a-time through the generic per-cluster "assign member" panel — awkward and easy to get wrong.

## 2. Goals / Non-goals

**Goals**
- Import a cluster with **1..N control-plane hosts** in one action.
- Each control-plane host supplies its **own** `controlplane.yaml` (per-host, verbatim) so per-node differences (disk IDs, etc.) are captured faithfully.
- All-or-nothing: a bad entry rejects the whole import with no orphan cluster row.

**Non-goals**
- **Create-cluster** flow and **worker** assignment are unchanged. Workers are still added after import via the existing per-cluster assign-member panel (`AssignMemberPanel`, `ClustersView.tsx:240-266`) — which already accepts multiple members.
- **No VIP support.** Import preserves the `endpoint` from the pasted configs (an existing HA cluster already fronts its API with a VIP/LB). VIP wiring belongs to the *create* flow / a future multi-arch/HA-authoring effort, not here.
- No enforcement of an odd control-plane count (etcd quorum is the operator's concern; this adopts an existing cluster as-is).
- No schema change.

## 3. Current state (grounded)

- **Schema is already N-capable.** Cluster membership is three nullable columns on `hosts` (`cluster_id`, `machine_type`, `node_config_id`; `pkg/db/migrations/0005_clusters.sql:44-46`), and frozen per-host configs live in `cluster_node_configs` keyed by `(mac, revision)`. There is **no** cardinality constraint on how many hosts of a cluster have `machine_type='controlplane'`. `ListClusterMembers` (`pkg/db/clusters.go:102-117`) already returns all members. No migration is needed.
- **The single-host limit is only in the import DTO/handler + its UI form.** The generic `add-cluster-member` path (`api_clusters.go:508-548`) has no control-plane count cap.
- **Import extraction mechanics** (`pkg/http/clusterimport.go`): `parseImportedConfig` loads + `Validate`s the config; `extractClusterFields` requires a **controlplane** config and reads `endpoint`, Talos version (from the install image), k8s version (from the apiserver image), and schematic. The import handler reconstructs the **shared secrets bundle** from the control-plane config via `secrets.NewBundleFromConfig(prov)` (`api_clusters.go:665`), encrypts it (requires `--secretsKey`), creates the cluster, then `freezeAndBind(..., "controlplane", schematic, talosVersion, <bytes>, "imported", "")` binds the host to the verbatim bytes.
- All host resolution/validation happens **before** `CreateCluster`, so a guarded/failed import never leaves an orphan cluster row (`api_clusters.go:644-661`).

## 4. Design

### 4.1 Input model
A repeatable list of control-plane entries, each `{ mac, controlplane }` (the node's own `controlplane.yaml`). The operator pastes each node's actual config; disk-ID and any other per-node differences are captured because each is stored verbatim. All control-plane nodes of a Talos cluster share one **secrets bundle** (cluster CA/identity) but may carry distinct per-node config — this model preserves both.

### 4.2 API / DTO (`POST /clusters/import`)
Replace the singular body with a list (clean replacement — pre-release internal API, no back-compat shim; UI + tests updated together):
```
Body {
  name: string
  controlPlanes: [ { mac: string, controlplane: string }, ... ]   // len >= 1
}
```
Response unchanged (`ClusterDTO`).

### 4.3 Backend handler — validate-all-then-create (transactional)
1. Require `len(controlPlanes) >= 1` and each `{mac, controlplane}` non-empty (422 otherwise).
2. For **every** entry: `parseImportedConfig` + `extractClusterFields` — each must be a valid **controlplane** config (reuse existing helpers; worker configs rejected as today). Require resolvable Talos + k8s versions (422 otherwise).
3. Resolve **every** MAC up front: not-found → 404; not Talos → 422; already in any cluster → 422 (duplicate MACs within the request → 422). No cluster row exists yet at this point.
4. **Same-cluster guard (new):** all pasted configs must belong to the *same* cluster. Compare a stable cryptographic identity — the **issuing CA cert bytes** (`prov.Cluster().IssuingCA()`, the material the secrets bundle is built from, so guard and bundle-source agree), with `cluster.id` (`prov.Cluster().ID()`) as a cheap secondary — and reject mixed clusters (422). **Reject a config whose identity is empty/missing** (else all-empty configs false-match). Also require `endpoint` to agree across configs (422 on mismatch — always correct for one cluster). Do **not** reject on Talos/k8s version mismatch: a cluster mid rolling-upgrade legitimately runs mixed versions — take cluster-level versions from the first entry.
5. **Hoist all fail-prone work before creating the cluster** (so no failure can leave a partial cluster): reconstruct + encrypt the shared secrets bundle from the **first** entry's config (`--secretsKey` required, else 422), and pre-run each entry's fallible preparation — notably `EnsureSchematicTarget` (proven fallible by `TestVersionBumpPrecacheFailureDoesNotCommit`) and per-host config prep — so everything that can fail has succeeded before any row is written.
6. `CreateCluster(name, endpoint, talosVersion, k8sVersion, encBundle)` **once**, then loop entries binding each host: `freezeAndBind(deps, cluster.ID, mac, "controlplane", schematic, cluster.TalosVersion, []byte(entry.controlplane), "imported", "")` — each frozen to **its own** verbatim bytes; per-entry `schematic` falls back to `--talosSchematic` when the install image doesn't encode one.
7. **Compensating rollback:** if any bind fails after `CreateCluster`, unbind the already-bound hosts and delete the cluster row via a **store-level** `DeleteCluster` (bypassing the HTTP `delete-cluster` handler, which is 403 until P10), then return the error. On success return `ClusterDTO`.

Correcting the earlier framing: validation (steps 1–4) is pre-create, but binding (step 6) is **post-create** and can fail — so the all-or-nothing guarantee comes from hoisting fail-prone work ahead of `CreateCluster` (step 5) **plus** the compensating rollback (step 7), not from validation alone (SGE B1/B2).

### 4.4 UI (`ClustersView.tsx` import modal + `clusters.ts`)
- Replace the single MAC `Input` + single `controlplane.yaml` textarea with a `Form.List` of removable rows: each row = a MAC `Input` + a `controlplane.yaml` `TextArea`, with an **"Add control-plane host"** button. Minimum one row (the first is non-removable / add-only).
- `clusters.ts` `importCluster` sends `{ name, controlPlanes: [{mac, controlplane}] }`.
- Surface a clear error toast for the new 422 cases (mixed clusters, endpoint mismatch, a MAC already in a cluster, empty/duplicate entries) so the all-or-nothing failure is understandable.

### 4.5 Data model
No change. Each imported control-plane host becomes a `hosts` row with `cluster_id` set, `machine_type='controlplane'`, and a frozen `cluster_node_configs` revision for its verbatim bytes.

## 5. Error handling (summary)
- `422` — empty list (huma `minItems:"1"`) / empty entry; any config not a valid controlplane config; unresolvable Talos/k8s version; a MAC not Talos or already in a cluster; duplicate MAC in the request; configs from different clusters (or empty cluster identity); endpoint mismatch across configs; missing `--secretsKey`.
- `404` — any control-plane MAC not found.
- Fail-prone work is hoisted before `CreateCluster`; a bind failure after create triggers compensating rollback (unbind + store-level `DeleteCluster`) → no orphan or partial cluster on any failure.

## 6. Testing
- **Backend handler:** happy path with 2–3 control-plane hosts (assert N members bound as `controlplane`, each frozen to its own verbatim bytes — including a disk-ID difference between two entries); all-or-nothing rejection for each failure mode (one unknown MAC, one already-claimed host, one worker config, one mixed-cluster config, endpoint mismatch, duplicate MAC in the request) asserting **no cluster row created**; missing-`--secretsKey` rejection. **Mid-loop rollback test** (bind fails on host #2 of 3 — inject a fallible `freezeAndBind`/`EnsureSchematicTarget`): assert the cluster row is deleted and no host stays bound. **Same-cluster guard test** against the real `IssuingCA()`/`ID()` accessors, including the empty-identity-bypass case. **Bundle-from-first test**: the reconstructed secrets bundle matches the (identical) cluster material in every entry.
- **UI:** the import modal adds/removes control-plane rows and submits the array; validation blocks submit with zero rows.

## 7. Decisions
- **Per-host configs** (operator pastes each node's `controlplane.yaml`) — chosen over a shared config or base+patch because import adopts an existing cluster whose full per-node configs the operator already holds, and it captures heterogeneity (disk IDs) faithfully.
- **Same-cluster guard** added — prevents accidentally importing configs from different clusters into one booty cluster.
- **All-or-nothing via hoist + rollback** — fail-prone work runs before `CreateCluster`, and a post-create bind failure rolls back (unbind + store-level `DeleteCluster`, bypassing the P10-gated 403 handler). Validation alone is insufficient because binding is post-create (SGE B1/B2).
- **No back-compat** for the old singular body shape (pre-release internal API).
- **Import stays control-plane-only**; workers via the existing member panel.
- **huma-idiomatic validation** — `minItems:"1"` + `required` tags on the `controlPlanes` array instead of manual empty checks (m2).
- **422 for all validation/conflict cases** — consistent with the existing create/import/add-member handlers, not 409 (m3).

## 8. Risks / open items
- Same-cluster identity uses `prov.Cluster().IssuingCA()` cert bytes (+ `ID()` secondary), verified present on the Talos `config.ClusterConfig` interface; empty/missing identity is rejected to prevent an all-empty false-match.
- Endpoint consistency is enforced (reject mismatch); Talos/k8s version mismatch is **not** rejected (mixed versions are legitimate mid rolling-upgrade) — cluster-level versions come from the first entry.

## 9. SGE design review (2026-07-18) — folded
Independent senior-Go-engineer review; verdict **AMEND-BEFORE-PLANNING**; all findings folded above:
- **B1** — "all-or-nothing" was false (the bind loop is post-`CreateCluster` and `freezeAndBind`/`EnsureSchematicTarget` can fail). Fixed: hoist fail-prone work before create + compensating rollback (§4.3 steps 5–7).
- **B2** — partial failure was unrecoverable (HTTP `delete-cluster` is 403 until P10; `name` is UNIQUE). Fixed: rollback uses a **store-level** `DeleteCluster` that bypasses the 403 guard.
- **M1** — the guard cited a nonexistent `prov.Cluster().CA()`; corrected to `IssuingCA()` + `ID()`, with empty-identity rejection.
- **M2** — added the guarantee-proving tests (mid-loop rollback, real-accessor + empty-identity, duplicate-MAC, bundle-from-first).
- **m1/m2/m3** — don't reject on version mismatch; huma `minItems`/`required`; 422 for consistency.
Confirmed sound (no change): shared-bundle-from-first, verbatim per-host binding, the guard's necessity (real footgun in a multi-paste UI, not YAGNI), import-only scope, dropping DTO back-compat.
