# booty

**A small, self-contained network-boot (PXE/iPXE) server for Flatcar Linux, Fedora CoreOS, and
Talos Linux.**

booty discovers the latest releases of each supported OS, caches their boot artifacts
(kernel + initramfs) locally, and serves a per-host boot configuration over TFTP and HTTP — so a
bare-metal or virtual machine set to network-boot comes up running the OS you assigned it, with the
Ignition / Talos machine config you provided. It is a single static binary (plus an embedded web
UI), runs happily in a container, and keeps its state in a single data directory.

It is **not** a DHCP server (though it ships an optional *proxyDHCP* responder for environments
where you can't point your existing DHCP server at it), and it is **not** a general image registry.
It does one job: get your machines netbooted into the right OS.

---

## What it does

- **Tracks OS releases automatically.** A background scheduler (default every 5 minutes) checks
  upstream for new Flatcar, Fedora CoreOS, and Talos releases.
- **Caches boot artifacts on disk.** Kernel and initramfs (and rootfs, for CoreOS) are downloaded
  once and served locally from `<dataDir>/cache/...`, so boots don't depend on upstream
  availability or speed.
- **Serves per-host boot configs.** Each registered host (keyed by MAC) is mapped to an OS and an
  optional config template. booty renders and serves the right Ignition JSON (Flatcar / CoreOS) or
  Talos machine config (Talos) at boot time.
- **Drives the full iPXE chain over TFTP.** It answers the magic `booty.ipxe` filename with a
  dynamically generated, per-host iPXE script that points the machine at the cached kernel,
  initramfs, and config.
- **Holds unknown machines safely.** A machine whose MAC isn't registered gets a reboot-loop
  "holding" config instead of booting into something unintended — and shows up in the UI as an
  unknown host awaiting registration.
- **Optional proxyDHCP.** When you can't reconfigure your LAN's DHCP server, booty can answer PXE
  boot requests alongside it (without handing out IP leases).
- **Embedded web UI + JSON API** for inspecting versions and hosts, and registering machines.

## Supported operating systems

| OS | Discovery source | Boot config |
|----|------------------|-------------|
| **Flatcar Linux** | `<channel>.release.flatcar-linux.net` | Ignition (rendered from a Butane template) |
| **Fedora CoreOS** | `builds.coreos.fedoraproject.org` streams | Ignition (rendered from a Butane template) |
| **Talos Linux** | Talos Image Factory (`factory.talos.dev`) | Talos machine config (per-host schematic supported) |

## How a boot works

```
  machine (network boot)
        │  1. DHCP hands out an IP + points at a TFTP/next-server
        │     (your DHCP server, or booty's optional proxyDHCP)
        ▼
  booty TFTP (:69/udp)
        │  2. serves an iPXE binary, then answers "booty.ipxe"
        │     with a per-host script (host looked up by MAC via ARP)
        ▼
  booty HTTP (:8080)
        │  3. serves cached kernel + initramfs from /data/...
        │  4. serves /ignition.json or /machineconfig for this host
        ▼
  machine boots the assigned OS
```

A host booty doesn't recognize is served a reboot-loop config at step 2/4 and recorded as an
*unknown host* until you register it.

---

## Quickstart (Docker)

**Prerequisites:** Docker, a layer-2 network where target machines can reach this host, and a way to
point those machines at booty for network boot — either your DHCP server's `next-server` / bootfile
options, or booty's `--proxyDHCPEnabled`.

1. **Create a data directory** with your config template(s):

   ```bash
   mkdir -p data/config
   # Butane template for Flatcar / CoreOS hosts (an example ships in the repo):
   cp examples/config/ignition.yaml data/config/ignition.yaml
   # For Talos hosts, provide your own machine-config template at:
   #   data/config/machineconfig.yaml
   ```

2. **Run booty:**

   ```bash
   docker run -ti --rm \
     -v "${PWD}/data:/data" \
     -p 8080:8080 \
     -p 69:69/udp \
     ghcr.io/jacaudi/booty:latest \
     --dataDir=/data \
     --serverIP=10.0.0.5 \        # the LAN IP clients should reach booty on
     --serverHttpPort=8080
   ```

   (Or build it yourself — see *Building from source* below.)

3. **Register a machine** so it boots an OS instead of the holding loop:

   ```bash
   curl -X POST http://localhost:8080/register \
     -H 'Content-Type: application/json' \
     -d '{"mac":"aa:bb:cc:dd:ee:ff","hostname":"node1","os":"talos"}'
   ```

   `os` is one of `flatcar`, `coreos`, `talos`. Talos hosts may also set `"schematic":"<id>"` to
   use a custom Image Factory schematic.

4. **Point the machine at booty** — set your DHCP `next-server` to booty's IP and the bootfile to an
   iPXE binary, or start booty with `--proxyDHCPEnabled` (then also publish `-p 67:67/udp
   -p 4011:4011/udp` and add `--cap-add=NET_BIND_SERVICE`). Network-boot the machine: it caches on
   first run and comes up on the assigned OS.

Visit **`http://localhost:8080/ui/`** for the web UI, or **`/booty.json`** for the raw host +
version state.

---

## Inspecting state

A few read-only endpoints let you see what booty is doing:

```bash
curl http://localhost:8080/info          # cached OS versions + booty build info
curl http://localhost:8080/booty.json    # all registered hosts + unknown (unregistered) hosts
curl 'http://localhost:8080/hosts?mac=aa:bb:cc:dd:ee:ff'   # one host by MAC
```

The web UI at `/ui/` presents the same host and version information, and lets you add, edit, and
remove hosts.

## Concepts

- **Registered vs. unknown hosts.** booty only boots machines it knows. A machine is *registered* by
  mapping its MAC to an OS (via `POST /register` or the web UI). A machine whose MAC isn't
  registered is an *unknown host*: it's served a reboot-loop config and listed under `unknownHosts`
  in `/booty.json` until you register it. This prevents a stray machine from booting into something
  unintended.
- **One-shot install.** A host can carry a `DoInstall` flag. The first time it fetches `booty.ipxe`,
  booty flips the flag off — so you can boot an installer once, then fall through to booting from
  disk on subsequent network boots.
- **Per-host Talos schematics.** A Talos host may set its own Image Factory `schematic`; booty caches
  and serves artifacts for each schematic independently.

## Configuration (essentials)

booty is configured entirely by flags / environment variables (there is no config file). The ones
you most likely need:

| Flag | Default | Purpose |
|------|---------|---------|
| `--dataDir` | `/data` | State directory: cache, templates, host DB |
| `--serverIP` | `127.0.0.1` | LAN IP clients use to reach booty (set this!) |
| `--httpPort` | `8080` | HTTP listener port |
| `--serverHttpPort` | `80` | HTTP port advertised to clients (if different from the listener) |
| `--updateSchedule` | `*/5 * * * *` | Cron schedule for upstream version checks |
| `--talosArchitecture` | `amd64` | Talos arch (`amd64` / `arm64`) |
| `--talosRetainMinors` | `3` | Number of newest Talos minor lines to cache |
| `--proxyDHCPEnabled` | `false` | Answer PXE boot requests without handing out leases |

See **[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md)** for the complete flag / env reference.

## Building from source

Requires Go 1.26+ and Node.js (for the web UI).

```bash
make build          # → bin/booty (static, CGO_ENABLED=0)
make run            # build the web UI and run against ./data
make image          # build the container image
make docker-buildx  # multi-arch (linux/amd64, linux/arm64) image
```

The container is a distroless image with the binary at `/booty` and the web UI bundled in.

## Deployment notes

- **Kubernetes.** An example manifest lives at [`examples/k8s.yaml`](examples/k8s.yaml): a ConfigMap
  holding the Ignition/Butane template, a Deployment of booty, and a Service. booty typically runs
  with host networking so it can answer TFTP (and, optionally, proxyDHCP) on the LAN.
- **PXE vs. iPXE.** iPXE is recommended (faster, HTTP-capable). The bootfile you point firmware at
  differs: use an iPXE binary (e.g. `undionly.kpxe` / `ipxe.efi`) for the iPXE chain, or
  `pxelinux.cfg/default` for legacy PXE. With `--proxyDHCPEnabled`, booty selects the right pass-1
  binary by client architecture automatically.
- **Networking.** booty must share a layer-2 segment with the machines it boots (it resolves a
  requester's MAC by ARP). Make sure UDP/69 (and UDP/67 + 4011 for proxyDHCP) are reachable.

## Project status

booty works today as the netboot server described above. It is under active development toward a
**v1 management plane** — a target/cache API, persistent state in SQLite, uploadable boot configs,
schematics / talhelper integration, and a richer UI — delivered as a series of additive slices. The
boot path and on-disk contracts documented here remain stable; new capabilities are added alongside
them.

## License

See [`LICENSE`](LICENSE).

---

## Documentation

> The table of contents below links into [`docs/`](docs/). Detailed reference material lives there,
> not in this README.

- **[Configuration reference](docs/CONFIGURATION.md)** — every flag and environment variable.
- **[Documentation index](docs/README.md)** — guides and reference, top level.
- **Schema & contracts** ([`docs/schema/`](docs/schema/)):
  - **[API](docs/schema/API.md)** — HTTP endpoints, TFTP boot filenames, proxyDHCP behavior.
  - **[Database](docs/schema/DATABASE.md)** — the host database and version-metadata records.
  - **[Storage](docs/schema/STORAGE.md)** — the on-disk data-directory and cache layout.
