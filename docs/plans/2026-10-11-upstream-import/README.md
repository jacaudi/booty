# Upstream Import Implementation Plan (index)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Execute the phase files in the order below; each one is a self-contained plan.

**Goal:** Implement `docs/designs/2026-10-11-upstream-import-design.md`. That covers:
- two security fixes;
- five bug fixes;
- a self-contained boot chain (embedded iPXE, MAC identification, hardened TFTP, fixed proxyDHCP);
- Wake-on-LAN and opt-in node reports;
- dependency, toolchain and build clean-up.

**Architecture:** Hand ports from `jeefy/booty` @ `8a5a227` fitted to this fork's packages (`pkg/tftp`, `pkg/http`, `pkg/proxydhcp`, `pkg/db`, `pkg/hardware`), plus three new packages (`boot`, `pkg/power`, `pkg/nodereport`).

**Tech Stack:**
- Go: go1.26.2, moving to go1.26.9 in phase 7.
- Libraries: pin/tftp v3, insomniacslk/dhcp, huma v2, modernc sqlite.
- Web: React 18 + Ant Design 5 + Vitest.
- Tools: go-task, golangci-lint v2, govulncheck.
- Test infrastructure: QEMU/OVMF for the boot-chain check.

**Spec:** `docs/designs/2026-10-11-upstream-import-design.md` (v2). Read it before starting; every task's "why" is there.

## Phases (execute in this order)

| # | File | Items | Depends on |
|---|---|---|---|
| 1 | `01-security-fixes.md` | 0, 0b | none |
| 2 | `02-tftp-hardening.md` | 8, 3 | 1 |
| 3 | `03-proxydhcp.md` | 1 | none |
| 4 | `04-small-fixes.md` | 2, 4, 5 | 1 (uses `machineConfigHandler`) |
| 5 | `05-embedded-ipxe.md` | 6 | 2 (`tftp.Config`), 3 (QEMU check needs the new proxyDHCP) |
| 6 | `06-http-boot-script.md` | 7 | 1, 2, 5 |
| 7 | `07-dependencies.md` | 12 + toolchain | 2 |
| 8 | `08-wake-on-lan.md` | 9 | none |
| 9 | `09-node-reports.md` | 10 | 1, 2, 6, 7 |
| 10 | `10-build-and-tooling.md` | 11, 13 | 5, 7 |

## Global Constraints

These apply to every task in every phase. Values are copied from the design.

- **Rule zero (from `.claude/CLAUDE.md`): verify, never assume.**
  - If a file, line, function or output differs from what a step says, **stop**. Search for the quoted code, or the named function, to find where it moved. Proceed only if the change is a pure line shift.
  - Otherwise report the mismatch and do not improvise.
  - Never "make a test pass" by changing its expected value, unless the step says to.
- Go toolchain from `go.mod` (`go1.26.2` until phase 7, then `go1.26.9`). `CGO_ENABLED=0`.
- No new package-level mutable state. Dependencies (ARP lookup, detectors, clocks, embedded FS, wakers, keys) are passed as parameters or struct fields.
- Ported code carries a provenance comment, `// Ported from jeefy/booty <path> @ <commit> (MIT).` Upstream is MIT with the same copyright holder as this repo.
- Every new base-mux route is read-only, except `POST /node/report`, which authenticates itself. Every state-changing operation is a huma operation under `/api/v1`.
- Commits:
  - Conventional Commits.
  - **No attribution trailers** (`.claude/settings.json` disables them).
  - One commit per task unless the task says otherwise.
  - Run `go build ./... && go vet ./...` before every commit, plus the task's tests.
- Release notes accumulate in `docs/releases/UNRELEASED.md` (created in phase 1).
- Phase exit: `go build ./... && go vet ./... && go test -race ./...` all `ok`. Phases that touch `web/` also run `cd web && npx tsc -b && npm test`.

## Prerequisites (run once, verify the output)

1. Upstream remote:

```bash
git remote get-url upstream 2>/dev/null || git remote add upstream https://github.com/jeefy/booty
git fetch upstream main
git rev-parse upstream/main
```

   Expected: a sha that begins `8a5a227` or is newer. If it is newer, every `git show upstream/main:<path>` in this plan must be read at `8a5a227` instead. Replace `upstream/main` with `8a5a227` in those commands.
2. `go version` inside the repo prints `go1.26.2` (or `go1.26.9` after phase 7).
3. `node` and `npm` are installed (CI uses `node-version: lts/*`, `.github/workflows/web-test.yml`); `cd web && npm ci` succeeds.
4. Baseline: `go test -count=1 ./...` passes (about 720 tests, about 60 s; research `fork-internals.md` §11).

## Research (evidence for every claim)

`research/` holds the verification reports this plan was written from. Cite them when a step's behaviour surprises you. Paths under `/tmp/claude-0/…` inside them refer to the research session's scratch space, which no longer exists. The facts and the commands that produced them are what matter.
- `fork-internals.md`: fork conventions and touch points.
- `upstream-port.md`: what to port, with exact lines.
- `go-deps.md`: dependency API changes, `os.Root` semantics.
- `boot-chain.md`: iPXE settings and the DHCP protocol.
- `tooling-qemu.md`: go-task, golangci-lint, govulncheck, distroless, QEMU.
- `qemu-ovmf.md`: the UEFI recipe.
- `roadmap.md`: background for `.claude/CLAUDE.md`, not for this plan.

## Review Focus (cross-phase)

1. **Upgrade path for an existing data dir.** Migration 0010 applies cleanly to a database at `user_version` 9 that holds hosts. Phase 9 Task 5 tests it.
2. **A host that netboots through an old cached script** (`chain tftp://…/menu/…`) still works, because the TFTP `menu/` branch is kept. Phase 6 Task 1 keeps the branch, and the existing menu tests cover it.
3. **A deployment with `--proxyDHCPEnabled` and custom `--proxyDHCPBootfile*` names.**
   - Those names stay served over TFTP: phase 1 allowlist test.
   - proxyDHCP still offers them: phase 3 `TestConfigFileOverrides`.
4. **Shutdown with TFTP running** completes within the 5 s bound. Phase 2 `TestShutdownIsIdempotent`, plus the `stopWithTimeout` safety net.
5. **The full boot chain on UEFI firmware** with proxyDHCP beside a DHCP server that names another next-server. The phase 5 QEMU check.
