# booty: project memory

Fork of `jeefy/booty` (this repo: `jacaudi/booty`; Go module path is still `github.com/jeefy/booty`).
booty is a single Go binary that network-boots machines (PXE/iPXE over TFTP + HTTP, optional
proxyDHCP), caches OS boot artifacts, and serves each host its boot configuration. State lives in
one data directory (SQLite + an artifact cache); a React UI and an authenticated JSON API manage it.

The two codebases share history only up to October 2024 (`d9eafed`) and were rewritten independently; nothing merges
mechanically. Anything taken from upstream is a hand port (see
`docs/designs/2026-10-11-upstream-import-design.md`).

## Rule zero: verify, never assume

This rule outranks every other instruction in this file.

- **Every claim about code, a library, a protocol or a tool is checked before you rely on it.** Read
  the source (`file:line`), run the command, or fetch the official doc. Cite what you checked.
- **"I don't know" is acceptable. Guessing is not.** If something cannot be verified, say so,
  write `UNVERIFIED` next to it, and say what would verify it. Never present a guess as a fact,
  and never fill a gap in a plan or design with a plausible default without labelling it.
- **Ask when the answer changes what gets built** and only the maintainer can decide it. Do not
  ask what the code, the docs or a quick experiment can answer: go and find out.
- **Designs leave no open questions.** A design is done when a builder could implement it
  without asking anything or assuming anything. Every choice is either the maintainer's decision
  (recorded as such) or a researched call (recorded with its evidence).
- **Plans are executable by a smaller model.** An implementation plan names exact files, functions,
  line anchors, code to write, commands to run and their expected output, so a Haiku- or
  Sonnet-class model can carry it out with zero judgement calls. If a step needs judgement, the
  plan is not finished.
- **Evidence before "done".** Do not claim something works, passes or is fixed without running
  the check and reading its output.

## Mission and priorities

booty is the starting point for Kubernetes control-plane nodes, the "scaler" that adds
agent/worker nodes, a consistent configuration source for every other OS it boots, and a source
of bootable tools. Configuration and repeatable extensions are the product.

In priority order:

1. **Talos Linux first.** PXE-boot Talos and supply its machine config. The Talos-based hypervisor
   (an extension to Talos itself) is a primary target: booty supports the features it needs.
   Cluster secrets (the Talos secrets bundle: cluster/OS/etcd/aggregator CAs, service-account key,
   tokens) are to be **stored in an external secret store and read at render time**, through a
   secrets seam with three backends: **local age** (today's), **1Password**, **OpenBao**.
   That work has its own future design. Facts gathered so far (2026-10-11):
   - `onepassword-sdk-go` v0.4.1, MIT, builds with `CGO_ENABLED=0` (WASM core, pure-Go runtime;
     client start-up ~3.7 s here, so create once and reuse).
   - `github.com/openbao/openbao/api/v2` (MPL-2.0) has KV v2 helpers.
   - Talos control-plane configs must carry every CA private key, so a PKI engine cannot keep
     them non-exportable.
2. **Kubernetes via kubeadm on Flatcar and Fedora CoreOS**, and possibly Bluefin Server.
   Upstream supports Bluefin Server as a kubeadm **worker only**; its kubeadm sysext pins
   Kubernetes 1.35.9 and upstream has no version-skew check.
3. **Flatcar with systemd-sysext running KubeSolo** as a backup to a single-node Talos. KubeSolo
   (Portainer, MIT) has no Flatcar sysext in `flatcar/sysext-bakery`; one would have to be built.
   Its install script refuses to run when Docker is present.
4. **Generic Linux for desktops/laptops:** Debian and Debian-based; Arch and Arch-based (CachyOS,
   SteamOS, EndeavourOS); Tails.
   - Arch's archiso has official PXE support.
   - No official netboot path was found for SteamOS or CachyOS (UNVERIFIED for CachyOS/EndeavourOS).
5. **Server OSes:**
   - Proxmox VE and Proxmox Backup Server. The installer supports PXE via
     `prepare-iso --pxe --pxe-loader ipxe` from VE installer 9.1.7; the first PBS version is
     UNVERIFIED.
   - TrueNAS: no PXE path found.
6. **Utility OSes** (rescue, disk, memory tools). Several already ship via netboot.xyz manifests.

## Engineering principles (apply to every change)

- **KISS / YAGNI.** Build the smallest thing that works, with no speculative options, layers or flags.
- **DRY.** One implementation per behaviour. Example: TFTP and HTTP share one boot-script renderer.
- **SOLID.** Small single-purpose types that depend on narrow interfaces, not concretions.
- **12-Factor.**
  - Config comes from flags and env (viper keys equal flag names).
  - Logs are structured `log/slog` to stderr (see `docs/standards.md` §1.7).
  - Start up fast and shut down gracefully (cancel context, drain servers, bounded timeouts).
- **Idiomatic, modern Go.**
  - Standard library first (`log/slog`, `net/http` method+path patterns, `embed`, `context`,
    `errors.Is/As/Join`, `os.Root`, `testing/synctest`, `t.Context()`).
  - Accept interfaces, return structs. Wrap errors with `%w`. Use table-driven tests.
  - No new package-level mutable state.
  - Code passes `gofmt`, `go vet`, `golangci-lint` v2 and `govulncheck`.
  - **Lint gate:** `.golangci.yml` is maratori's golden config (v2.14.0) with `paralleltest` and
    `testpackage` disabled. New and changed code must pass
    `golangci-lint run --new-from-merge-base=origin/main ./...`.
  - **Modern idioms:** before editing a `.go` file, run the vendored JetBrains `use-modern-go`
    skill (`sh .claude/skills/use-modern-go/scripts/run-tool.sh list --file-path <file>`) and apply
    what it returns.
  - Follow go.dev guidance (Effective Go, Code Review Comments, Go Proverbs).
- **Seam.** One interface per capability: OS type, secret store, boot-script renderer, power
  action, notifier and so on. A seam has one or more **backends**, each in its own package and
  each a drop-in implementation of that interface.
- **Patchbay.** The single place that picks a backend for each seam and plugs it in. Swapping or
  adding a backend means "add a package, plus one case in the patchbay (or a config value)",
  never a refactor of the callers. Backends never construct other backends.
  - `pkg/ostype`'s registry is today's closest thing to a patchbay (OSes register themselves via
    `init()`).
  - New seams (the secrets store first) get an explicit patchbay wired from `cmd/`.
- **Security invariants.**
  - Every state-changing endpoint lives under `/api/v1`, behind the auth gate.
  - Boot-path endpoints (`/ignition.json`, `/machineconfig`, `/preseed`, `/booty.ipxe`,
    `/boot/*`, `/data/*`) are read-only.
  - Files under `dataDir` are served only from an explicit allowlist, on HTTP and TFTP alike.
  - Secrets are never logged and never written outside their store.
  - Any new unauthenticated endpoint carries its own credential and bounded, pre-auth rate limiting.

### How the principles apply to existing code

The maintainer's decision (2026-10-11): **new code must follow the principles; code a change
touches moves toward them within that change's scope; there is no big-bang refactor.** Known
debt, not to be copied into new code:

- `viper.Get*` is read deep inside `pkg/tftp`, `pkg/http`, `pkg/cache` and others.
- New exported helpers `tftp.URLHost` and `tftp.TopLevelBootFiles` read viper directly (accepted in the upstream import; move to parameters when touched).
- Package-level injected stores: `hardware.SetStore`, `tftp.SetStore`, `absDataDir`.
- The layout is `pkg/` rather than `internal/`, and there is no patchbay package.
- Servers call `os.Exit` inside goroutines (TFTP, HTTP).
- Existing `golangci-lint` findings (1,231 with the golden config at `7432c81`) are not yet fixed.
  Only new and changed code is gated. govulncheck findings are fixed in the upstream-import work.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/` | Cobra entrypoint. `main.go` registers flags into an `args` struct in `init()` and binds them all with one `viper.BindPFlags(flags)`. It also holds the `run` startup sequence and ordered shutdown, and the `token` and `convert-preseed` subcommands. |
| `pkg/config/` | Viper key constants, `LoadConfig` defaults/env bindings, path helpers, `--secretsKey` (age) loading. |
| `pkg/auth/` | Shared API token store (`<dataDir>/api-token`, 0600) and the stateless signed session cookie. Leaf package. |
| `pkg/db/` | SQLite (`modernc.org/sqlite`, pure Go) typed store. Migrations are embedded `migrations/NNNN_name.sql` files, applied in order against `PRAGMA user_version`; there are no down migrations. |
| `pkg/hardware/` | Host model (`Host`), MAC normalisation (`NormalizeMAC`), get/write host helpers over `pkg/db`. |
| `pkg/ostype/` | OS taxonomy: families (ignition, talos, debian, tool), OS registrations, version discovery, verification keys. |
| `pkg/cache/` | Artifact cache: catalog (`catalog.yaml`), reconciler, download/verify, retention/eviction, Debian DVD handling. |
| `pkg/checksum/` | `sha256sum`-format digest parsing. |
| `pkg/tftp/` | TFTP server, `booty.ipxe` dispatch (holding / assigned / menu), boot menu, per-OS iPXE templates and tool scripts. |
| `pkg/http/` | Base mux (boot-path endpoints, `/data/` allowlist, login/logout, UI) plus the huma `/api/v1` surface. Also config rendering for Butane→Ignition, Talos machineconfig and debianconfig→preseed, and Talos cluster generation/import. |
| `pkg/proxydhcp/` | Default-off proxyDHCP responder (UDP 67 + 4011). |
| `pkg/power/` | Wake-on-LAN. |
| `pkg/nodereport/` | Node health reports (opt-in `--nodeReports`). |
| `boot/` | Embedded iPXE binaries (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`), regenerated with `go generate`. |
| `test/qemu/` | UEFI boot-chain check under QEMU. |
| `web/` | React + Ant Design UI (Vite, Vitest). The built `dist/` is embedded by `web/embed.go`. |
| `docs/designs/` | Design documents, one per change (`YYYY-MM-DD-<topic>-design.md`). |
| `docs/schema/` | API, catalog, database and storage references. |
| `examples/`, `deploy/` | Example Butane/scripts, Kubernetes manifest, docker-compose. |
| `.claude/skills/` | obra/superpowers skills and JetBrains `use-modern-go`, vendored unmodified (see its README). |

## How a boot works (today)

1. The site DHCP server (or booty's proxyDHCP) points firmware at booty's TFTP and an iPXE binary.
   booty embeds iPXE (`boot/`) and serves it over TFTP and `/boot/`.
2. iPXE fetches `booty.ipxe` over TFTP, which is a stub that chains to HTTP `/booty.ipxe?mac=`.
   That renders one of three scripts: holding (unknown or unapproved), assigned OS, or boot menu.
   The host is identified by the `?mac=` iPXE reports; **ARP** on the request's source IP is the
   fallback.
3. The OS kernel/initrd come from `/data/cache/...` over HTTP. The config comes from
   `/ignition.json`, `/machineconfig` or `/preseed`, identified by `?mac=` or ARP.
4. Unknown MACs get a reboot-loop config and appear as unknown hosts until approved.

## Conventions

- **Flags.**
  - Add a field to `args`, add `flags.XxxVar(...)` in `cmd/main.go` `init()`, and add a key
    constant in `pkg/config/config.go`.
  - Optionally add a `viper.SetDefault` in `LoadConfig`.
  - Read values only via `viper.Get*(config.Key)` in existing code. New code receives values as
    parameters or struct fields.
- **API.**
  - Use huma operations in `pkg/http/api_*.go`, registered on the gated group, returning
    `huma.ErrorNNN`.
  - Boot-path handlers use `writeError` (`pkg/http/errors.go`).
  - API tests build the server with no `Auth` (see `newTestAPI` in `pkg/http/api_test.go`).
- **Migrations.**
  - Add the next `NNNN_name.sql`.
  - Update the hard-coded `user_version` assertions in `pkg/db/migrate_test.go`.
  - The `ALTER TABLE ... ADD COLUMN ... NOT NULL DEFAULT` pattern is established.
- **Tests.**
  - `go test -race ./...` must pass; the baseline is about 720 Go tests in about 60 s.
  - Use `restoreViper` (cmd) or `viper.Reset(); t.Cleanup(viper.Reset)` to isolate viper state.
  - Web: `cd web && npm ci && npm test` (Vitest), and `npx tsc -b` for type checks.
- **Ported upstream code** keeps a one-line provenance comment naming the upstream file and
  commit. Upstream is MIT, same licence and copyright holder as this repo.
- **Design and plan documents.**
  - Brainstorm and design with the superpowers `brainstorming` skill and plan with `writing-plans`.
  - Designs go in `docs/designs/`, plans in `docs/plans/`.

## Working in this repo

- Build/test: `go build ./...`, `go vet ./...`, `go test -race ./...`.
  - The `go.mod` toolchain line selects go1.26.9; the system `go` here may be older and
    auto-switches.
  - The Makefile is gone; `taskfile.yml` (go-task) is the build tool.
  - The golangci-lint gate needs v2.14.0 built with Go >= 1.26, at `$HOME/go/bin`.
- CI: `.github/workflows/go-test.yml` (build, vet, `go test -race`) and `web-test.yml`. Publish
  workflows are being removed and will be rebuilt later.
- Real network boots need L2 adjacency, UDP 67/69/4011 and `CAP_NET_BIND_SERVICE` + `CAP_NET_RAW`.
  Never assume a PXE client or QEMU exists. Check (`which qemu-system-x86_64`) and install if
  the task needs it.
- Commits: Conventional Commits style (`fix(tftp): ...`, `docs(design): ...`). Attribution
  trailers are disabled in `.claude/settings.json`.
