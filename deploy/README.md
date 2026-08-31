# Deploying booty with Docker Compose

A ready-to-run Docker Compose pack for running booty as a home/NAS network-boot
management plane: PXE boot server (TFTP + proxyDHCP), OS image cache, and
management UI in a single container.

## Prerequisites

- **A host on the same L2 segment as your PXE clients.** This is a hard
  requirement of booty's boot path, not just of this Compose file: booty
  identifies a booting machine by resolving its MAC with ARP, and ARP does not
  cross a router. A client on a different VLAN or subnet cannot be identified,
  so it is served the holding loop instead of its assigned OS — **swapping
  proxyDHCP for your own DHCP server's `next-server` does not lift this**
  (routed VLAN support is tracked in
  [#71](https://github.com/jacaudi/booty/issues/71)). proxyDHCP adds a second
  constraint on top: it answers on LAN broadcast and does not honour `giaddr`,
  so it cannot serve relayed requests either. Between them, the container needs
  `network_mode: host` — it cannot run behind Docker's default bridge network
  or a NAT.
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

## Security posture — token-authenticated, trusted LAN only

The `/api/v1` management surface — host registration (`POST /api/v1/hosts`), approval, config/cluster/
cache edits, cluster-member removal, and everything else under `/api/v1` — now requires a credential.
Seven resource-deletion endpoints (cluster/config/target/cache/role/target-version, plus editing a
host) still return `403`, unauthenticated or not, because they are wired but not yet implemented;
`DELETE /api/v1/hosts/{mac}` is implemented and credentialed like the rest.

**Credential.** Two forms: an `X-Booty-Token: <token>` header (scripts, `curl`), or a session cookie
the web UI obtains once via `POST /login` and then sends automatically. The token itself lives at
`<dataDir>/api-token` (mode `0600`) and is logged exactly once on first run — `docker logs booty | grep
token`. See the top-level [`README.md`](../README.md#authentication) for the full token lifecycle
(`booty token print` / `rotate`, `SIGHUP` reload, `--apiToken`/`BOOTY_API_TOKEN`, `--noAuth`).
If the container crash-loops on `auth: token file ... is blank`, delete `<dataDir>/api-token` and
restart the container — booty mints and logs a fresh token exactly once, as on first run.

**Cleartext caveat — read this before assuming the token protects you on the wire.** This Compose
stack serves plain HTTP by default. With the cookie's `Secure` flag off (which it is, on plain HTTP),
**both the session cookie and the `X-Booty-Token` header travel the LAN in cleartext.** `HttpOnly` and
`SameSite=Strict` defend the cookie against XSS and cross-site request forgery — they do **not**
defend against anyone capturing packets on the same LAN segment. Token auth raises the bar against
casual and programmatic unauthorized access (a stray client, a misconfigured script, a scan), not
against an attacker who can already sniff your network traffic. Terminating TLS in front of booty
(directly, or via a reverse proxy — booty honors `X-Forwarded-Proto`) flips the cookie's `Secure` flag
on and encrypts both credential forms in transit.

**`/data/` serving surface.** Only `<dataDir>/cache/` (boot artifacts) and `<dataDir>/public/`
(operator-published assets fetched by booted nodes) are served under `/data/`; everything else under
`/data`, including `<dataDir>/config/` and the SQLite database, answers `404`.

**Upgrading an existing deploy.** On the first restart with this version, booty mints a new token, logs
it once, and starts enforcing the gate:

1. `docker logs booty | grep token` — capture the token before it scrolls out of the log.
2. Open the UI at `/ui/` and paste the token in when prompted; it is exchanged for a session cookie and
   you won't be asked again on that browser.
3. Update any external script or automation that called the old open endpoints to send
   `X-Booty-Token: <token>`.
4. If anything still calls `POST /register`, migrate it to `POST /api/v1/hosts` (same fields, plus the
   token header) — the old route is gone.

**Do not expose booty to the public internet or an untrusted network.** Token auth narrows who can act
without a credential; it does not make booty safe to expose past your LAN boundary. Run it only on a
trusted home/lab LAN, and keep it off any network segment you don't control.

## Known follow-up: container healthcheck

The runtime image is distroless/shell-less, so this Compose file has no
container `healthcheck:` directive — there's no shell inside the image to run
one via `CMD`. `restart: unless-stopped` covers crash recovery in the
meantime. A future `booty healthcheck` subcommand (invoked as the container's
`HEALTHCHECK` via `CMD` on the compiled binary itself, no shell required)
would let Compose/Docker report container health directly; until then, use
the `/healthz` HTTP endpoint from outside the container to monitor liveness.
