# Configuration reference

booty is configured entirely through command-line flags and environment variables — there is **no
config file**. Every flag is also bindable as an environment variable (via Viper). Defaults are the
values used when neither a flag nor an env var is set.

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--httpPort` | `8080` | Port the HTTP server listens on. |
| `--debug` | `false` | Enable debug-level (verbose) logging. |
| `--cacheInterval` | `5m` | Interval between cache reconcile passes (discovery refresh). |
| `--cacheConcurrency` | `4` | Max concurrent artifact downloads during a reconcile pass. |
| `--dataDir` | `/data` | Directory for all stateful data (cache, templates, host DB). |
| `--serverIP` | `127.0.0.1` | LAN-reachable IP that clients use to reach booty. **Set this.** |
| `--serverHttpPort` | `80` | HTTP port advertised to clients (when it differs from `--httpPort`). |
| `--joinString` | `""` | Optional `kubeadm join` string injected into rendered Ignition. |
| `--flatcarArchitecture` | `amd64` | Architecture for Flatcar downloads. |
| `--flatcarChannel` | `stable` | Flatcar release channel. |
| `--coreOSArchitecture` | `x86_64` | Architecture for Fedora CoreOS downloads. |
| `--coreOSChannel` | `stable` | Fedora CoreOS stream/channel. |
| `--talosArchitecture` | `amd64` | Talos architecture token (`amd64` / `arm64`). |
| `--talosSchematic` | `376567988ad3…b4ba` | Default Talos Image Factory schematic ID. |
| `--talosRetainMinors` | `3` | Number of newest Talos minor lines to keep cached. |
| `--talosConfigFile` | `config/machineconfig.yaml` | Talos machine-config template, relative to `--dataDir`. |
| `--talosFactoryURL` | `https://factory.talos.dev` | Talos Image Factory base URL. |
| `--proxyDHCPEnabled` | `false` | Enable the proxyDHCP responder (UDP 67 + 4011). |
| `--proxyDHCPBootfileBIOS` | `undionly.kpxe` | Pass-1 BIOS iPXE binary name (staged in `--dataDir`). |
| `--proxyDHCPBootfileUEFI` | `ipxe.efi` | Pass-1 UEFI (x86-64) iPXE binary name. |
| `--proxyDHCPBootfileARM64` | `ipxe-arm64.efi` | Pass-1 ARM64 iPXE binary name. |

## Environment variables

In addition to the auto-bound flag env vars, a few settings are read directly from the environment:

| Env var | Default | Purpose |
|---------|---------|---------|
| `IGNITION_FILE` | `config/ignition.yaml` | Butane/Ignition template path, relative to `--dataDir`. |
| `HARDWARE_MAP` | `hardware.json` | Host-database filename, relative to `--dataDir`. |
| `DATABASE_PATH` | `<dataDir>/booty.db` | SQLite database path (control-plane + host state). |

## Network ports

| Port | Protocol | When | Purpose |
|------|----------|------|---------|
| `8080` (`--httpPort`) | TCP | always | HTTP: boot configs, artifacts, API, web UI. |
| `69` | UDP | always | TFTP: iPXE binaries and the `booty.ipxe` chain script. |
| `67` | UDP | `--proxyDHCPEnabled` | proxyDHCP pass-1 (firmware PXE request). |
| `4011` | UDP | `--proxyDHCPEnabled` | proxyDHCP pass-2 (iPXE re-request). |

proxyDHCP binds privileged ports — run with `CAP_NET_BIND_SERVICE` (e.g. Docker
`--cap-add=NET_BIND_SERVICE`) or as root. proxyDHCP is best-effort: if it fails to start, booty
logs the error and continues serving TFTP/HTTP.

## Notes

- booty is **not** a DHCP server. Either point your existing DHCP server's `next-server` / bootfile
  at booty, or enable `--proxyDHCPEnabled` to answer PXE requests alongside it (it never hands out
  IP leases).
- All config templates and the host database live **under `--dataDir`**; see
  [schema/STORAGE.md](schema/STORAGE.md).

> **As of P1c:** the `/api/v1` management plane is now active. The following HTTP paths are served
> on the existing `--httpPort` alongside the boot-facing endpoints, with no new flags:
>
> | Path | Purpose |
> |------|---------|
> | `/api/v1` | Versioned management API (targets, catalog, hosts). |
> | `/api/v1/docs` | Scalar interactive API documentation UI. |
> | `/api/v1/openapi.json` | OpenAPI 3.1 spec (machine-readable). |
>
> Per-target and per-host settings are managed through the API and persisted in `booty.db`. See
> [schema/API.md](schema/API.md) for the full endpoint reference.
