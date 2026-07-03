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
| `--signaturePolicy` | `warn` | Artifact verification policy: `strict` \| `warn` \| `off` (see [below](#signature-verification---signaturepolicy)). An unknown value fails startup. |
| `--dataDir` | `/data` | Directory for all stateful data (cache, templates, host DB). |
| `--serverIP` | `127.0.0.1` | LAN-reachable IP that clients use to reach booty. **Set this.** |
| `--serverHttpPort` | `80` | HTTP port advertised to clients (when it differs from `--httpPort`). |
| `--joinString` | `""` | Optional `kubeadm join` string injected into rendered Ignition. |
| `--flatcarArchitecture` | `amd64` | Architecture for Flatcar downloads. |
| `--flatcarChannel` | `stable` | Flatcar release channel — **first-boot default only** (see below). |
| `--coreOSArchitecture` | `x86_64` | Architecture for Fedora CoreOS downloads. |
| `--coreOSChannel` | `stable` | Fedora CoreOS stream/channel — **first-boot default only** (see below). |
| `--talosArchitecture` | `amd64` | Talos architecture token (`amd64` / `arm64`). |
| `--talosSchematic` | `376567988ad3…b4ba` | Default Talos Image Factory schematic ID — **first-boot default only** (see below). |
| `--talosRetainMinors` | `3` | Number of newest Talos minor lines to keep cached — **first-boot default only** (see below). |
| `--talosConfigFile` | `config/machineconfig.yaml` | Talos machine-config template, relative to `--dataDir`. |
| `--talosFactoryURL` | `https://factory.talos.dev` | Talos Image Factory base URL. |
| `--proxyDHCPEnabled` | `false` | Enable the proxyDHCP responder (UDP 67 + 4011). |
| `--proxyDHCPBootfileBIOS` | `undionly.kpxe` | Pass-1 BIOS iPXE binary name (staged in `--dataDir`). |
| `--proxyDHCPBootfileUEFI` | `ipxe.efi` | Pass-1 UEFI (x86-64) iPXE binary name. |
| `--proxyDHCPBootfileARM64` | `ipxe-arm64.efi` | Pass-1 ARM64 iPXE binary name. |

> **First-boot defaults (#48).** `--flatcarChannel`, `--coreOSChannel`, `--talosSchematic`, and
> `--talosRetainMinors` seed the **predefined** cache targets (Flatcar, Fedora CoreOS, Talos) only
> when that target's row doesn't exist yet — a fresh install, or the one-time migration on first
> startup after upgrading past #48 (see [schema/STORAGE.md](schema/STORAGE.md)). Once a predefined
> row exists, these flags are never consulted for it again: the `/api/v1/targets` API owns
> `enabled` / `retainN` / `mode` from that point on, and a value set via `PATCH` survives every
> reconcile tick. Concretely:
>
> - Changing `--flatcarChannel` / `--coreOSChannel` after first boot does **not** retarget the
>   existing predefined row — because the channel is part of the target's identity
>   (`UNIQUE(os,arch,params)`), it seeds a **new** predefined target for the new channel on the
>   next tick, alongside the old one. Disable the old channel's target with
>   `PATCH /api/v1/targets/{id} {"enabled":false}` if it should stop being cached.
> - **`--talosRetainMinors` behavior change:** bumping this flag after the Talos predefined target
>   already exists has **no effect** on that row — it only sets the initial `retainN` at creation
>   time. Adjust retention on an existing target with
>   `PATCH /api/v1/targets/{id} {"retainN":<n>}` instead.
>
> See [schema/API.md](schema/API.md#targets) for the full create-if-absent / PATCH contract.

### Retention windows for single-version-discovery OSes

Flatcar and Fedora CoreOS discovery only ever returns **one** version — the channel's current
build. For these OSes, `retainN` (via `--flatcarChannel`'s / `--coreOSChannel`'s predefined target,
or `PATCH retainN` on any flatcar/fcos target) bounds a **window over known versions** rather than
selecting from a larger discovered set: each reconcile tick, the newly-discovered version is added
to the set of versions still in-window, and the newest `retainN` of that combined set are kept. The
window therefore **grows one release at a time** as new versions are discovered — `retainN=3` takes
three upstream releases to reach three cached versions, not one. It does **not backfill** older
versions upstream no longer advertises; there is no way to retroactively populate history that was
never seen while the reconciler was running. Versions that age out of the window are archived, not
deleted (see [schema/STORAGE.md](schema/STORAGE.md)).

## Signature verification (`--signaturePolicy`)

booty verifies the integrity of the boot artifacts it downloads before serving them. The mechanism
is **per-OS** (Fedora CoreOS: SHA-256 from the streams JSON; Flatcar: detached GPG `.sig` against an
embedded keyring; Talos/Debian: no mechanism yet), and the **policy** is one global flag:

| Value | Behavior |
|-------|----------|
| `strict` | Any verifiable artifact that **fails** verification does not land — the whole version is refused and the prior cached version keeps serving. Refuses **both** failure classes (below). |
| `warn` *(default)* | A GPG **signature mismatch** (forgery signal) is refused, exactly as under `strict`. A **checksum / non-forgery** failure lands anyway, logs a `WARN`, and records `verified=0`. |
| `off` | No verification runs; artifacts land unchecked and `verified` stays `NULL`. |

Verification is **admission-time only** and applies to the newest discovered version; a passing
version records `verified=1`, and a version with no verification mechanism records `verified=NULL`
(see [schema/DATABASE.md](schema/DATABASE.md)). The verdict is surfaced in the Cache view and via
`POST /api/v1/cache/{id}/reverify`.

**Scope — `strict` does not refuse "unverifiable" OSes.** `strict` means *verifiable artifacts that
fail verification do not land*. It does **not** refuse OSes or versions that have **no** verification
mechanism — Talos, Debian, and FCOS pattern-fallback pins (a manually pinned older FCOS version with
no per-artifact `sha256`) — which land with `verified=NULL` under **every** policy.

### Failure classes — what the default `warn` does (D15)

`warn` is **not** "provenance is advisory." A verification failure is classified, and the two classes
are treated differently:

- **Signature mismatch (forgery signal)** — a GPG `.sig` that does not validate against the embedded
  key. This is a tamper indicator: it is **refused even under `warn`** (the version does not land and
  does not boot), identically to `strict`.
- **Corruption / non-forgery failure** — a SHA-256 mismatch, a short/unparseable sidecar, or an
  unknown/expired signing key. Under `warn` these **land** (logged, `verified=0`) — the availability
  trade-off `warn` exists for; under `strict` they are **refused**.

So `strict` refuses every failure class; `warn` refuses only forgeries; `off` verifies nothing.

**`warn` is advisory against a capable network attacker — use `strict` in production.** Because a
Flatcar artifact and its `.sig` are fetched from the **same channel**, an attacker who can tamper the
artifact can also substitute a self-signed `.sig`. That substitution surfaces as an *unknown-issuer*
verdict, which is the **corruption** class — so it **lands under `warn`** (`verified=0`, logged) even
though it is really an attack. `warn` only reliably stops the naive/accidental case (corruption, or a
forged `.sig` still signed such that it fails as a signature mismatch). **`strict` is the only policy
that closes this hole** — recommend `strict` for production.

**Policy is non-retroactive.** Tightening `warn` → `strict` does **not** auto-evict a version already
admitted under `warn`: the reconciler's idempotency skip guard leaves settled (`cached=1`, files
present) versions in place and does not re-verify them. The recourse is
`POST /api/v1/cache/{id}/reverify`, which re-checks the version under the current policy and re-records
`verified=0` so the operator can **see** it; removal is then a manual decision (`DELETE` is `403`
until auth lands in P10).

### Flatcar signing-key rotation runbook

The Flatcar image-signing public key is **embedded at compile time**
(`//go:embed pkg/ostype/keys/flatcar.asc`) — there is no hot reload. Flatcar signs releases with
**rotating subkeys**; the currently-active signing subkey
`52F145DFD00BBDCD928CBB5A32DA80F91EF52974` **expires 2027-03-08**. Before that date (or whenever
Flatcar rotates early), the operator must ship a **new booty release** that re-vendors the key:

1. Re-fetch the published key into `pkg/ostype/keys/flatcar.asc`
   (from <https://www.flatcar.org/security/image-signing-key/Flatcar_Image_Signing_Key.asc>).
2. Update the expiry-horizon assertion in `pkg/ostype/flatcar_key_test.go` (which pins the active
   subkey fingerprint and asserts it does not expire before the horizon date).
3. Build and redeploy.

Until that release ships, an expired/rotated key makes Flatcar verification fail as an
**unknown/expired signing key** (the corruption class): under `strict` this **halts all new Flatcar
caching** (a provisioning outage fixable only by a code release), while under `warn` the version
still **lands** with `verified=0`.

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
