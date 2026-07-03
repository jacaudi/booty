# API & boot contracts

booty exposes three interfaces a client or operator interacts with: an **HTTP** API (boot configs,
artifacts, host management, web UI), the **TFTP** boot filenames that drive the iPXE chain, and the
optional **proxyDHCP** responder. This documents the current contract.

---

## HTTP endpoints

Served on `--httpPort` (default `8080`).

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/` | Redirect to the web UI. | `302 → /ui/` |
| `GET` | `/ignition.json` | Ignition config for a Flatcar/CoreOS host. MAC resolved from a query param or by ARP; unknown hosts get a reboot-loop config. | Ignition v3.4.0 JSON |
| `GET` | `/machineconfig` | Talos machine config for a host. MAC resolved from query/ARP; supports a per-host schematic. | YAML (`text/yaml`) |
| `GET` | `/version.txt` | Current cached versions, env-var format. | `FLATCAR_VERSION=…\nCOREOS_VERSION=…\n` |
| `GET` | `/version.json` | Current cached versions, JSON. | `{"flatcar":"…","coreos":"…"}` |
| `GET` | `/info` | Aggregated version + build info. | `{"flatcar":{…},"coreos":{…},"booty":{…}}` |
| `GET` | `/hosts?mac=<MAC>` | Look up one registered host by MAC (required). | Host JSON, or `400`/`404` |
| `GET` | `/booty.json` | All registered hosts and all in-memory unknown hosts. | `{"hosts":{…},"unknownHosts":{…}}` |
| `POST` | `/register` | Register/update a MAC → host mapping. Body: a Host JSON object (see [DATABASE.md](DATABASE.md)). | `OK` / `500` |
| `POST` | `/unregister` | Remove a MAC mapping (idempotent). Body: a Host JSON object (MAC required). | `OK` / `500` |
| `GET` | `/data/<path>` | Static file server over `--dataDir` (cache artifacts, templates, iPXE binaries). | file / `404` |
| `GET` | `/ui/<path>` | The embedded web UI. | asset / `404` |

**Register example:**

```bash
curl -X POST http://localhost:8080/register \
  -H 'Content-Type: application/json' \
  -d '{"mac":"aa:bb:cc:dd:ee:ff","hostname":"node1","os":"talos","schematic":"<id>"}'
```

MACs are canonicalized (lowercase, colon-delimited) on write and on lookup, so any common format is
accepted.

> **As of P1b:** `/version.txt`, `/version.json`, and `/info` report the **newest cached** Flatcar /
> CoreOS version (derived from the `cache/` directory), not internal `Current*` state. The response
> shapes are unchanged — this is a source change only.

---

## TFTP boot filenames

Served on UDP `:69`. The host's OS is resolved by ARP-ing the requesting IP and looking it up in the
host database. Most filenames are served as plain cached files; two are **magic**:

| Filename | Behavior |
|----------|----------|
| `booty.ipxe` | Dynamically generated per-host iPXE script — selects the OS template and substitutes boot tokens (below). If the host's `DoInstall` flag is set, it is flipped off on this fetch (one-shot install). |
| `pxelinux.cfg/default` | Legacy PXE config, for firmware that boots PXE rather than iPXE. |
| *(any other path)* | Served from the artifact cache via a path-escape-checked join under `--dataDir`. |

**Boot-token substitution** (replaced in the generated `booty.ipxe`):

- Common: `[[server]]` (= `serverIP:serverHttpPort`).
- Flatcar: `[[flatcar-arch]]`, `[[flatcar-version]]`, `[[flatcar-baseurl]]`.
- CoreOS: `[[coreos-arch]]`, `[[coreos-version]]`, `[[coreos-baseurl]]`.
- Talos: `[[talos-schematic]]`, `[[talos-arch]]`, `[[talos-version]]`, `[[talos-baseurl]]`
  (version resolved from the newest cached release for the host's schematic).

> **As of #48:** Flatcar and CoreOS resolve their channel the same way Talos resolves its
> schematic — `host.AssignedParams["channel"]` if set, else the `--flatcarChannel` /
> `--coreOSChannel` flag default — then serve the newest cached version under that channel.
> `[[coreos-channel]]` is removed: the prior CoreOS template read the channel from a viper flag
> directly and never consumed the token's value.

---

## proxyDHCP

Enabled with `--proxyDHCPEnabled`. It answers PXE boot requests **without assigning IP leases**
(`YourIPAddr = 0.0.0.0`), so it coexists with an existing DHCP server. It only responds to requests
whose vendor class identifier begins with `PXEClient`.

- **Pass 1** (UDP `:67`, bare firmware): returns the architecture-appropriate iPXE binary —
  `--proxyDHCPBootfileUEFI` (x86-64 UEFI), `--proxyDHCPBootfileARM64` (ARM64), or
  `--proxyDHCPBootfileBIOS` (legacy BIOS). The named binary must be staged in `--dataDir`.
- **Pass 2** (UDP `:4011`, iPXE re-request, detected via the `iPXE` user-class): returns
  `booty.ipxe`, handing control to the TFTP chain above.

---

## Management API — `/api/v1`

The versioned operator API, mounted under `/api/v1` on the same `--httpPort`. It does not affect
the boot contract above. All endpoints speak JSON.

> **Trust window (design §2.10) — read this first.** Mutating `POST` and `PATCH` endpoints are
> **OPEN** (no authentication required). Destructive endpoints (`DELETE`, and `PUT /api/v1/hosts/{mac}`)
> return `403 Forbidden` — this is an
> **API-shape device** that reserves destructive operations for the auth layer; it is **not** a
> security control. The entire pre-auth window assumes a **trusted LAN**. Authentication lands in
> P10 and will gate all mutating operations uniformly at that point.

### OpenAPI & docs

| Path | Purpose |
|------|---------|
| `GET /api/v1/openapi.json` | OpenAPI 3.1 spec (machine-readable). |
| `GET /api/v1/docs` | Scalar interactive docs UI. |

### Catalog (read-only)

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/families` | List boot-config families (`ignition`, `machineconfig`, …). | `{"families":[…]}` |
| `GET` | `/api/v1/os` | List supported OS types with required params per OS. | `{"os":[…]}` |

### Targets

Cache targets represent an (OS, arch, params) tuple that the reconciler discovers and caches.

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/targets` | List all targets. | `{"targets":[…]}` |
| `GET` | `/api/v1/targets/{id}` | Get one target. | target JSON / `404` |
| `POST` | `/api/v1/targets` | Create a target. Async — the new target's `cached` versions are `false` until the reconciler completes its next pass. **OPEN.** | `201` target JSON |
| `PATCH` | `/api/v1/targets/{id}` | Partial update: `enabled`, `retainN`, `mode`. **OPEN.** | target JSON / `404` |
| `DELETE` | `/api/v1/targets/{id}` | **403 until auth (P10).** | `403` |
| `POST` | `/api/v1/targets/{id}/versions` | Pin a manual version on a target. Triggers async cache. **OPEN.** | `201` |
| `DELETE` | `/api/v1/targets/{id}/versions/{v}` | **403 until auth (P10).** | `403` |

**Required params, per OS** (as of #48, `flatcar` and `fedora-coreos` join `debian` in requiring a
channel; `talos` requires a schematic):

| OS | Required param(s) |
|----|--------------------|
| `talos` | `schematic` |
| `flatcar` | `channel` |
| `fedora-coreos` | `channel` |
| `debian` | `channel` |

`GET /api/v1/os` reports the authoritative required-params list per registered OS.

**`POST /api/v1/targets` validation, in order** (all failures are `422`):

1. `os` must be a registered OS (`ostype.Lookup`).
2. `params` may only contain keys the OS's `RequiredParams()` declares — any other key is rejected
   as `"unexpected param: <k>"`. This isn't just tidiness: `paramSegment` picks the
   path-discriminating cache segment by fixed key precedence (`schematic` > `channel`), so an
   unrequested key would silently become an **unvalidated** disk/URL path segment if it happened to
   match one of those names.
3. Every required param must be present and non-empty (`"missing required param: <p>"`).
4. Every required param's **value** must match `^[a-z0-9][a-z0-9.-]*$` — lowercase-alnum start,
   then alnum/dot/dash, no `/` — since required params become the cache directory + URL segment
   (`"invalid param <p>"`). The same check runs on the `--flatcarChannel` / `--coreOSChannel` /
   `--talosSchematic` flags at startup and on the one-time #48 migration, so a malformed flag or a
   malformed API param are rejected the same way.

**Predefined-seeding semantics (#48 D1).** The Flatcar, Fedora CoreOS, and Talos predefined targets
are **create-if-absent**: `--flatcarChannel` / `--coreOSChannel` / `--talosSchematic` /
`--talosRetainMinors` only populate a predefined row the first time it's created (fresh install, or
the one-time migration described in [STORAGE.md](STORAGE.md)). Once a row exists, the flags are
never read again for it — `PATCH /api/v1/targets/{id}` owns `enabled` / `retainN` / `mode` from
then on, and survives every reconcile tick untouched. Changing a channel flag later does **not**
update the existing row: because params are part of row identity (`UNIQUE(os,arch,params)`), it
creates a **new** predefined target for the new channel on the next tick; the old channel's target
keeps running until an operator disables it with `PATCH {"enabled":false}` (`DELETE` is `403` until
P10).

### Configs (P4)

Boot configs are first-class DB state: an identity row (`configs`) plus immutable, append-only
revisions (`config_revisions`). `PUT` never mutates a revision — it appends a new one and repoints
the config's active pointer. See [DATABASE.md](DATABASE.md) for the table shapes and
[CONFIGURATION.md](../CONFIGURATION.md) for the boot-time precedence that consumes these configs.

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/configs` | List configs (name, kind, active revision number, revision count). | `{"configs":[…]}` |
| `POST` | `/api/v1/configs` | Create a config. Body: `{"name","kind","source"}` (`kind`: `butane`\|`machineconfig`\|`preseed`). Validates by rendering `source` against stub vars — a bad Butane config surfaces the fatal report in the `422` body. The first revision is recorded and made active. **OPEN.** | `201` config JSON |
| `GET` | `/api/v1/configs/{id}` | Get a config's identity plus its active revision's decoded source. | config JSON `+source` / `404` |
| `PUT` | `/api/v1/configs/{id}` | Append a new immutable revision from `{"source"}` and make it active. Same stub-var validation as create. On success, also prunes older revisions per `--configRevisionsKeep` (the active revision is always kept — see [CONFIGURATION.md](../CONFIGURATION.md)). **OPEN.** | config JSON / `404` |
| `POST` | `/api/v1/configs/{id}/preview` | Render the config's **active revision**. Body: `{"mac"?}`. **Subsumes `/validate`** — omit `mac` to validate against stub vars only (report-only: a bad Butane config returns its fatal report in the `200` body, never a `5xx`); pass `mac` to render against a real host's vars (the same vars the boot path would use). **OPEN.** | `{"rendered","contentType","report"}` |
| `GET` | `/api/v1/configs/{id}/revisions` | List a config's revisions, newest first, each flagged `active`. | `{"revisions":[…]}` |
| `POST` | `/api/v1/configs/{id}/rollback` | Move the active pointer to an existing revision (`{"revision"}`, validated to belong to this config). A pointer move — no content is copied, no new revision is created. **OPEN.** | config JSON / `422` |
| `DELETE` | `/api/v1/configs/{id}` | **403 until auth (P10).** | `403` |

**`ConfigDTO`:**

| Field | Type | Meaning |
|-------|------|---------|
| `id` | integer | `configs.id`. |
| `name` | string | Operator-chosen, unique. |
| `kind` | string | `butane` \| `machineconfig` \| `preseed` — the dialect an operator authors (see `kind` vs family `ConfigKind` in [DATABASE.md](DATABASE.md#configs)). |
| `activeRevision` | integer | The active revision's number; `0` when the config has no active revision yet. |
| `revisionCount` | integer | Total revisions retained (bounded by `--configRevisionsKeep`). |
| `updatedAt` | string | Bumped on every active-pointer move (create, edit, or rollback). |

**`RevisionDTO`:**

| Field | Type | Meaning |
|-------|------|---------|
| `revision` | integer | Per-config sequence number. |
| `sha256` | string | Hex SHA-256 of the raw (decoded) source. |
| `createdAt` | string | Revision creation timestamp. |
| `active` | bool | Whether this is the config's current active revision. |

### Roles (P4)

Roles are fleet-wide groupings that carry an optional default config, resolved by name (rung 2 of
the boot-config precedence — see [CONFIGURATION.md](../CONFIGURATION.md)).

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/roles` | List roles with bound-host count. | `{"roles":[…]}` |
| `POST` | `/api/v1/roles` | Create a role. Body: `{"name","defaultConfigId"?}`. **OPEN.** | `201` role JSON |
| `PUT` | `/api/v1/roles/{id}` | Update `name` and/or `defaultConfigId`; omitted fields are left unchanged. There is no way to *clear* a set `defaultConfigId` in P4. **OPEN.** | role JSON / `404` |
| `DELETE` | `/api/v1/roles/{id}` | **403 until auth (P10).** | `403` |

**`RoleDTO`:**

| Field | Type | Meaning |
|-------|------|---------|
| `id` | integer | `roles.id`. |
| `name` | string | Operator-chosen, unique; also the alphabetical tie-break order for precedence rung 2. |
| `defaultConfigId` | integer *(omitempty)* | Config served to hosts with this role absent an explicit host `config_id`. Omitted when unset. |
| `hostCount` | integer | Number of hosts currently bound to this role (`host_roles`). |

### Hosts

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/hosts` | List known hosts. Optional `?approved=true\|false` filter. | `{"hosts":[…]}` |
| `POST` | `/api/v1/hosts/{mac}/approve` | Approve a host. If the host has a non-empty `os` field, also sets `boot_mode='assigned'` and `assigned_os=os` (plus `schematic` param for Talos), making the host immediately boot-ready once its target's versions are cached. **P4:** the body is now optional and extended to `{"configId"?, "roleIds"?[]}` — an empty/omitted body is byte-identical to pre-P4 approve; a present `configId`/`roleIds` atomically binds them in the same call (see the family-match rule below). **OPEN.** | host JSON / `404` |
| `POST` | `/api/v1/hosts/{mac}/bind` | **P4.** Rebind `{"configId"?, "roleIds"?[]}` on an already-approved host without changing its approval state. Same validation as `approve`'s binding. **OPEN.** | host JSON / `404` / `422` |
| `POST` | `/api/v1/hosts/{mac}/revoke` | Revoke approval (host falls back to holding pattern). **OPEN.** | `204` |
| `POST` | `/api/v1/hosts/{mac}/menu` | Approve (if needed) and put the host into interactive boot-menu mode (`boot_mode='menu'`). Does **not** route through `SetAssignment`; `approved_os` is unchanged. **OPEN.** `404` if MAC is unknown. | host JSON / `404` |
| `PUT` | `/api/v1/hosts/{mac}` | **403 until auth (P10).** | `403` |
| `DELETE` | `/api/v1/hosts/{mac}` | **403 until auth (P10).** | `403` |

> **Family-match validation (P4).** Both `approve` and `bind` validate a present `configId` against
> the host's OS family before writing it: the config's `kind` must equal
> `configKindForFamily(family.ConfigKind)` for the host's `os` (e.g. a `flatcar` host requires a
> `butane`-kind config). A mismatch — or an unresolvable OS family — returns `422` and binds
> nothing (roles, if also present in the body, are not bound either; the call fails atomically).
> Each `roleIds` entry must reference an existing role or the call fails the same way. See
> [DATABASE.md](DATABASE.md#configs) for the `kind` enum and its relationship to `ConfigKind`.

> The management UI (`web/`, served at `/ui/`) consumes these hosts endpoints:
> `GET /api/v1/hosts`, `POST /api/v1/hosts/{mac}/approve`,
> `POST /api/v1/hosts/{mac}/revoke`, `POST /api/v1/hosts/{mac}/menu`.
> `PUT`/`DELETE /api/v1/hosts/{mac}` are wired but return 403 until auth (P10),
> so the UI exposes no edit/delete actions.

### Cache

Cache inventory: the set of on-disk boot artifacts tracked in `cache_entries`. All endpoints are under `/api/v1`.

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/cache` | List cache inventory. Optional filters: `os`, `arch`, `state` (`in-cycle`\|`archived`), `pinned` (`true`\|`false`). | `{"entries":[…]}` |
| `POST` | `/api/v1/cache/{id}/pin` | Pin a cached version (exempt from eviction). **OPEN.** | cache entry JSON / `404` |
| `POST` | `/api/v1/cache/{id}/unpin` | Unpin a cached version (eligible for eviction again). **OPEN.** | cache entry JSON / `404` |
| `POST` | `/api/v1/cache/scan` | Reconcile the cache inventory to disk: recomputes sizes, repairs missing `cache_entries` rows, and counts on-disk version dirs with no matching `target_version`. **OPEN.** | `{"scanned":N,"updated":N,"orphans":N}` |
| `POST` | `/api/v1/cache/{id}/reverify` | Re-run artifact verification for a cached version and re-record `verified`/`verify_err`. Recomputes the verdict from the on-disk final files (SHA256 re-hashed, `.sig` re-fetched and re-checked) — **non-destructive**: never evicts, moves, or deletes bytes regardless of outcome. Ignores `--signaturePolicy off` (an explicit operator ask always verifies). **OPEN.** | cache entry JSON / `404` |
| `DELETE` | `/api/v1/cache/{id}` | **403 until auth (P10).** | `403` |

**Cache entry JSON** (`CacheEntryDTO`):

| Field | Type | Meaning |
|-------|------|---------|
| `id` | integer | `cache_entries.id` — stable row key. |
| `os` | string | OS taxonomy name (`talos`, `flatcar`, `coreos`). |
| `arch` | string | Architecture (`amd64`, `arm64`, …). |
| `params` | object | Decoded target params (e.g. `{"schematic":"…"}` for Talos; `{"channel":"…"}` for Flatcar / Fedora CoreOS / Debian). |
| `version` | string | Version string. |
| `size` | integer | Cached artifact bytes (summed from disk at last upsert). |
| `state` | string | Derived: `in-cycle` \| `in-cycle-pinned` \| `archived` \| `archived-pinned`. |
| `pinned` | bool | Whether this version is operator-pinned. |
| `inWindow` | bool | Whether this version is currently in the reconciler's retention window. |
| `fetchedAt` | string | ISO-8601 timestamp of the last successful cache or scan update. |
| `verified` | bool *(omitted when no verdict)* | Tri-state artifact-integrity verdict (P3b): `true` = all verifiable artifacts passed; `false` = at least one failed; **omitted** (`omitempty`) when there is no verdict (no verification mechanism, or `--signaturePolicy off`). Maps to `cache_entries.verified` (see [DATABASE.md](DATABASE.md)). |
| `verifyErr` | string *(omitempty)* | Present only when `verified=false`: the `errors.Join` of every failing artifact's message across the version, each carrying its failure-class text (`checksum mismatch` / `signature mismatch` / `unknown or expired signing key`). |

**Filter notes.** The `state` query parameter maps to `in_window`: `state=in-cycle` returns all rows with `in_window=1` (both `in-cycle` and `in-cycle-pinned`); `state=archived` returns rows with `in_window=0`. To narrow to pinned-only or unpinned-only, combine with `pinned=true` or `pinned=false`. An unrecognised `state` value is silently ignored (no filter applied).

**Scan notes.** `POST /cache/scan` repairs `cache_entries` rows from disk but does **not** recompute `in_window` — window membership requires a live discovery run and is self-healed by the next reconciler tick. Orphans are reported but never auto-adopted.

> The management UI consumes these endpoints:
> `GET /api/v1/cache`, `POST /api/v1/cache/{id}/pin`, `POST /api/v1/cache/{id}/unpin`,
> `POST /api/v1/cache/scan`, `POST /api/v1/cache/{id}/reverify` (the Cache view's per-row
> Reverify action + the three-state Verified column). `DELETE /api/v1/cache/{id}` is wired
> but returns 403 until auth (P10).

### Boot dispatch (P1c)

`booty.ipxe` (the TFTP magic file) now dispatches per host state rather than solely by `host.OS`:

| Host state | Boot outcome |
|-----------|-------------|
| Unknown MAC (no ARP match) or unregistered | Holding pattern — serves `holding.ipxe`, which re-chains to `booty.ipxe` and loops until the host is registered and approved. |
| Registered but **not approved** | Holding pattern (same as above). |
| Approved + `boot_mode='assigned'` | Boots the newest cached version of `assigned_os` (falls back to `host.os` if `assigned_os` is empty). |
| Approved + `boot_mode='menu'` | Serves a dynamically generated interactive iPXE boot menu (over TFTP) listing every currently-cached `(os, version)` image. The node selects a version and boots it. The selection is ephemeral — nothing is written back. |

> **As of P1c:** `/booty.json` (the UI payload) now **additively** carries host approval and
> assignment state for each registered host: `approved` (bool), `bootMode` (string),
> `assignedOS`, `assignedArch`, `assignedParams` (strings). Fields are omitted when zero-valued.
> The response shape for existing fields is unchanged.

---

## Versioning & stability

The boot-facing endpoints (`/ignition.json`, `/machineconfig`, `/version.*`), the TFTP filenames,
and proxyDHCP behavior are the **stable contract** machines depend on. The `/api/v1` management
plane is explicitly versioned and documented here as each slice lands; it does not change the boot
contract.
