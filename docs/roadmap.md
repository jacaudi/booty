# booty roadmap

Milestones toward the mission in `.claude/CLAUDE.md`, in order. Each milestone ends with a
demonstrable result. GitHub milestones carry the same names; issues attach to them.

| # | Milestone | Needs |
|---|---|---|
| M0 | Hardening and groundwork | none |
| M1 | Secrets backends | M0 |
| M2 | Talos cluster lifecycle | M1 |
| M3 | Discovery OS | M0 (can run alongside M1) |
| M4 | Scaler controller | M2, M3 |
| M5 | Node agent | M2 |
| M6 | Utility OSes and Tails | none |
| M7 | Generic Linux (Arch) | none |

## M0: Hardening and groundwork

- Stop logging the rendered Butane template (it carries the join token): `pkg/http/ignition.go:189,202`.
- Fail fast when startup or the TFTP listener fails, before the cache reconciler's first pass.
- Persist unknown MACs in the database so a restart or spoofed `?mac=` requests cannot evict them (#89).
- Let the UI see and approve an unknown host (#78).
- CI enforces lint (`task lint:new`), `gofmt` and `govulncheck`.
- Correct the statements in `.claude/CLAUDE.md` that are no longer true.

**Done when:** a new machine appears in the UI, survives a restart, and is approved from the UI.

## M1: Secrets backends

- A secret-store interface and a patchbay wired from `cmd/`.
- Move today's age implementation behind it as the first backend.
- Decide whether control-plane configs are rendered on demand or their frozen copies live behind
  the store (today they sit in SQLite and hold every CA private key).
- 1Password backend, then OpenBao backend.

**Done when:** no CA private key is stored in SQLite, and switching backends is a config value.

## M2: Talos cluster lifecycle

Lifecycle covers clusters booty creates **and** existing clusters it onboards, and several
clusters at once. Each operation is an idempotent step that the UI, the API and the M4 controller
all call.

| Step | New cluster | Imported cluster |
|---|---|---|
| Bootstrap etcd | booty does it | skipped (already running) |
| talosconfig / kubeconfig | issued by booty | issued from the imported secrets |
| Add a control-plane or worker node | yes | yes |
| Gate joins on the control plane being reachable | yes | yes |
| Schematic kernel args on the PXE command line (hypervisor) | yes | yes |

Already present on `main` (`5dd504f`): many clusters in the `clusters` table, and import with
several control-plane hosts (`pkg/http/api_clusters.go:653`).

**Done when:** one booty runs two clusters, one created and one imported, adds a control-plane
node and a worker to each, and no operation on one touches the other; no hand-run `talosctl`.

## M3: Discovery OS

- Spike: evaluate gokrazy. Whether it netboots from a kernel and initrd is UNVERIFIED and decides
  go/no-go.
- Inventory report endpoint with its own credential and bounded pre-auth rate limiting.
- Holding pattern, and handoff to the assigned OS once a machine is approved and assigned.
- Hardware facts shown in the UI.

**Done when:** a first-seen machine boots the discovery OS, its hardware appears in booty, and it
waits there until assigned.

## M4: Scaler controller

An automated process in the spirit of Cluster API or Talos Omni.

- Declared state per cluster: node counts and node types (for example 3 control-plane nodes,
  2 GPU workers for AI, 1 SDR node).
- A reconcile loop matches the approved, unassigned pool (from M3) to what is declared, picks the
  best-fitting machine by its hardware, assigns it, boots it and bootstraps it through the M2 steps.
- Workload demand (for example 20 waiting CI jobs) raises the declared count; it is not a
  separate code path. The source of that signal (the Kubernetes API or a CI webhook) is a design
  decision.
- Needs its own design document before code.

**Done when:** raising a declared count brings a parked machine of the right type into the cluster
with no manual step.

## M5: Node agent

A DaemonSet container on every node that is to booty what a VM guest agent is to KVM.

- Design first: how it is built, deployed and authenticated.
- Telemetry to booty: CPU, memory, disk status and capacity, network, devices added.
- Light controls from booty: power off, reboot.

**Done when:** booty shows live telemetry for every node and can reboot one.

## M6: Utility OSes and Tails

Seven tools and Tails already boot from the menu. Close the gaps only (arm64; anything beyond
menu-only use).

## M7: Generic Linux (Arch)

Debian already installs end to end. Arch needs a new OS family, not a tool entry; archiso has
official PXE support.

## Backlog

kubeadm on Flatcar and Fedora CoreOS; Flatcar with sysext running KubeSolo; Proxmox VE and PBS;
TrueNAS. See `.claude/CLAUDE.md`.
