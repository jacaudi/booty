# P6 — Talos Cluster Authoring — Design

**Date:** 2026-07-04 · **Slice:** P6 (v1 roadmap) · **Issues:** [#25](https://github.com/jacaudi/booty/issues/25), [#32](https://github.com/jacaudi/booty/issues/32) · **PR target:** `jacaudi/booty:main` · ships **after** P5; depends on **P5** (per-node schematic), **P4** (machineconfig serving, roles, host binding), and the **cache reconciler** (retention pin).

> **Session note:** designed 2026-07-04 in a brainstorm reconciled against **current Talos v1.13**, with the two load-bearing areas — the `pkg/machinery` generation API and the secrets internals — **verified at the source level** by two research passes (§14, citations inline). The original roadmap framed P6 as "talhelper vs topf"; that framing was rejected in favour of **native generation** (§1.2). All decisions **D1–D10 USER-APPROVED** (each as recommended); alternatives on file in §16. This is the largest v1 slice and may be split at plan time (see §17).
>
> **SGE review amendment (2026-07-06):** an `sr-go-engineer` design review (verdict *AMEND-BEFORE-PLANNING*, 0 blocking, no approved decision flagged defective) was folded. Gap-closing amendments: **I1** pin a cluster member's netboot version to `cluster.talos_version` (§5/§6.3/§8 — **Option A, user-confirmed 2026-07-06**), **I2** member schematic/version single-sourced through regenerate (§6.3/§6.5), **M2** `--secretsKey` startup fail-fast (§7), **M3** §8 retention pin reworded as a *new* never-evict union member, **ADOPT** `config.Provider.Validate()` on import (§6.2/§10), **M5** note `cluster_node_configs` deliberately unshared (§3). The talhelper/topf/Talos idea sweep **validated** P6's native-generation direction and recommended **rejecting** any integration (confirms §1.2).

---

## 1. Context & problem

### 1.1 What's missing

booty can serve a Talos machineconfig (P1c/P4) and manage schematics (P5), but it has **no way to author a Talos cluster.** Today an operator hand-writes each node's full machineconfig (or runs `talosctl gen config` out-of-band) and pastes the result into booty as a P4 passthrough `machineconfig` config. There is no cluster concept, no secrets management, no notion of control-plane vs worker, and no way to add a node to a running cluster from booty.

P6 is the **butane→ignition analog for Talos clusters**: author one high-level cluster spec, and booty *generates* each node's machineconfig from durable inputs. It reuses booty's existing seams rather than inventing a parallel model — the cluster's members are **hosts booty already tracks**, the machineconfig is served through the **existing `/machineconfig?mac=`** path, and per-node images come from **P5 schematics**.

| Layer | Flatcar/CoreOS (shipped) | Talos (P5 + P6) |
|---|---|---|
| High-level authored | butane YAML | **cluster spec** (endpoint, versions, patches) + host bindings |
| Translation engine | `coreos/butane` (vendored) | **`siderolabs/talos/pkg/machinery`** (vendored) + persisted secrets bundle |
| Low-level served | ignition (translated) | **machineconfig** per host, **generated once and frozen** (§5) |

Prior art: this is precisely what an operator otherwise stands up with **Matchbox** (the canonical bare-metal Talos flow) — static hand-written `controlplane.yaml`/`worker.yaml` matched to machines by group selectors, served at the `talos.config=` URL. P6 is "Matchbox, but the per-node config is *generated* from your cluster spec and bound through hosts booty already tracks."

### 1.2 Why not talhelper / topf (the roadmap's original framing)

- **topf** (postfinance) is cluster *lifecycle orchestration* — its own host model + bootstrap/reset/apply. Integrating it means running two competing management planes; its Factory-wrapping is already P5's job. → borrow ideas, do not integrate.
- **talhelper** (budimanjojo) generates configs from a `talconfig.yaml` **node list** — which duplicates booty's host registry + binding, and is a CLI/heavy dependency fighting booty's single-static-server shape.
- Both sit on the **same official library** underneath: `pkg/machinery`. P6 vendors that library directly (as booty already does with butane) and expresses authoring in booty's idiom. This is the KISS/DRY choice — no subprocess, no parallel host model, no schema-churn tracking (§4).

### 1.3 The operator workflows this slice must serve

1. **Create (greenfield)** — define a cluster (name, endpoint, pinned Talos + Kubernetes version); booty mints an encrypted secrets bundle.
2. **Adopt (import)** — upload an existing `controlplane.yaml` (+ optional per-role/per-node `worker.yaml`s); booty reconstructs the cluster (secrets, endpoint, versions, per-role schematics) and stores the imported bytes **verbatim** for byte-identical recreation.
3. **Assign / add a node** — bind a known host to the cluster as `controlplane` or `worker` (pick its P5 schematic, optional per-host patch); booty generates + freezes its machineconfig and pre-caches its image. The node netboots and **joins**.
4. **Remove a node** — unassign a host; booty stops provisioning it (in-cluster drain/reset is operator-driven — §6.4).
5. **Edit** — change cluster-wide/role patches or bump the pinned version; booty regenerates + freezes new per-node revisions; hosts roll forward on explicit re-bind.
6. **Recreate (DR)** — a wiped node re-netboots and gets its **byte-identical** frozen config back.

---

## 2. Goals / non-goals

**Goals**

1. A **`clusters`** entity + a per-host membership (`hosts.cluster_id`, `hosts.machine_type`) + an **encrypted frozen node-config store** (`cluster_node_configs`). Migration `0005` (additive).
2. Vendor `github.com/siderolabs/talos/pkg/machinery`; generate per-node machineconfigs from a persisted **secrets bundle** + pinned **version contract** + layered **patches** + per-node **schematic** (§4).
3. **Materialize-and-freeze serving** (§5): every served node config is an immutable, content-hashed, age-encrypted artifact — generated-then-frozen or imported-verbatim — replayed verbatim at boot for a **byte-identical** guarantee.
4. **Import/adopt** an existing cluster from its `controlplane.yaml` (§6.2).
5. **Add/remove nodes** to a live cluster (§6.3, §6.4).
6. **age encryption at rest, fail-closed** for the secrets bundle *and* frozen node configs (§7).
7. **Retention pin**: cluster-referenced `(schematic, version)` pairs never evicted by the reconciler (§8).
8. Cluster-general (single-node = degenerate one-host controlplane) (D9).

**Non-goals (YAGNI — see §17)**

- **Lifecycle orchestration** — `talosctl bootstrap`/`upgrade`/`reset`, `kubectl drain`, etcd membership management, kubeconfig distribution. booty authors + serves; the operator drives the cluster. (D5)
- **Secrets `Rotate`** (CA re-mint + rolling all nodes) — heavy, risky live-cluster surgery; deferred. `Export` (download `secrets.yaml`) is the thin escape hatch, and an externally-rotated bundle can be re-imported. (D5)
- **Cluster health monitoring** — member status is *derived* from the existing `host.Booted`, not a new subsystem. (D5)
- **Reverse-engineering patches** from a full imported config (fragile) — import extracts durable inputs + stores bytes verbatim; bespoke tweaks are re-expressed as patches (§6.2). (D8)
- **JSON6902 patches on multi-document configs** — the library rejects them (§9); strategic-merge is the multi-doc path.
- `DELETE /clusters/{id}` enabled (wired-but-403 until P10).

---

## 3. Data model

**Migration `0005`** (additive; P5 was `0004`):

```sql
CREATE TABLE clusters (
  id              INTEGER PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  endpoint        TEXT NOT NULL,              -- controlplane URL/VIP, e.g. https://10.0.0.10:6443
  talos_version   TEXT NOT NULL,              -- pinned version contract, e.g. v1.13.5 (reproducibility #1)
  k8s_version     TEXT NOT NULL,              -- pinned, e.g. v1.32.0
  spec_config_id  INTEGER REFERENCES configs(id),   -- taloscluster-kind config: cluster-wide + role patches
  secrets_enc     BLOB NOT NULL,              -- age-encrypted secrets bundle (§7)
  created_at      TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

ALTER TABLE hosts ADD COLUMN cluster_id   INTEGER REFERENCES clusters(id);   -- NULL = not a cluster member
ALTER TABLE hosts ADD COLUMN machine_type TEXT;                              -- 'controlplane' | 'worker' | NULL

CREATE TABLE cluster_node_configs (           -- the frozen, encrypted per-host machineconfig revisions
  id           INTEGER PRIMARY KEY,
  mac          TEXT NOT NULL,
  cluster_id   INTEGER NOT NULL REFERENCES clusters(id),
  revision     INTEGER NOT NULL,
  config_enc   BLOB NOT NULL,                 -- age-encrypted full machineconfig bytes (embeds CA material)
  sha256       TEXT NOT NULL,                 -- content hash of the PLAINTEXT bytes (integrity / change detection)
  source       TEXT NOT NULL,                 -- 'generated' | 'imported'
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(mac, revision)
);
-- a host serves its ACTIVE frozen revision; pointer mirrors P4's active_revision idiom
ALTER TABLE hosts ADD COLUMN node_config_id INTEGER REFERENCES cluster_node_configs(id);
```

- A host is in **at most one** cluster → columns on `hosts` (mirroring P4's `config_id`), not a members table (KISS).
- The cluster's **schematic is per-node** — it lives on each member's `host.Schematic` (P5), *not* on the cluster. Control-planes and workers legitimately differ (minimal CP; iSCSI/GPU workers).
- The **spec** (`taloscluster`-kind config, reusing P4's configs table + revisions) holds cluster-wide + role patches **only** — never node identity (that's the host binding).
- `cluster_node_configs` is a **dedicated encrypted store** (not P4's plaintext `config_revisions`) because generated CP configs embed CA private keys. Keeping it separate leaves P4 plaintext/simple and contains encryption to P6 (D10).
  - **Deliberately unshared with P4 (SGE M5).** It reuses P4's revision + active-pointer *shape* (`node_config_id` mirrors `config_id`) but that is shared **shape, not shared knowledge**: encrypted per-host frozen bytes vs. plaintext per-config source, with different change-drivers. The plan must **not** try to force-merge the two stores — the duplication is correct per DRY, and D10's isolation rationale stands.

---

## 4. The generation engine (verified `pkg/machinery` API)

Vendor **one module**, `github.com/siderolabs/talos/pkg/machinery`, pinned to the v1.13 tag. Verified canonical flow (source-level, §14-A):

```go
// secrets: mint (greenfield) or reconstruct (import)
bundle, _ := secrets.NewBundle(secrets.NewFixedClock(fixedTime), config.TalosVersion1_13) // greenfield
bundle, _ := secrets.NewBundleFromConfig(secrets.NewFixedClock(fixedTime), importedCPCfg) // import
// or secrets.LoadBundle(path) for an uploaded secrets.yaml

in, _ := generate.NewInput(clusterName, endpoint, k8sVersion,   // name/endpoint/k8sVer are POSITIONAL
    generate.WithSecretsBundle(bundle),
    generate.WithVersionContract(config.TalosVersion1_13),       // pins field/default selection
    generate.WithInstallImage("factory.talos.dev/installer/"+schematicID+":"+talosVersion),
    generate.WithAllowSchedulingOnControlPlanes(singleNode),     // single-node degenerate case
)
prov, _ := in.Config(machine.TypeControlPlane)  // or machine.TypeWorker → config.Provider (multi-doc)
patched, _ := configpatcher.Apply(configpatcher.WithConfig(prov), patches) // strategic-merge + JSON6902, in order
out, _ := patched.(configpatcher.Output).Bytes()   // → the machineconfig bytes we freeze
```

Verified specifics that shape the design:
- **Determinism requires a fixed clock + fixed bundle + pinned contract** — `NewBundle` mints certs at `Clock.Now()`; `NewFixedClock` pins the "not-before" so regeneration is stable. The contract gates which fields/defaults land.
- **Import reconstructs the full bundle from `controlplane.yaml`** via `secrets.NewBundleFromConfig` (worker configs can't — §6.2, §14-B).
- **Imported bytes are preserved verbatim** by `container.NewReadonly(bytes)` (its `Bytes()` returns the original source YAML) — so imported-verbatim serving has zero reserialization risk.
- **Multi-document** configs are first-class (`container.New(documents...)`, `Provider.Documents()`); booty treats the output as **opaque bytes** and never parses the Talos schema (P4 D6 extended — the multi-doc migration is exactly the churn to avoid).

Transitive deps inherited (for the plan): `siderolabs/crypto/x509`, `evanphx/json-patch`, `ghodss/yaml`, `go.yaml.in/yaml/v4`.

---

## 5. Serving model — materialize-and-freeze (byte-identical)

**Why not render-at-serve.** Regeneration is byte-identical *only* given an identical machinery binary — Talos keys reproducibility on the **version contract, not the tool version**, and is explicitly silent on same-contract/different-binary byte-identity (serialization order, formatting, un-gated defaults can drift; the only hard byte-identical guarantee Talos makes is for *disk images*, §14-B5). A booty upgrade that bumps vendored machinery could re-emit an *unchanged* cluster's config differently. A **guarantee** therefore cannot rest on regeneration — it must freeze the exact bytes.

**The model:**
- Every served node config is an **immutable, content-hashed, age-encrypted revision** in `cluster_node_configs` — **generated-then-frozen** (greenfield/edit) or **imported-verbatim** (adopt).
- **Serving = verbatim byte-replay.** A new top rung in `handleMachineConfigRequest` (`pkg/http/machineconfig.go`):

```
GET /machineconfig?mac=…
  ├─ host has an active node_config_id (cluster member)?
  │     YES → decrypt cluster_node_configs.config_enc → write bytes verbatim   ← byte-identical, no generation
  └─ NO  → existing P4 precedence (bound machineconfig config → default file)  ← unchanged
```
  This is **additive** (existing rungs untouched, run only for non-members) and, because it is dumb byte-replay rather than hot-path generation, it is **lower** boot risk than render-at-serve.
- **Regeneration on explicit change only** (edit patches / bump version / re-bind) → new frozen revision; the host's `node_config_id` advances when the operator rolls it forward. Byte-identity holds until *you* choose to change it.

> Deliberate, documented divergence from Talos's "generate, apply, discard — never commit" guidance: that guidance assumes reproducibility holds and targets git-hygiene; booty is the provisioning system with a hard byte-identical requirement, so it stores the bytes (encrypted). (D3.)

**Netboot version must be pinned too (SGE I1 — Option A, user-confirmed 2026-07-06).** The freeze above pins the *served machineconfig* (whose `machine.install.image` carries `cluster.talos_version`). But the **netboot kernel/initramfs** are selected separately by `pkg/tftp` `bootTokens` (`tftp.go:290-299`) via `cache.NewestCached("talos", arch, {schematic})` — with **no per-host version pin**. When the reconciler has a newer minor cached for that schematic (the normal state of a rolling `--talosRetainMinors` window above a pinned cluster), a member would **netboot the newer maintenance initramfs and install the older pinned version** — self-healing on reboot (Talos tolerates boot≠install version at initial install), but the reproducibility/byte-identical promise would otherwise hold only for the machineconfig, not the boot kernel (a DR-recreate surprise). **Resolution:** for a cluster member (`host.cluster_id` set), `bootTokens` sources the Talos netboot version from **`cluster.talos_version`** — a per-member version pin consulted alongside the existing `host.Schematic` override at `tftp.go:292` — instead of `NewestCached`. Non-members are unchanged. This makes the pin take effect end-to-end (boot kernel *and* install image). This adds P6 plan tasks: plumb `cluster_id → talos_version` into the `tftp` boot path + tests.

---

## 6. Cluster lifecycle & membership

### 6.1 Create (greenfield)
`POST /clusters {name, endpoint, talosVersion, k8sVersion}` → mint bundle (`NewBundle` + `NewFixedClock`) → age-encrypt → store. No members yet; no node configs yet.

### 6.2 Adopt (import) — byte-identical
`POST /clusters/import` with a **`controlplane.yaml`** (required) + optional `worker.yaml`(s):
- **Secrets:** `secrets.NewBundleFromConfig(controlplane.yaml)` → full bundle → encrypt + store. Verified: a worker config carries **crt-only** CAs and omits etcd/aggregator/SA keys, so **worker configs alone cannot reconstruct a cluster** — the CP config is mandatory (§14-B1/B2).
- **Cluster fields:** parse `cluster.controlPlane.endpoint`, the Talos/K8s versions, and each file's `machine.install.image` → endpoint, pinned versions, and per-role **schematic** (registered as P5 schematic configs + retention-pinned targets — satisfies "images can be cached").
- **Frozen configs:** store each imported file **verbatim** (`source='imported'`) as the initial frozen revision, mapped **file → host(s)** — one file per role (shared; per-node identity still comes from DHCP/runtime + the `?mac=` query, exactly as before) *or* one file per node (each host its exact original bytes). → **byte-identical recreation** (satisfies "nodes can be recreated").
- v1 import does **not** reverse-engineer patches from a full config (fragile); the stored verbatim bytes ARE the fidelity guarantee, and future edits re-express tweaks as patches (D8).
- **Validate on import (SGE ADOPT):** because P6 already vendors `pkg/machinery`, the import path runs the config loader's **`config.Provider.Validate(mode)`** (container/multi-doc mode) on the uploaded `controlplane.yaml`, not just a parse check — catching malformed-but-parseable configs *before* they are frozen and served, at near-zero cost. This is a **boundary check**, not schema tracking: the config stays opaque bytes thereafter (P4 D6 extended); Validate is the admission gate, not a model booty maintains.

### 6.3 Add a node
Bind a **known host** to the cluster: `POST /clusters/{id}/members {mac, machineType, schematicId?, patch?}`:
- Sets `hosts.cluster_id` + `hosts.machine_type`; sets `host.Schematic` (P5) if a schematic is chosen.
- **Generates** the node's machineconfig from the persisted bundle + endpoint + pinned versions + patches (cluster→role→host) + install image; **freezes** it (`source='generated'`, encrypted); sets `node_config_id`.
- Ensures the `(schematic, version)` cache target (retention-pinned, §8).
- The node netboots, fetches its config at `/machineconfig?mac=`, and **joins** — the bundle supplies the bootstrap token + cluster CA cert a worker needs to join (§14-B). Adding a control-plane is the same at booty's layer (etcd join is Talos runtime, operator-triggered).

> **Member schematic/version is single-sourced through regenerate (SGE I2).** A member's schematic lives on `host.Schematic` (P5, drives netboot) *and* is baked into the frozen `install.image` at generation. To prevent the two from drifting, **schematic and version changes for a cluster member flow only through add-member / regenerate** (which re-freezes the node config so `install.image` and — under I1 Option A — the pinned netboot version move together). The **raw P5 binding path refuses when `host.cluster_id` is set** (P5 §5 guard). This single-sources the member's image identity through one mutation path, not two (No-Wall/DRY).

### 6.4 Remove a node
`DELETE /clusters/{id}/members/{mac}` (a **mutation** — unbind, not a resource DELETE, so open in the trust window):
- Clears `hosts.cluster_id`/`machine_type`/`node_config_id`; the host stops matching the cluster rung and reverts to P4 precedence; its frozen revisions may be pruned.
- **In-cluster teardown is operator-driven** (`kubectl drain` + delete Node + `talosctl reset`) — the deferred lifecycle-orchestration boundary (D5). "Remove" = *stop provisioning*, not *gracefully evict from Kubernetes/etcd*.

### 6.5 Edit / version bump
Editing the `taloscluster` spec (patches) or bumping `talos_version`/`k8s_version` regenerates affected members into **new** frozen revisions; hosts roll forward on explicit re-bind. A version bump re-pins retention on the new `(schematic, version)` pairs — and, under I1 Option A, updates the per-member netboot version pin (§5) so boot and install stay aligned across the bump.

---

## 7. Secrets & encryption (age, fail-closed)

- **Storage:** the secrets bundle **and** every frozen node config are age-encrypted at rest (both embed CA material — for CP configs, private keys).
- **Key:** an operator-provided age identity via a new flag `--secretsKey` (path) / env. **Fail-closed:** with no key configured, cluster create/import/generation is **refused** (an encryption you can silently skip is not encryption). *(A friendlier warn+plaintext mode is deliberately not offered — D2.)*
- **Fail-fast when set-but-broken (SGE M2):** if `--secretsKey` *is* provided but unreadable/malformed, booty **refuses at startup** — mirroring booty's fail-fast-on-bad-config convention (`config.ValidateSignaturePolicy`, `config.go:160`). So: fail-**closed** when the key is unset (refuse cluster ops), fail-**fast** when the key is set but invalid (refuse to start) — a broken key never surfaces first as a mid-operation decrypt failure.
- **Rationale:** cluster root CA keys are qualitatively more sensitive than machineconfigs, and P8 (backup+snapshots) will ship the DB off-box — plaintext CAs in exported backups are unacceptable.
- **Library:** `filippo.io/age` (a small, standard dependency; not Talos).
- `Export` decrypts + emits `secrets.yaml` (escape hatch / interop). `Rotate` is deferred (§2, D5).

---

## 8. Retention pin (a required reconciler change)

A cluster pins a specific Talos version, but the reconciler caches a **rolling** window (`--talosRetainMinors`). Without intervention, a long-lived pinned cluster would eventually see its version age out of the window and be pruned — **boot assets vanish, nodes can't netboot/recreate.** Therefore:

> Every `(schematic, version)` pair referenced by a live cluster joins the reconciler's never-evict union as a **new union member** — "versions referenced by live clusters," sourced from `clusters.talos_version` + members. A version stops being pinned when no cluster references it (edit/version-bump/cluster-delete).

**Why this is a genuinely new retention input, not a reuse (SGE M3).** P3b's D13 protects only the *newest cached* version; #48 keeps `discovered ∪ (in-window ∧ cached ∧ discovered)`. Neither pins an *arbitrary older* version once it ages out of discovery — it drops from `known`, so `retentionFor` prunes it (`pkg/cache/reconcile.go`, `evict.go`). Pinning `cluster.talos_version` after it ages below the window therefore requires a **new** retention source keyed on live-cluster references; §8 is correctly titled "a required reconciler change" and D4 owns it. This closes an otherwise-silent "cluster dies months later" failure. (D4.)

---

## 9. Patching model

Verified (§14-A3): `configpatcher.Apply(in, []Patch)` applies patches **in slice order**; format auto-detected (`LoadPatches`); **strategic-merge** works on multi-doc configs, **JSON6902** rejects them (`numDocuments != 1`). Layering, composed in order:

1. **cluster-wide** patches (from the `taloscluster` spec) — apply to all members.
2. **role** patches (`patches.controlplane` / `patches.worker`) — apply by `machine_type`.
3. **per-host** patch (optional, set at bind time) — the narrowest override.

Strategic-merge is the default/recommended authoring format (multi-doc-safe); JSON6902 is accepted only for single-document targets. Deletion via `$patch: delete` is supported by the library.

---

## 10. API surface (`/api/v1`, Huma-typed)

New `pkg/http/api_clusters.go` (`registerClusters(grp, deps)`), wired additively in `registerOperations` (siblings untouched, No-Wall). Host mutations go through `pkg/hardware` wrappers (new `SetHostCluster`/`SetHostMachineType`); cluster/node-config reads/writes via `deps.Store`.

| method + path | op | auth | body / result |
|---|---|---|---|
| `GET /clusters` | list-clusters | open | `{clusters: [ClusterDTO]}` (member counts, versions, status derived from `host.Booted`) |
| `POST /clusters` | create-cluster (201) | open | `{name, endpoint, talosVersion, k8sVersion}` → mint+encrypt bundle |
| `POST /clusters/import` | import-cluster (201) | open | `controlplane.yaml` (req) + `worker.yaml`(s) + host mapping → reconstruct (§6.2) |
| `GET /clusters/{id}` | get-cluster | open | `ClusterDTO` + members + spec revision |
| `PUT /clusters/{id}` | update-cluster | open | `{endpoint?, talosVersion?, k8sVersion?, spec?}` → regenerate frozen revisions |
| `POST /clusters/{id}/members` | add-member | open | `{mac, machineType, schematicId?, patch?}` → generate+freeze+bind (§6.3) |
| `DELETE /clusters/{id}/members/{mac}` | remove-member | open | unbind (mutation, §6.4) |
| `POST /clusters/{id}/export` | export-secrets | open | decrypt → `secrets.yaml` (escape hatch) |
| `DELETE /clusters/{id}` | delete-cluster | **403** | wired-but-403 until P10 |

`ClusterDTO{ID, Name, Endpoint, TalosVersion, K8sVersion string, Members []MemberDTO, SpecRevision int, UpdatedAt string}`.
`MemberDTO{MAC, Hostname, MachineType, SchematicID, Status string}` (Status derived from `host.Booted`).

Boundary validation: `machineType` ∈ {controlplane, worker}; `endpoint` a valid URL; imported `controlplane.yaml` must **parse *and* pass `config.Provider.Validate()`** as a Talos config (422 otherwise — SGE ADOPT, §6.2); a member's host must exist and not already belong to another cluster.

---

## 11. UI — Clusters (antd v5, token-driven / v6-compatible)

Added additively through the `nav.tsx` seam (a new top-level Clusters view; see the [design mockup](https://claude.ai/code/artifact/764bb660-cb5f-4716-be15-b04e5360ac19)):

- **Clusters list** — name, node count, version, status (derived).
- **Cluster detail** — endpoint / pinned versions (fields), secrets (generated · encrypted; `Export`), spec `[Edit YAML]`, and a **Members** table with per-node **schematic** + machine type + status.
- **Assign host** — pick from booty's known hosts, choose controlplane/worker, optional per-host patch → add-member.
- **Import** — upload `controlplane.yaml` (+ worker files), map files → hosts.

---

## 12. Constraints (unchanged project invariants)

- Vendor `pkg/machinery` (the sole heavy dep) + `filippo.io/age`; **no subprocess / no talosctl binary**; booty stays a single static server.
- `log/slog`; host access through `pkg/hardware`; store reads/writes via `deps.Store`.
- New flag `--secretsKey` (age identity). Fail-closed if unset (§7).
- machineconfig treated as **opaque bytes** — no Talos schema parsing (P4 D6 extended).
- `DELETE /clusters/{id}` wired-but-403 until P10; member unbind is a mutation (open).

---

## 13. Testing (against the real harnesses)

- **Determinism:** generate a config twice with a fixed clock + fixed bundle + pinned contract → byte-identical; changing the contract → different bytes (guards the §5 rationale).
- **Import:** `controlplane.yaml` → full bundle reconstructed + endpoint/versions/schematic parsed + verbatim bytes stored; a **worker-only** import → rejected (missing CA keys, §6.2).
- **Freeze/serve:** the new rung serves decrypted verbatim bytes byte-for-byte; a non-member falls through to P4 precedence unchanged (byte-identical target-key guard).
- **Add/remove member:** add → frozen config + cache target + `node_config_id`; remove → reverts to P4 precedence; frozen revision pruned.
- **Encryption fail-closed:** no `--secretsKey` → create/import/generate refused; wrong key → decrypt fails loudly, never serves a broken config.
- **Retention pin:** a cluster-referenced version survives a reconcile pass that would otherwise evict it.
- **Docker smoke:** import a real single-node `controlplane.yaml`, confirm `/machineconfig?mac=` serves it byte-identical and the schematic's assets cache.

---

## 14. Verified facts (source-level, this session)

**A — `pkg/machinery` API** (siderolabs/talos `release-1.13`): `generate.NewInput(name, endpoint, k8sVer string, ...Option)`; options `WithSecretsBundle`, `WithVersionContract`, `WithInstallImage`, `WithInstallDisk`, `WithAllowSchedulingOnControlPlanes`, `WithInstallExtraKernelArgs` (type is `Option`, not `GenOption`). `in.Config(machine.TypeControlPlane|TypeWorker) → config.Provider`. Secrets: `secrets.Bundle`; `NewBundle(clock, contract)`, `LoadBundle(path)`, `NewBundleFromConfig(clock, cfg)`, `NewBundleFromKubernetesPKI(...)`; `NewFixedClock(t)` for reproducibility. Patching: `configpatcher.Apply(Input, []Patch)`, strategic-merge multi-doc / JSON6902 single-doc, in slice order. Contract: `config.TalosVersion1_13`, `ParseContractFromVersion`. Serialize: `Provider.Bytes()`; `container.NewReadonly(bytes)` preserves original bytes.

**B — secrets internals** (source `worker.go`/`init.go` @ `v1.13.0` + v1alpha1 reference): `worker.yaml` carries **crt-only** `machine.ca`/`cluster.ca` and **omits** etcd CA, aggregator CA, SA key → cannot reconstruct a bundle. `controlplane.yaml` carries the full crt+key set = the complete bundle → `talosctl gen secrets --from-controlplane-config` (no `--from-worker-config`). Install image: `installer/<schematic>:<version>` is the v1.13 default (`metal-installer/…` is an equivalent alias). Byte-identity is keyed on the **version contract, not the tool binary**; same-contract cross-binary byte-identity is **not** doc-guaranteed → freeze bytes + pin the machinery version + test-verify.

**C — reconciled Talos v1.13 facts** (P5 §11): schematic content-addressed & version-independent; `talos.config=` query-only (`${uuid}/${serial}/${mac}/${hostname}`) → booty's `?mac=` model correct; machineconfig multi-document & opaque; Matchbox is the static-file precedent (§1.1).

---

## 15. Documentation gate (slice incomplete without)

Update `docs/schema/{API,DATABASE}.md` (clusters, membership columns, `cluster_node_configs`, the new endpoints), `docs/CONFIGURATION.md` (`--secretsKey`, fail-closed *and* fail-fast posture, the retention-pin behavior, the deferred-orchestration boundary), and `docs/STORAGE.md` (the encrypted node-config store — **plus the DR coupling: recovery requires *both* the backed-up DB and the on-box age key**, SGE 12-Factor VI note). README: add Talos cluster authoring to the feature list.

---

## 16. Explicit YAGNI / KISS cuts

- No talhelper/topf integration; no subprocess; no parallel node list (§1.2).
- No lifecycle orchestration (bootstrap/upgrade/reset/drain/etcd/kubeconfig); no `Rotate`; no health subsystem (D5).
- No patch reverse-engineering on import (D8); no JSON6902-on-multi-doc.
- No members table (columns on `hosts`); no cluster-level schematic (per-node via P5).
- No warn+plaintext secrets mode (fail-closed only, D2).

---

## 17. Appendix — decisions (ALL USER-APPROVED 2026-07-04, each as recommended)

- **D1 — booty-native cluster:** a `clusters` entity + host binding (`cluster_id`, `machine_type`); the authored YAML holds cluster-wide/role patches, **not** a node list. *(Alt: YAML-centric node list (talhelper-style); reuse P4 roles for machine-type — rejected: parallel host model / overloaded labels.)*
- **D2 — Secrets: booty mints + persists, import supported; age-encrypted, fail-closed.** *(Alt: operator-provides-only; warn+plaintext — rejected.)*
- **D3 — Materialize-and-freeze serving (byte-identical):** generate/import once, store immutable encrypted bytes, serve verbatim; regenerate only on explicit change. *(Alt: render-at-serve / never-materialized — reversed once byte-identical became a requirement; regeneration is not cross-version byte-stable, §5/§14-B.)*
- **D4 — Retention pin** cluster-referenced `(schematic, version)` in the reconciler's never-evict union (§8).
- **D5 — Authoring-only scope:** defer `Rotate`, upgrade/bootstrap/reset orchestration, health; member status derived from `host.Booted`. *(User explicitly accepted defer.)*
- **D6 — Per-node schematic** via P5 `host.Schematic` (cluster owns no schematic).
- **D7 — endpoint/versions as structured pinned fields;** YAML = patches only (protects the reproducibility-critical version pin; mirrors the machinery `Input`/patch split).
- **D8 — Import extracts durable inputs + stores bytes verbatim (byte-identical);** no patch reverse-engineering. Import **requires `controlplane.yaml`** (worker configs lack CA keys, §14-B).
- **D9 — Cluster-general;** single-node = degenerate one-host controlplane (`WithAllowSchedulingOnControlPlanes`).
- **D10 — Dedicated encrypted `cluster_node_configs` store** (not P4's plaintext `config_revisions`) + one new serving rung. *(Alt: encrypt P4 configs / reuse config_id — rejected: spreads encryption into P4.)*

---

## 18. Acceptance criteria

1. Migration `0005` adds `clusters`, `cluster_node_configs`, and `hosts.{cluster_id, machine_type, node_config_id}` (additive; existing behavior unchanged).
2. Greenfield create mints an age-encrypted bundle; **fail-closed** without `--secretsKey`.
3. Import from `controlplane.yaml` reconstructs the cluster (secrets, endpoint, versions, per-role schematic + cache targets) and stores imported bytes **verbatim**; worker-only import is rejected.
4. Add-member generates + freezes a node config, pre-caches its assets, and binds the host; the node netboots and joins. Under **I1 Option A**, a member netboots the pinned `cluster.talos_version` (not `NewestCached`), so boot and install versions align; the raw P5 bind path refuses for members (I2). Remove-member reverts the host to P4 precedence.
5. `/machineconfig?mac=` serves a cluster member's frozen bytes **byte-identical**; non-members fall through to P4 unchanged.
6. Editing spec/version mints new frozen revisions; hosts roll forward on re-bind.
7. Cluster-referenced versions are never evicted by the reconciler.
8. machineconfig is never parsed (opaque bytes). Docs gate met (§15). `go test ./... -race`, `vet`, web `tsc` clean; Docker smoke passes.
