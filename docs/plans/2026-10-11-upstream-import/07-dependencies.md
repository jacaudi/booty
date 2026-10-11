# Phase 7: Remaining Go dependency bumps (item 12) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** viper, butane, ignition, godotenv, jsonparser and the dhcp library reach the versions verified in design §9.3, with one import change. The Go toolchain moves to go1.26.9 and `golang.org/x/net` / `google.golang.org/grpc` to their fixed versions, so govulncheck reports no called vulnerabilities (maintainer decision, design §8.9).

**Architecture:** `go get` + `go mod tidy`; the only code change is the Ignition types import in `pkg/http/ignition.go`.

**Tech Stack:** Go modules.

**Spec:** design item 12, §9.3; `research/go-deps.md`.

## Global Constraints

See `README.md`. pin/tftp v3 was done in phase 2.

## Review Focus

1. The reboot config for unknown hosts must be byte-identical before and after (verified in research; pinned by Task 1's test).
2. A Butane source using `variant: flatcar, version: 1.1.0` (the shipped example) must translate to the same Ignition JSON as before. Task 1 golden.
3. Flatcar `version.txt` parsing via godotenv must still yield `FLATCAR_VERSION`. Covered by existing `pkg/ostype` tests.
4. CoreOS stream JSON parsing via jsonparser must still yield release, location and sha256. Covered by existing `pkg/ostype` tests.
5. Viper duration flags (`--cacheInterval 5m`) must still parse. Covered by existing `pkg/config` and `cmd` tests.

---

### Task 1: Pin current Ignition outputs before bumping

**Files:** Create `pkg/http/ignition_golden_test.go`, `pkg/http/testdata/ignition/reboot.json`, `pkg/http/testdata/ignition/example-flatcar.json`.

- [ ] **Step 1: Write a golden test** `TestIgnitionOutputsStable` with an `-update-ignition` flag (`var updateIgnition = flag.Bool("update-ignition", false, "rewrite ignition goldens")`):
  - **Reboot config.** Call `handleIgnitionRequest(ignitionTestStore(t))` exactly as `TestHandleIgnitionRequest_EmptyMACServesRebootConfigQuietly` does (`pkg/http/ignition_test.go:35-70`), with the same template `variant: fcos\nversion: 1.5.0\n`. Compare the body with `testdata/ignition/reboot.json`.
  - **Shipped example.** `out, _, _, err := renderConfig("butane", <bytes of os.ReadFile("../../examples/config/ignition.yaml")>, TemplateVars{Hostname: "node1", ServerIP: "10.0.0.1:8080", JoinString: "kubeadm join x"})`; compare the output with `testdata/ignition/example-flatcar.json`.
- [ ] **Step 2:** Run it **before** bumping, with `go test ./pkg/http/ -run TestIgnitionOutputsStable -update-ignition`, then `go test ./pkg/http/ -run TestIgnitionOutputsStable`. Expected: `ok`.
- [ ] **Step 3:** Commit: `git add pkg/http && git commit -m "test(http): pin Ignition outputs before the butane/ignition bump"`.

### Task 2: Bump

**Files:** `go.mod`, `go.sum`, `pkg/http/ignition.go:15`.

- [ ] **Step 1:** Run:

```bash
go get github.com/spf13/viper@v1.21.0 github.com/coreos/butane@v0.29.0 github.com/coreos/ignition/v2@v2.27.0 github.com/joho/godotenv@v1.5.1 github.com/buger/jsonparser@v1.6.1 github.com/insomniacslk/dhcp@v0.0.0-20260901064844-234b97448fae
```

- [ ] **Step 2:** In `pkg/http/ignition.go:15`, change `coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"` to `coreOSType "github.com/coreos/ignition/v2/config/v3_4/types"`.
- [ ] **Step 3:** Run `go mod tidy && go build ./... && go vet ./...`. Expected: no errors.
- [ ] **Step 4:** Run `go test -race ./...`. Expected: all `ok`. `TestIgnitionOutputsStable` passes **without** `-update-ignition`; the research verified identical output. If it fails, stop and report the diff. Do not regenerate.
- [ ] **Step 5:** Run `git diff go.mod`. Expected direct requires: `viper v1.21.0`, `butane v0.29.0`, `ignition/v2 v2.27.0`, `godotenv v1.5.1`, `jsonparser v1.6.1`, `insomniacslk/dhcp v0.0.0-20260901064844-234b97448fae`.
- [ ] **Step 6:** Append to `docs/releases/UNRELEASED.md` `## Changes`: `Butane 0.29 / Ignition 2.27: the fcos 1.6.0-experimental variant no longer translates (use fcos 1.6.0); flatcar 1.2.0-experimental now emits Ignition 3.7.0-experimental. Stable variants are unchanged.`
- [ ] **Step 7:** Commit: `git add go.mod go.sum pkg/http/ignition.go docs && git commit -m "build(deps): viper 1.21, butane 0.29, ignition 2.27, godotenv 1.5.1, jsonparser 1.6.1, dhcp 2026-09-01"`.

### Task 3: Toolchain and vulnerability fixes

**Files:** `go.mod`, `go.sum`, `Dockerfile` (the `golang:1.26.2-alpine` build stage).

Verified at go1.26.2 (`research/tooling-qemu.md` §3): 29 called vulnerabilities.
- 24 are in the standard library and are all fixed by go1.26.9.
- `golang.org/x/net v0.53.0`: GO-2026-6617, 6612, 6611, 6610 and 6603 are fixed in v0.60.0; GO-2026-5026 is fixed in v0.55.0.
- `google.golang.org/grpc v1.79.3`: GO-2026-6348 is fixed in v1.83.1; GO-2026-6061 is fixed in v1.82.1.

- [ ] **Step 1:** Install govulncheck as a binary. Do **not** use `go run …@v1.9.0`: run that way, it scans the wrong standard library (research §3).

```bash
GOBIN=$HOME/go/bin go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
```

- [ ] **Step 2:** Record the baseline: `$HOME/go/bin/govulncheck ./...`.
  - If it fails with `Forbidden` fetching `vuln.go.dev`, use the GitHub mirror of the database instead:
    - `git clone --depth 1 https://github.com/golang/vulndb /tmp/vulndb`;
    - then run `$HOME/go/bin/govulncheck -db file:///tmp/vulndb/data/osv ./...`.
  - Expected: exit code 3, with `Your code is affected by` followed by a non-zero count.
- [ ] **Step 3:** Bump:

```bash
go mod edit -toolchain=go1.26.9
go get golang.org/x/net@v0.60.0 google.golang.org/grpc@v1.83.1
go mod tidy
```

- [ ] **Step 4:** In `Dockerfile`, change `FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine` to `golang:1.26.9-alpine`. Before committing, check the tag exists: `docker manifest inspect golang:1.26.9-alpine` (if docker is available), or `curl -s https://registry.hub.docker.com/v2/repositories/library/golang/tags/1.26.9-alpine`. Expected: a JSON body that includes `"name":"1.26.9-alpine"`. If the tag does not exist, stop and report; do not choose another tag.
- [ ] **Step 5:** Run `go version`, which must print `go1.26.9` inside the repo. Then `go build ./... && go vet ./... && go test -race ./...`: all `ok`.
- [ ] **Step 6:** Re-run govulncheck exactly as in Step 2. Expected: exit code 0. govulncheck exits 3 when called vulnerabilities remain; the baseline run produced exactly that (research §3). Notices about vulnerabilities in imported packages or required modules that the code does not call may still be printed; they are out of scope. If it exits 3, copy the listed IDs into the task report and stop.
- [ ] **Step 7:** Commit: `git add go.mod go.sum Dockerfile && git commit -m "build(deps): go1.26.9, x/net v0.60.0, grpc v1.83.1 (govulncheck: no called vulnerabilities)"`.

### Phase exit check

`go build ./... && go vet ./... && go test -race ./...` → all `ok`.
