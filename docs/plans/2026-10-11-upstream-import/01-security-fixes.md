# Phase 1: Security fixes (items 0 and 0b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first: its Global Constraints and Rules apply to every task.

**Goal:** TFTP serves only allowlisted files from `dataDir`, and `/machineconfig` serves a Talos cluster member's config only to a request whose source IP ARP-resolves to that member's MAC.

**Architecture:** One shared predicate in `pkg/cache` for "in-flight download" paths (used by HTTP `/data/` and TFTP); one pure TFTP allowlist function called from `readHandler`; the machineconfig handler gains an injected ARP lookup.

**Tech Stack:** Go 1.26, `net/http/httptest`, existing `pkg/tftp`, `pkg/http`, `pkg/cache`.

**Spec:** `docs/designs/2026-10-11-upstream-import-design.md` items 0 and 0b.

## Global Constraints

See `README.md` (this directory). In particular: no new package-level mutable state; ARP lookup is injected as a function parameter.

## Review Focus

1. A TFTP name with `..` that cleans into `cache/` (e.g. `public/../cache/x`) must be judged on the **cleaned** path: allowed only if the cleaned path is below `cache/` or `public/`. Covered by Task 2's table.
2. A request for a subtree root itself (`cache`, `cache/`, `public`) must be refused, as HTTP does. Covered by Task 2.
3. A member's request whose `?mac=` names the member but whose source IP ARP-resolves to a **different** MAC must get 403, not the config. Covered by Task 3.
4. `RemoteAddr` without a port (malformed) must give 403 for a member, not a panic. Covered by Task 3.
5. Mixed-case `.PARTIAL` / `.DOWNLOAD` suffixes must be refused over TFTP as over HTTP. Covered by Tasks 1-2.

---

### Task 1: Shared in-flight-download predicate in `pkg/cache`

**Files:**
- Create: `pkg/cache/inflight.go`
- Create: `pkg/cache/inflight_test.go`
- Modify: `pkg/http/http.go:201-204` (`isPartialPath` body)

**Interfaces:**
- Produces: `func IsInFlightPath(p string) bool` in package `cache`.

- [ ] **Step 1: Write the failing test** in `pkg/cache/inflight_test.go`:

```go
package cache

import "testing"

func TestIsInFlightPath(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"/cache/flatcar/stable/amd64/1/x.img.partial", true},
		{"/cache/tails/-/amd64/7.10/tails-amd64.iso.download", true},
		{"cache/x.PARTIAL", true},
		{"cache/x.Download", true},
		{"/cache/flatcar/stable/amd64/1/x.img", false},
		{"public/partial.txt", false},
	}
	for _, c := range cases {
		if got := IsInFlightPath(c.p); got != c.want {
			t.Errorf("IsInFlightPath(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it and confirm it fails to compile**

Run: `go test ./pkg/cache/ -run TestIsInFlightPath`
Expected: FAIL, `undefined: IsInFlightPath`.

- [ ] **Step 3: Implement** `IsInFlightPath(p string) bool` in `pkg/cache/inflight.go`: lower-case `p`, return true if it ends with `".partial"` or with `DownloadSuffix` (`pkg/cache/isodownload.go:31`, value `".download"`). Doc comment: move the explanatory comment from `pkg/http/http.go:185-200` here (it explains both suffixes and case-insensitivity).

- [ ] **Step 4: Make `isPartialPath` delegate.** In `pkg/http/http.go`, replace the body of `isPartialPath` (lines 201-204) with `return cache.IsInFlightPath(p)` and shorten its comment to one line pointing at `cache.IsInFlightPath`. `pkg/http` already imports `pkg/cache`.

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/cache/ ./pkg/http/ -run 'TestIsInFlightPath|TestPartialSuffixExcluded|TestDataFileHandler'`
Expected: `ok` for both packages.

- [ ] **Step 6: Commit**

```bash
git add pkg/cache/inflight.go pkg/cache/inflight_test.go pkg/http/http.go
git commit -m "refactor(cache): share the in-flight download predicate between HTTP and TFTP"
```

### Task 2: TFTP allowlist (item 0)

**Files:**
- Create: `pkg/tftp/allow.go`
- Create: `pkg/tftp/allow_test.go`
- Modify: `pkg/tftp/tftp.go:197-215` (the `safeJoin` fall-through branch of `readHandler`)

**Interfaces:**
- Consumes: `cache.IsInFlightPath` (Task 1), `cache.DirName` (`pkg/cache/layout.go:24`, `"cache"`).
- Produces: `func allowedDataFile(name string, topLevel []string) bool` and `func topLevelBootFiles() []string` in package `tftp`. Phase 2 replaces `topLevelBootFiles()` with a `Config` field; keep the function name until then.

- [ ] **Step 1: Write the failing test** in `pkg/tftp/allow_test.go`:

```go
package tftp

import "testing"

func TestAllowedDataFile(t *testing.T) {
	top := []string{"undionly.kpxe", "ipxe.efi", "snponly.efi", "ipxe-arm64.efi", "custom-bios.kpxe"}
	cases := []struct {
		name string
		want bool
	}{
		{"cache/flatcar/stable/amd64/1/flatcar_production_pxe.vmlinuz", true},
		{"public/scripts/join.sh", true},
		{"undionly.kpxe", true},
		{"ipxe.efi", true},
		{"snponly.efi", true},
		{"ipxe-arm64.efi", true},
		{"custom-bios.kpxe", true},
		{"api-token", false},
		{"booty.db", false},
		{"booty.db-wal", false},
		{"node-report-key", false},
		{"config/ignition.yaml", false},
		{"cache", false},
		{"cache/", false},
		{"public", false},
		{"public/../api-token", false},
		{"public/../cache/x.img", true},
		{"../api-token", false},
		{"/etc/passwd", false},
		{"cache/flatcar/x.img.partial", false},
		{"cache/tails/x.iso.DOWNLOAD", false},
		{"sub/undionly.kpxe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := allowedDataFile(c.name, top); got != c.want {
			t.Errorf("allowedDataFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTopLevelBootFilesIncludesFlagValues(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ProxyDHCPBootfileBIOS, "my-bios.kpxe")
	viper.Set(config.ProxyDHCPBootfileUEFI, "ipxe.efi")
	viper.Set(config.ProxyDHCPBootfileARM64, "")
	got := topLevelBootFiles()
	want := []string{"ipxe-arm64.efi", "ipxe.efi", "my-bios.kpxe", "snponly.efi", "undionly.kpxe"}
	if !slices.Equal(got, want) {
		t.Fatalf("topLevelBootFiles() = %v, want %v", got, want)
	}
}
```

Add imports `slices`, `github.com/jeefy/booty/pkg/config`, `github.com/spf13/viper` to the test file.

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./pkg/tftp/ -run 'TestAllowedDataFile|TestTopLevelBootFiles'`
Expected: FAIL, `undefined: allowedDataFile`.

- [ ] **Step 3: Implement in `pkg/tftp/allow.go`:**
  - `allowedDataFile(name string, topLevel []string) bool`. Steps in order: return false if `name == ""`; `cleaned := path.Clean("/" + name)` (use package `path`, not `filepath`: TFTP names are slash-separated); return false if `cache.IsInFlightPath(cleaned)`; return true if `cleaned` has prefix `"/"+cache.DirName+"/"` or `"/public/"`; return true if `slices.Contains(topLevel, strings.TrimPrefix(cleaned, "/"))` **and** `strings.Count(cleaned, "/") == 1`; else false.
  - `topLevelBootFiles() []string`: the fixed names `undionly.kpxe`, `ipxe.efi`, `snponly.efi`, `ipxe-arm64.efi` plus the non-empty values of `viper.GetString(config.ProxyDHCPBootfileBIOS)`, `...UEFI`, `...ARM64`; drop empties, sort with `slices.Sort`, deduplicate with `slices.Compact`.
  - Doc comment on `allowedDataFile`: state that it mirrors `pkg/http`'s `/data/` allowlist (`isAllowedDataPath`, `pkg/http/http.go:134-158`) and why (dataDir holds `api-token`, `booty.db`, `config/`).

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./pkg/tftp/ -run 'TestAllowedDataFile|TestTopLevelBootFiles'`
Expected: `ok`.

- [ ] **Step 5: Wire it into `readHandler`.** In `pkg/tftp/tftp.go`, at the start of the fall-through branch (immediately before `path, err := safeJoin(filename)`, line 197), insert:

```go
	if !allowedDataFile(filename, topLevelBootFiles()) {
		slog.Warn("TFTP rejected: not an allowlisted path", "client", raddr.String(), "requested", filename)
		return os.ErrNotExist
	}
```

- [ ] **Step 6: Run the package and vet**

Run: `go vet ./pkg/tftp/ && go test -race ./pkg/tftp/`
Expected: `ok`.

- [ ] **Step 7: Commit**

```bash
git add pkg/tftp/allow.go pkg/tftp/allow_test.go pkg/tftp/tftp.go
git commit -m "fix(tftp): serve only allowlisted dataDir files

TFTP opened any name under dataDir, so any LAN client could fetch
api-token, booty.db and config/. Mirror the HTTP /data/ allowlist:
cache/ and public/ subtrees plus the top-level iPXE bootfile names."
```

### Task 3: Member configs require ARP identity (item 0b)

**Files:**
- Create: `pkg/http/arp.go`
- Modify: `pkg/http/machineconfig.go:26-61` (handler head) and imports
- Modify: `pkg/http/machineconfig_cluster_test.go` (two member tests)
- Create: `pkg/http/machineconfig_member_test.go`

**Interfaces:**
- Produces (package `http`):
  - `type arpFunc func(ip net.IP) (net.HardwareAddr, error)`
  - `func arpLookup(ip net.IP) (net.HardwareAddr, error)`: calls `arping.Ping(ip)` and returns its hardware address and error. Plain function, not a variable.
  - `func requestFromMAC(r *http.Request, mac string, arp arpFunc) bool`
  - `func machineConfigHandler(store *db.Store, arp arpFunc) http.HandlerFunc`
  - `handleMachineConfigRequest(store *db.Store) http.HandlerFunc` keeps its signature and returns `machineConfigHandler(store, arpLookup)`. Phase 6 reuses `arpFunc`, `arpLookup` and `requestFromMAC`.

- [ ] **Step 1: Write the failing tests** in `pkg/http/machineconfig_member_test.go`. Use the fixture code of `TestMemberServesFrozenBytesVerbatim` (`machineconfig_cluster_test.go:12-37`) as a helper `seedMember(t *testing.T, s *db.Store, mac string) []byte` that returns the frozen bytes.

```go
func fixedARP(mac string) arpFunc {
	return func(net.IP) (net.HardwareAddr, error) { return net.ParseMAC(mac) }
}

func failingARP(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no reply") }

func TestMemberConfigRequiresMatchingARP(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:80"
	cases := []struct {
		name       string
		arp        arpFunc
		remoteAddr string
		wantCode   int
	}{
		{"arp matches", fixedARP(mac), "10.0.0.50:1234", 200},
		{"arp matches, upper-case reply", fixedARP("AA:BB:CC:DD:EE:80"), "10.0.0.50:1234", 200},
		{"arp names another host", fixedARP("aa:bb:cc:dd:ee:99"), "10.0.0.50:1234", 403},
		{"arp fails", failingARP, "10.0.0.50:1234", 403},
		{"remote addr without port", fixedARP(mac), "10.0.0.50", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			testSecretsKey(t)
			frozen := seedMember(t, s, mac)
			req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
			req.RemoteAddr = c.remoteAddr
			rr := httptest.NewRecorder()
			machineConfigHandler(s, c.arp)(rr, req)
			if rr.Code != c.wantCode {
				t.Fatalf("code = %d, want %d; body %q", rr.Code, c.wantCode, rr.Body.String())
			}
			if c.wantCode == 200 && rr.Body.String() != string(frozen) {
				t.Fatalf("body not the frozen config")
			}
			if c.wantCode == 403 && strings.Contains(rr.Body.String(), "frozen bytes") {
				t.Fatalf("403 body leaked the config")
			}
		})
	}
}

func TestNonMemberIgnoresARP(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine: {}\n")
	const mac = "aa:bb:cc:dd:ee:81"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/machineconfig?mac="+mac, nil)
	rr := httptest.NewRecorder()
	machineConfigHandler(s, failingARP)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("non-member must not need ARP: %d %q", rr.Code, rr.Body.String())
	}
}
```

The frozen bytes in `seedMember` must contain the text `frozen bytes` (copy `machineconfig_cluster_test.go:23`). The test file's imports are `errors`, `net`, `net/http`, `net/http/httptest`, `strings`, `testing`, `github.com/jeefy/booty/pkg/config`, `github.com/jeefy/booty/pkg/db`, `github.com/jeefy/booty/pkg/hardware` and `github.com/spf13/viper`.

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./pkg/http/ -run 'TestMemberConfigRequiresMatchingARP|TestNonMemberIgnoresARP'`
Expected: FAIL, `undefined: arpFunc` / `machineConfigHandler`.

- [ ] **Step 3: Implement `pkg/http/arp.go`:** `arpFunc`, `arpLookup` and `requestFromMAC(r, mac, arp)`. `requestFromMAC` steps: `net.SplitHostPort(r.RemoteAddr)` (error → false); `net.ParseIP(host)` (nil → false); `hw, err := arp(ip)` (error → false); `got, err := hardware.NormalizeMAC(hw.String())` (error → false); `want, err := hardware.NormalizeMAC(mac)` (error → false); return `got == want`.

- [ ] **Step 4: Refactor the handler.** In `pkg/http/machineconfig.go`:
  - rename the body of `handleMachineConfigRequest` into `machineConfigHandler(store *db.Store, arp arpFunc) http.HandlerFunc`;
  - in it, replace the direct `arping.Ping(net.ParseIP(ip))` call (lines 36-40) with `arp(net.ParseIP(ip))` (same error handling);
  - replace the member branch, `pkg/http/machineconfig.go` lines 52-62 **inclusive** (from `if host != nil {` through its closing `}` on line 62, which wraps the `if host.NodeConfigID != nil` block), with:

```go
		if host != nil && host.NodeConfigID != nil {
			if !requestFromMAC(r, host.MAC, arp) {
				writeError(w, http.StatusForbidden, "cluster member config requires the request to come from the member", nil)
				return
			}
			serveFrozenNodeConfig(w, store, host)
			return
		}
```

  - `handleMachineConfigRequest(store)` becomes a one-line wrapper returning `machineConfigHandler(store, arpLookup)`;
  - remove the now-unused `arping` import from `machineconfig.go` (it moves to `arp.go`).
  - Comment above the member branch: one paragraph stating it is the interim fix from design item 0b, that L2 spoofing is still possible, and that relayed members are refused until the authenticated-fetch design.

- [ ] **Step 5: Update the two existing member tests.** In `pkg/http/machineconfig_cluster_test.go`, `TestMemberServesFrozenBytesVerbatim` and `TestMemberWithUndecryptableConfigFailsLoud` call `handleMachineConfigRequest(s)(rr, req)`. Change both to `machineConfigHandler(s, fixedARP(mac))(rr, req)` and set `req.RemoteAddr = "10.0.0.50:1234"`. `TestNonMemberFallsThroughByteIdentical` is unchanged.

- [ ] **Step 6: Run the package**

Run: `go vet ./pkg/http/ && go test -race ./pkg/http/`
Expected: `ok` (about 45-60 s).

- [ ] **Step 7: Commit**

```bash
git add pkg/http/arp.go pkg/http/machineconfig.go pkg/http/machineconfig_cluster_test.go pkg/http/machineconfig_member_test.go
git commit -m "fix(http): serve a cluster member's config only to the member's own address

/machineconfig picked the host from ?mac= and served a member's
decrypted config, CA private keys included, to any caller. Require the
request's source IP to ARP-resolve to the member's MAC. Interim fix:
L2 spoofing remains possible until authenticated config fetch."
```

### Task 4: Release notes and docs for phase 1

**Files:**
- Create: `docs/releases/UNRELEASED.md` (the fork has no release notes yet; this file collects notes for the next release)
- Modify: `docs/schema/API.md:57` (TFTP "any other path" row)
- Modify: `docs/schema/STORAGE.md:26-29` (add the TFTP reachability rule next to the HTTP one)

- [ ] **Step 1:** Create `docs/releases/UNRELEASED.md` with a `## Security` section containing two bullets, verbatim:
  - `TFTP now serves only cache/, public/ and the iPXE bootfile names from --dataDir. Previously any file there, including api-token and booty.db, was readable by any host that could reach UDP/69. If untrusted hosts could reach it, rotate the API token: booty token rotate --yes. Move any other top-level files you served over TFTP under public/.`
  - `A Talos cluster member's machine config is now served only when the request's source IP ARP-resolves to that member's MAC. Previously anyone who knew a member's MAC could fetch it, including the cluster CA private keys. Members booted through a DHCP relay cannot be ARP-resolved and are refused until authenticated config fetch is designed.`
- [ ] **Step 2:** In `docs/schema/API.md` line 57, replace the "*(any other path)*" description with: `Served only if below cache/ or public/, or one of the iPXE bootfile names (undionly.kpxe, ipxe.efi, snponly.efi, ipxe-arm64.efi and the --proxyDHCPBootfile* values); everything else is "file not found".`
- [ ] **Step 3:** In `docs/schema/STORAGE.md` after line 29, add one sentence: `TFTP applies the same allowlist (plus the top-level iPXE bootfile names).`
- [ ] **Step 4:** Commit: `git add docs && git commit -m "docs: release notes and reachability docs for the TFTP and member-config fixes"`.

### Phase exit check

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: every package `ok`, no `FAIL`.
