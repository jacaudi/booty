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

## Versioning & stability

The boot-facing endpoints (`/ignition.json`, `/machineconfig`, `/version.*`), the TFTP filenames,
and proxyDHCP behavior are the **stable contract** machines depend on. The v1 management plane adds
a separate, explicitly **versioned** operator API under **`/api/v1`** (targets, catalog, configs,
cache, hosts); it is documented here as each slice lands and does not change the boot contract.
