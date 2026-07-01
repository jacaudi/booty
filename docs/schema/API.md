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
- CoreOS: `[[coreos-arch]]`, `[[coreos-channel]]`, `[[coreos-version]]`, `[[coreos-baseurl]]`.
- Talos: `[[talos-schematic]]`, `[[talos-arch]]`, `[[talos-version]]`, `[[talos-baseurl]]`
  (version resolved from the newest cached release for the host's schematic).

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

### Hosts

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| `GET` | `/api/v1/hosts` | List known hosts. Optional `?approved=true\|false` filter. | `{"hosts":[…]}` |
| `POST` | `/api/v1/hosts/{mac}/approve` | Approve a host. If the host has a non-empty `os` field, also sets `boot_mode='assigned'` and `assigned_os=os` (plus `schematic` param for Talos), making the host immediately boot-ready once its target's versions are cached. **OPEN.** | host JSON / `404` |
| `POST` | `/api/v1/hosts/{mac}/revoke` | Revoke approval (host falls back to holding pattern). **OPEN.** | `204` |
| `POST` | `/api/v1/hosts/{mac}/menu` | Approve (if needed) and put the host into interactive boot-menu mode (`boot_mode='menu'`). Does **not** route through `SetAssignment`; `approved_os` is unchanged. **OPEN.** `404` if MAC is unknown. | host JSON / `404` |
| `PUT` | `/api/v1/hosts/{mac}` | **403 until auth (P10).** | `403` |
| `DELETE` | `/api/v1/hosts/{mac}` | **403 until auth (P10).** | `403` |

> The management UI (`web/`, served at `/ui/`) consumes these hosts endpoints:
> `GET /api/v1/hosts`, `POST /api/v1/hosts/{mac}/approve`,
> `POST /api/v1/hosts/{mac}/revoke`, `POST /api/v1/hosts/{mac}/menu`.
> `PUT`/`DELETE /api/v1/hosts/{mac}` are wired but return 403 until auth (P10),
> so the UI exposes no edit/delete actions.

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
