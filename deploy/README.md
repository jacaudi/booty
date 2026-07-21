# Deploying booty with Docker Compose

A ready-to-run Docker Compose pack for running booty as a home/NAS network-boot
management plane: PXE boot server (TFTP + proxyDHCP), OS image cache, and
management UI in a single container.

## Prerequisites

- **A host on the same L2 segment as your PXE clients.** proxyDHCP relies on
  LAN broadcast traffic and answers alongside your router's existing DHCP
  server, so the container needs `network_mode: host` — it cannot run behind
  Docker's default bridge network or a NAT.
- **Access to the private image.** `ghcr.io/jacaudi/booty` is a private GHCR
  image — run `docker login ghcr.io` with a PAT that has `read:packages`
  before pulling. Alternatively, uncomment `build: ..` in
  `docker-compose.yml` (and comment out `image:`) to build from source
  instead.
- Docker Compose v2 (`docker compose`, not the standalone `docker-compose`).

## Quick start

1. Edit `deploy/docker-compose.yml` and set `--serverIP` to this host's real
   LAN IP address (e.g. `192.168.1.10`). This is required: booty's proxyDHCP
   responder refuses to start against a loopback, unspecified, or unparsable
   address, since PXE clients need a real address to fetch boot files from.
2. (Optional) copy `deploy/catalog.yaml` into the `booty-data` volume at
   `/data/catalog.yaml` to customize which OS images are cached — see the
   comments in that file for the schema. If you skip this, booty falls back
   to a built-in default set (Flatcar stable + lts, Talos, Debian 13
   netinst) driven by its channel/schematic flags.
3. Bring it up:

   ```bash
   docker compose -f deploy/docker-compose.yml up -d
   ```

4. Open the UI at `http://<serverIP>:<httpPort>/ui/` (defaults to
   `http://<serverIP>:8080/ui/`). Liveness/readiness is available at
   `/healthz`.

## Ports in play

Because the container runs with `network_mode: host`, it binds directly on
the host's network stack — there is no `ports:` mapping to configure. The
ports it uses:

| Port         | Protocol | Purpose                                                |
|--------------|----------|---------------------------------------------------------|
| `--httpPort` (default `8080`) | TCP | Management UI (`/ui/`), API, and boot-artifact/HTTP serving |
| 69           | UDP      | TFTP — stage-1 iPXE binary handoff                      |
| 67, 4011     | UDP      | proxyDHCP — PXEClient-only DHCP responses (67 = broadcast pass, 4011 = ProxyDHCP port for the second pass) |

proxyDHCP only answers requests carrying the `PXEClient` vendor class and
assigns no IP leases, so it safely coexists with your router's or NAS's
existing DHCP server — it never competes for address assignment.

## Persistence

The `booty-data` named volume, mounted at `/data`, holds:

- The SQLite database (host inventory, cluster state, config revisions).
- Cached OS images and boot artifacts (Flatcar/Fedora CoreOS/Talos/Debian).

This volume survives container restarts and image upgrades. Back it up if
you want to preserve host approvals, cluster configuration, or avoid
re-downloading cached images after a rebuild.

## Security posture — trusted LAN only

booty does not yet have authentication (tracked in #21/#5). Destructive
`DELETE` endpoints are already disabled, but `/register` (host self-registration)
and `/data/` (served artifacts) are open to any client that can reach the
container. **Do not expose booty to the public internet or an untrusted
network** — run it only on a trusted home/lab LAN, and keep it off any
network segment you don't control.

## Known follow-up: container healthcheck

The runtime image is distroless/shell-less, so this Compose file has no
container `healthcheck:` directive — there's no shell inside the image to run
one via `CMD`. `restart: unless-stopped` covers crash recovery in the
meantime. A future `booty healthcheck` subcommand (invoked as the container's
`HEALTHCHECK` via `CMD` on the compiled binary itself, no shell required)
would let Compose/Docker report container health directly; until then, use
the `/healthz` HTTP endpoint from outside the container to monitor liveness.
