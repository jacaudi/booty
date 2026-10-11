# Phase 9: Node health and running-version reports (item 10) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** With `--nodeReports` on:
- Flatcar, CoreOS and debianconfig-installed Debian hosts post a bounded health report at boot, including the OS version they run.
- booty authenticates each report with a per-host HMAC token, stores the latest one, and shows it on the Hosts page.

**Architecture:**
- New stdlib-only package `pkg/nodereport` (report model, token, key file, script/unit text, Ignition merge, Debian late_command, rate limiters).
- `pkg/db` gains a migration and four store methods.
- `pkg/http` gains the receiver, the injection on the two config-serving paths, and three API operations.
- The web UI gains columns and a drawer.

**Tech Stack:** Go stdlib (`crypto/hmac`, `crypto/sha256`, `container/list`), `github.com/vincent-petithory/dataurl` (the data-URL library Ignition itself decodes with, `ignition/v2@v2.27.0/internal/resource/url.go:411`), `github.com/coreos/ignition/v2/config` (validation), `github.com/coreos/go-systemd/v22/unit` (unit test only).

**Spec:** design item 10; decisions §6.1, §6.2, §6.6, §6.11/§7.8; findings §7.4, §7.7; §8.4.

## Global Constraints

See `README.md`. Everything is off unless `--nodeReports` is set. With it off, served configs are byte-identical to today, and `/node/report` returns 404. Talos and tool OSes are out of scope.

## Review Focus

1. A report whose `?mac=` names a real host but whose token is for another host must get the same 401 as an unknown MAC. Task 6 table.
2. A flood of requests with random MACs from one IP must hit the per-IP limit (429) before any DB lookup; the limiter map must not exceed its cap. Task 4 + Task 6 tests.
3. An operator Butane config that already writes `/opt/booty/health-report` or defines `booty-health.service` must keep its own version. Task 3 test.
4. With `--nodeReports` off, `/ignition.json` and `/preseed` output must be byte-identical to before. Task 7 test.
5. After `report-token/rotate`, a report with the old token is refused and one with the new token is accepted. Task 8 test.

---

### Task 1: Report model (`pkg/nodereport/health.go`)

**Files:** Create `pkg/nodereport/health.go`, `pkg/nodereport/health_test.go`.

**Interfaces:**
- Produces: `type Health`, `type DMI`, `FirmwareUEFI`, `FirmwareBIOS`, the caps constants, `ErrInvalidHealth`, `func (h *Health) Normalize() error`, and:

```go
// Summary is what GET /api/v1/hosts shows per host.
type Summary struct {
	Running         string `json:"running"`
	Kernel          string `json:"kernel"`
	Firmware        string `json:"firmware"`
	ReceivedAt      string `json:"receivedAt"`
	FailedUnitCount int    `json:"failedUnitCount"`
	Vendor          string `json:"vendor"`
	Product         string `json:"product"`
}

func (h Health) Summary() Summary
```

- [ ] **Step 1:** Write `pkg/nodereport/health.go` from `git show upstream/main:pkg/hardware/health.go`. Keep lines 15-135; drop lines 137-154 (`targetVersionPattern`, `ValidateTargetVersion`) and the `regexp` import. Change the package to `nodereport`. Rewrite the doc comments at 10-14 and 29-34 so they describe booty's report (no autopilot). Add `// Ported from jeefy/booty pkg/hardware/health.go @ 27fafe8 (MIT).` Then add `Summary` and its method.
- [ ] **Step 2: Write the tests** in `health_test.go`. Upstream has no unit tests for this, so all of these are new:
  - `TestNormalizeCaps`: 250 failed units → 200; 60 journal lines of 400 `x` → at most 50 lines, each ≤ 300 bytes, total ≤ 8192; a 300-byte `Running` → ≤ 256 bytes and ends with `…`.
  - `TestNormalizeFirmware`: `"UEFI"` → `"uefi"`; `""` → `""`; `"coreboot"` → `errors.Is(err, ErrInvalidHealth)`.
  - `TestNormalizeDropsEmptyUnits`: `{"", " ", "a.service"}` → `{"a.service"}`.
  - `TestSummary`: `FailedUnitCount == len(FailedUnits)`; `Vendor`/`Product` come from `DMI`.
- [ ] **Step 3:** Run `go test ./pkg/nodereport/`. Expected: `ok`.

### Task 2: Token and key file (`pkg/nodereport/token.go`)

**Interfaces:**
- Produces:
  - `func Token(key []byte, mac string, gen int64) string`: lowercase hex of HMAC-SHA256 over `"booty-node-report\x00" + mac + "\x00" + strconv.FormatInt(gen, 10)`.
  - `func ValidToken(key []byte, mac string, gen int64, presented string) bool`: `hmac.Equal` on the hex strings.
  - `func LoadOrCreateKey(path string) ([]byte, error)`:
    - reads 32 bytes if the file exists, erroring if it is not exactly 32 bytes;
    - otherwise generates 32 bytes from `crypto/rand` and writes them atomically (temp file in the same dir, mode 0600, rename).

- [ ] **Step 1: Tests** (`token_test.go`):
  - `TestTokenDeterministicAndDistinct`:
    - same inputs → same token;
    - a different MAC, gen or key → a different token;
    - the length is 64 hex characters.
  - `TestValidToken`: the correct token → true; a wrong token → false; an empty token → false.
  - `TestLoadOrCreateKey`, in `t.TempDir()`:
    - the first call creates the file with `0600` (check `Stat().Mode().Perm() == 0o600`);
    - a second call returns the same bytes;
    - a 31-byte file → error.
- [ ] **Step 2:** Implement. Run `go test ./pkg/nodereport/`. Expected: `ok`.

### Task 3: Script, unit, Ignition merge, Debian late_command

**Files:** Create `pkg/nodereport/script.go`, `ignition.go`, `debian.go` and their tests.

**Interfaces:**
- Produces:
  - `const ScriptPath = "/opt/booty/health-report"`, `const UnitName = "booty-health.service"`.
  - `func Script(server, mac, token string) string`: upstream `HealthReportScript` (`git show upstream/main:pkg/ignition/health.go`, lines 36-102) with exactly these changes:
    - `MAC` is fixed: replace the `if [ -n "${BOOTY_MAC:-}" ]…fi` block with `MAC=${BOOTY_MAC:-` + mac + `}`; the env override is kept for the test harness;
    - the curl line posts to `"http://` + server + `/node/report?mac=$MAC"` and adds the header `-H 'X-Booty-Node-Token: ` + token + `'`;
    - nothing else changes.
  - `func Unit(scriptPath string) string`: upstream `HealthUnit` (lines 8-27), with `After=multi-user.target network-online.target` (the `BootedUnitName` reference removed).
  - `func MergeIgnition(cfg []byte, script, unit string) ([]byte, error)`.
  - `func DebianLateCommand(script, unit string) string`.

- [ ] **Step 1: Port the script tests.** Copy `git show upstream/main:pkg/ignition/health_test.go` into `pkg/nodereport/script_test.go`, then:
  - change the package to `nodereport`;
  - replace `HealthReportScript(srv.URL[len("http://"):])` calls with `Script(srv.URL[len("http://"):], "aa:bb:cc:dd:ee:01", "tok")`;
  - change the server's expected path `/health` to `/node/report`;
  - add an assertion that the request header `X-Booty-Node-Token` equals `tok`;
  - in `TestHealthUnitParses`, change the expected `Unit/After` value to `multi-user.target network-online.target`.

  The tests skip unless `bash`, `curl` and `uname` are on `PATH` (upstream lines 57-61). Run `which bash curl uname` and note the result in the task report. If any is missing, these tests **skip**, and the task report must say so.
- [ ] **Step 2: `MergeIgnition` tests** (`ignition_test.go`). The base config is the 3.4.0 JSON `{"ignition":{"version":"3.4.0"}}`.
  - Merged output, parsed with `github.com/coreos/ignition/v2/config.Parse`:
    - reports no error and `!report.IsFatal()`;
    - contains a file at `ScriptPath` with mode `493` (0755), whose `contents.source` decodes via `dataurl.DecodeString` to the script bytes;
    - contains a unit `UnitName` with `enabled: true` and the unit text.
  - A base config that already has a file at `ScriptPath` and a unit named `UnitName` keeps its own entries unchanged, with none added.
  - A base config `{"ignition":{"version":"3.3.0"},"storage":{"files":[{"path":"/x","contents":{"source":"data:,x"}}]}}` keeps `/x` and adds the new file.
  - Invalid JSON input → error.
- [ ] **Step 3: Implement `MergeIgnition`.**
  - `json.Unmarshal` into `map[string]any`.
  - Ensure the `storage.files` and `systemd.units` arrays exist.
  - Skip the file if any existing entry's `path == ScriptPath`. Otherwise append `{"path": ScriptPath, "mode": 493, "overwrite": true, "contents": {"source": dataurl.EncodeBytes([]byte(script))}}`.
  - Skip the unit if any existing entry's `name == UnitName`. Otherwise append `{"name": UnitName, "enabled": true, "contents": unit}`.
  - `json.Marshal`.
  - Validate with `config.Parse(out)`; if it errors or `report.IsFatal()`, return an error that includes `report.String()`.
- [ ] **Step 4: `DebianLateCommand` test:**
  - the result starts with `in-target sh -c '`;
  - it contains `base64 -d > /opt/booty/health-report`, `chmod 0755 /opt/booty/health-report`, `base64 -d > /etc/systemd/system/booty-health.service` and `systemctl enable booty-health.service`;
  - extract the two base64 payloads with a regexp and decode them back to the inputs;
  - it contains no single quote other than the two delimiting ones.
- [ ] **Step 5: Implement `DebianLateCommand`.** Exactly:

```go
	return "in-target sh -c 'mkdir -p /opt/booty && echo " + base64.StdEncoding.EncodeToString([]byte(script)) +
		" | base64 -d > " + ScriptPath + " && chmod 0755 " + ScriptPath + " && echo " +
		base64.StdEncoding.EncodeToString([]byte(unit)) + " | base64 -d > /etc/systemd/system/" + UnitName +
		" && systemctl enable " + UnitName + "'"
```

  Base64 contains no single quote, so the outer single quotes are safe.
- [ ] **Step 6:** Run `go get github.com/vincent-petithory/dataurl@v1.0.0 github.com/coreos/go-systemd/v22@v22.5.0 && go mod tidy`. These are already in the module graph (`go.mod:47` and as indirect); this promotes them to direct. Then `go test -race ./pkg/nodereport/`. Expected: `ok`.

### Task 4: Bounded limiters (`pkg/nodereport/limit.go`)

**Interfaces:**
- Produces: `type Limiter struct{...}`, `func NewLimiter(max int, window time.Duration, capacity int, now func() time.Time) *Limiter`, `func (l *Limiter) Allow(key string) bool`, `func (l *Limiter) Len() int`.
- Semantics: a fixed window per key. `Allow` returns false once `max` events fall in the current window. Keys are kept in LRU order (`container/list` + map); when `Len()` would exceed `capacity`, the least recently used key is evicted. Safe for concurrent use (one `sync.Mutex`).

- [ ] **Step 1: Tests** (`limit_test.go`), with a fake clock:
  - `max=2`: allow, allow, deny; advance past the window; allow.
  - `capacity=3`, keys a, b, c, d → `Len() == 3`, and `a` was evicted, so a fresh `a` gets a full budget.
  - `TestLimiterConcurrent`: 50 goroutines × 100 `Allow` calls under `-race`.
- [ ] **Step 2:** Implement. Run `go test -race ./pkg/nodereport/`. Expected: `ok`.
- [ ] **Step 3:** Commit Tasks 1-4: `git add pkg/nodereport go.mod go.sum && git commit -m "feat(nodereport): report model, HMAC token, key file, script/unit, Ignition merge, Debian late_command, limiters"`.

### Task 5: Storage

**Files:**
- Create: `pkg/db/migrations/0010_host_health.sql`, `pkg/db/health.go`, `pkg/db/health_test.go`
- Modify: `pkg/db/migrate_test.go` (lines 32-33, 56-57, 64, 69-70, 301-302, 471-472: `9` → `10`)

**Interfaces:**
- Produces (package `db`):
  - `func (s *Store) UpsertHostHealth(mac, receivedAt string, report []byte) error`
  - `func (s *Store) GetHostHealth(mac string) (receivedAt string, report []byte, err error)`, which returns `sql.ErrNoRows` on a miss
  - `func (s *Store) ListHostHealth() (map[string][]byte, error)`, keyed by MAC
  - `func (s *Store) HostReportTokenGen(mac string) (int64, error)`, which returns `sql.ErrNoRows` for an absent host
  - `func (s *Store) BumpHostReportTokenGen(mac string) (int64, error)`, which returns the new value

- [ ] **Step 1:** `0010_host_health.sql`:

```sql
CREATE TABLE host_health (
    mac         TEXT PRIMARY KEY REFERENCES hosts(mac) ON DELETE CASCADE,
    received_at TEXT NOT NULL,
    report      TEXT NOT NULL
);
ALTER TABLE hosts ADD COLUMN report_token_gen INTEGER NOT NULL DEFAULT 0;
```

- [ ] **Step 2: Tests** (`health_test.go`, using `newTestStore(t)` from `pkg/db/targets_test.go:8-16`):
  - an upsert then get round-trips;
  - a second upsert replaces the first;
  - deleting the host removes its health row (`ON DELETE CASCADE`);
  - `HostReportTokenGen` is 0 for a new host;
  - `Bump` returns 1, then 2;
  - an absent host → `sql.ErrNoRows`;
  - `ListHostHealth` returns both hosts.

  Also add `TestMigrate0010_HostHealth`, following `TestMigrate0008_DebianColumns` (`migrate_test.go:465-494`): `user_version == 10` and `report_token_gen` defaults to 0. Seed hosts with the existing `UpsertHost`.
- [ ] **Step 3:** Implement. Update the five `user_version` assertions and the comment at line 64 from 9 to 10.
- [ ] **Step 4:** Run `go test -race ./pkg/db/`. Expected: `ok`. Commit: `git add pkg/db && git commit -m "feat(db): host_health table and per-host report token generation"`.

### Task 6: Receiver `POST /node/report`

**Files:**
- Create: `pkg/http/nodereport.go`, `pkg/http/nodereport_test.go`
- Modify: `pkg/http/api.go` (`APIDeps`), `pkg/http/http.go` (`baseMux`)

**Interfaces:**
- Produces:
  - `type NodeReports struct { Key []byte }` in package `http`, and the field `NodeReports *NodeReports` on `APIDeps`. A nil value means the feature is off.
  - `type reportReceiver struct { store *db.Store; key []byte; now func() time.Time; perIP, perMAC *nodereport.Limiter; gens *genCache }`, with `func newReportReceiver(store *db.Store, key []byte, now func() time.Time) *reportReceiver` and `func (rr *reportReceiver) ServeHTTP(w, r)`.
  - `genCache`: a mutex-guarded `map[string]genEntry{gen int64; exp time.Time}`, TTL 60 s, capacity 4096 (when full, evict an arbitrary entry), plus `func (c *genCache) Invalidate(mac string)`. A miss calls `store.HostReportTokenGen` (host absent → not cached).
  - Limiter settings: per IP `NewLimiter(10, time.Minute, 4096, now)`; per MAC `NewLimiter(1, 10*time.Second, 4096, now)`.

- [ ] **Step 1: Tests**, as a table driving `rr.ServeHTTP` with `httptest`:
  - Setup: `servingStore(t)`, host `aa:bb:cc:dd:ee:c0` (gen 0), key `bytes.Repeat([]byte{1}, 32)`, a fixed clock, and the valid body `{"running":"4152.2.0","kernel":"6.6","firmware":"uefi"}`.
  - Status cases:

    | case | request | expected |
    |---|---|---|
    | feature off (handler built with nil `NodeReports`; see Step 3) | any | 404 |
    | malformed MAC | `?mac=zz` | 400 |
    | missing token header | valid MAC | 401 |
    | unknown MAC | valid token for that MAC under gen 0 | 401 |
    | token for another host | host c0, token computed for c1 | 401 |
    | valid | correct token | 204, then `store.GetHostHealth` returns the normalized report |
    | second valid report within 10 s | same | 429 |
    | body > 64 KiB | correct token | 413 |
    | bad firmware | `{"firmware":"coreboot"}` | 400 |
    | 11th request from the same `RemoteAddr` IP within a minute, each with a new random MAC | — | 429 on the 11th |

  - Also assert that every 401 body is identical.
  - Also assert that `rr.perIP.Len() <= 4096` after 5000 requests from distinct IPs.
- [ ] **Step 2: Implement `ServeHTTP`**, in this exact order:
  1. method not POST → 405;
  2. `hardware.NormalizeMAC(r.URL.Query().Get("mac"))` fails → 400;
  3. `X-Booty-Node-Token` header empty → 401;
  4. per-IP `Allow(host part of r.RemoteAddr)` false → 429;
  5. gen lookup via `genCache` (absent host → 401);
  6. `!nodereport.ValidToken(...)` → 401;
  7. per-MAC `Allow(mac)` false → 429;
  8. `http.MaxBytesReader(w, r.Body, 64<<10)`, then JSON decode into `nodereport.Health` (a `*http.MaxBytesError` → 413; any other decode error → 400);
  9. `Normalize()` error → 400;
  10. set `h.ReceivedAt = now().UTC().Format(time.RFC3339)`; `json.Marshal(h)`; `store.UpsertHostHealth(mac, h.ReceivedAt, b)` (error → 401: the host was deleted mid-request);
  11. 204.

  Every 401 uses the same `writeError(w, http.StatusUnauthorized, "unauthorized", nil)`.
- [ ] **Step 3: Wire it.** In `baseMux`, add:

```go
	myHandler.Handle("POST /node/report", nodeReportHandler(deps))
```

  `nodeReportHandler(deps)` returns `http.NotFoundHandler()` when `deps.NodeReports == nil`, else `newReportReceiver(deps.Store, deps.NodeReports.Key, time.Now)`. Keep the receiver on `APIDeps`-level state so Task 8's rotate can call `Invalidate`: store the `*reportReceiver` in a new unexported field of `NodeReports` (`receiver *reportReceiver`), set by `nodeReportHandler`.
- [ ] **Step 4:** Run `go test -race ./pkg/http/ -run NodeReport`. Expected: `ok`. Commit: `git add pkg/http && git commit -m "feat(http): POST /node/report with per-host HMAC tokens and bounded pre-auth limits"`.

### Task 7: Injection on the serve paths

**Files:**
- Modify: `pkg/http/ignition.go` (both paths), `pkg/http/preseed.go` (DB-bound path), `pkg/http/render.go`, `pkg/http/debiangen.go`
- Test: `pkg/http/nodereport_inject_test.go`

**Interfaces:**
- Produces:
  - `func executeTemplate(source []byte, vars TemplateVars) ([]byte, error)`: extracted from `renderConfig` lines 85-93. `renderConfig` calls it.
  - `type preseedOptions struct { ExtraLateCommand string; ExtraPackages []string }`.
  - `func translateDebianConfigWith(rendered []byte, opts preseedOptions) ([]byte, error)`. `translateDebianConfig(rendered)` becomes `translateDebianConfigWith(rendered, preseedOptions{})`.
  - `buildPreseedView(spec debianConfigSpec, opts preseedOptions) (preseedView, error)`: the new parameter is used only here.
  - `func (n *NodeReports) script(store *db.Store, host *hardware.Host) (script, unit string, ok bool)`: returns `ok=false` when `n == nil`, the host is nil, or the gen lookup fails. Otherwise it builds `nodereport.Script(tftp.URLHost(), host.MAC, nodereport.Token(n.Key, host.MAC, gen))` and `nodereport.Unit(nodereport.ScriptPath)`.

- [ ] **Step 1: Tests:**
  - `TestIgnitionUnchangedWhenReportsOff`: for a known host with `ignitionHandler(s, nil)`, the body contains no `booty-health`. Phase 7's `TestIgnitionOutputsStable` must also still pass **without** `-update-ignition`; that is the byte-identity guard.
  - `TestIgnitionGetsHealthWhenOn`, for both the DB-bound and the file path: the body parses with `config.Parse` and contains `booty-health.service` and `/opt/booty/health-report`.
  - `TestDebianconfigGetsHealthWhenOn`:
    - the DB-bound debianconfig host's preseed contains `systemctl enable booty-health.service`;
    - the health fragment appears **after** any ESP-sync fragment and **before** the operator's `late_command` text;
    - `d-i pkgsel/include` contains `curl`.
  - `TestDebianPreviewAndConverterUnchanged`: `renderConfig("debianconfig", …)` and `ConvertPreseedToDebianConfig` output contain no `booty-health`.
  - `TestRawPreseedNeverInjected`: the rung-4 file path output has no `booty-health`, even with the feature on.
- [ ] **Step 2: Implement.**
  - **Ignition.**
    - Add `ignitionHandler(store *db.Store, n *NodeReports) http.HandlerFunc`, which holds the existing body; `handleIgnitionRequest(store)` returns `ignitionHandler(store, nil)`.
    - Just before each `w.Write` of the DB-bound output and of the translated file-path output (not the reboot config): if `s, u, ok := n.script(store, host); ok`, then `merged, err := nodereport.MergeIgnition(out, s, u)`. On error, `slog.Warn("node report injection skipped", "mac", host.MAC, "err", err)` and serve the original; on success serve `merged`.
  - **Preseed.**
    - Add `preseedHandler(store, n)`; `handlePreseedRequest(store)` returns `preseedHandler(store, nil)`.
    - In the DB-bound branch, when `kind == "debianconfig"` and `n.script` returns ok:
      - `rendered, err := executeTemplate(src, vars)`;
      - `out, err := translateDebianConfigWith(rendered, preseedOptions{ExtraLateCommand: nodereport.DebianLateCommand(s, u), ExtraPackages: []string{"curl"}})`;
      - `ct = "text/plain"`.
    - Otherwise keep `renderConfig`.
  - **`buildPreseedView`.**
    - Append `opts.ExtraLateCommand` to `lateParts` immediately **before** the operator part (`debiangen.go:522-524`).
    - Apply `appendIfAbsent(pkgs, p, true)` for each entry of `opts.ExtraPackages`, after the existing `openssh-server`/`sudo` lines (529-531).
    - Update the comment at 509-511 to list the order: ssh → sudo → ESP sync → booty health → operator.
  - **`baseMux`:** use `ignitionHandler(deps.Store, deps.NodeReports)` and `preseedHandler(deps.Store, deps.NodeReports)` for `/ignition.json` and `/preseed`.
- [ ] **Step 3:** Run `go test -race ./pkg/http/`. Expected: `ok`. Commit: `git add pkg/http && git commit -m "feat(http): inject the node report unit into Ignition and debianconfig when --nodeReports is on"`.

### Task 8: API operations, flag, key wiring

**Files:**
- Modify: `pkg/http/api_hosts.go` (list-hosts output, two new operations)
- Modify: `pkg/config/config.go`
- Modify: `cmd/main.go`
- Test: `pkg/http/api_hosts_test.go`

**Interfaces:**
- `list-hosts` items become `hostView struct { *hardware.Host; Health *nodereport.Summary `json:"health,omitempty"` }`. Change `listHostsOutput.Body.Hosts` from `[]*hardware.Host` to `[]hostView` and fill `Health` from `store.ListHostHealth()`. A report that fails to unmarshal is logged and treated as absent.
- `GET /api/v1/hosts/{mac}/health` (`get-host-health`):
  - 422 for a bad MAC;
  - 404 for an unknown host;
  - 404 with the message `no report yet` when the host exists but has no report;
  - 200 with body `nodereport.Health`.
- `POST /api/v1/hosts/{mac}/report-token/rotate` (`rotate-host-report-token`):
  - 404 for an unknown host;
  - 409 with the message `node reports are off` when `deps.NodeReports == nil`;
  - otherwise `store.BumpHostReportTokenGen`, then `deps.NodeReports.receiver.Invalidate(mac)` when the receiver is non-nil;
  - 200 `{"generation": <n>}`.

- [ ] **Step 1: Tests.**
  - list-hosts includes `health.running` for a host with a stored report, and omits `health` for one without.
  - get-host-health: the 422, 404 and 200 cases above.
  - rotate:
    - 409 when the feature is off;
    - 200 and generation 1 when it is on;
    - after rotation, a report with the old token → 401 and with the new token → 204. Build `deps` with `NodeReports: &NodeReports{Key: key}`, then call `h := nodeReportHandler(deps)`, which also sets `deps.NodeReports.receiver`, **before** `newTestAPI(t, deps)`; send the reports through `h`.
  - `TestEveryRegisteredOperationIsGated` (`auth_test.go:178-226`) must still pass. Both new paths use `{mac}`, which it already substitutes.
- [ ] **Step 2: Implement.** Add `NodeReports = "nodeReports"` to `pkg/config`, with `viper.SetDefault(NodeReports, false)`. In `cmd/main.go`:
  - add the flag `flags.BoolVar(&args.nodeReports, "nodeReports", false, "Inject a boot-time health/running-version report into Flatcar, CoreOS and debianconfig Debian hosts and accept reports at POST /node/report")`;
  - in `run`, before `StartHTTP`:

```go
	var nodeReports *bootyHTTP.NodeReports
	if viper.GetBool(config.NodeReports) {
		key, err := nodereport.LoadOrCreateKey(filepath.Join(viper.GetString(config.DataDir), "node-report-key"))
		if err != nil {
			return fmt.Errorf("node reports: %w", err)
		}
		nodeReports = &bootyHTTP.NodeReports{Key: key}
	}
```

  Pass `NodeReports: nodeReports` in `APIDeps`. The phase 1 TFTP allowlist already refuses `node-report-key`.
- [ ] **Step 3:** Run `go build ./... && go vet ./... && go test -race ./...`. Expected: `ok`. Commit: `git add pkg cmd && git commit -m "feat: --nodeReports flag, host health API and per-host token rotation"`.

### Task 9: UI

**Files:** Modify `web/src/api/types.ts` (`Host`, lines 3-19), `web/src/api/client.ts`, `web/src/views/HostsView.tsx`; Test `web/src/views/HostsView.test.tsx`, `web/src/api/client.test.ts`.

- [ ] **Step 1: Types and client.**
  - In `types.ts`, add to `Host`: `health?: { running: string; kernel: string; firmware: string; receivedAt: string; failedUnitCount: number; vendor: string; product: string }`.
  - In `client.ts`, add `getHostHealth(mac)` → `GET /hosts/${mac}/health`, returning a `HostHealth` type (`running`, `kernel`, `firmware`, `receivedAt`, `failedUnits: string[]`, `journalErrors: string[]`, `dmi: { vendor; product; biosVersion }`), and `rotateReportToken(mac)` → `POST /hosts/${mac}/report-token/rotate`.
- [ ] **Step 2: Tests**, with mocked modules as in the existing file:
  - an approved host with `health.running = '4152.2.0'` and `failedUnitCount = 2` shows the text `4152.2.0` and a badge with count `2`;
  - a host without `health` shows `—` in the Running column;
  - clicking the Running cell calls `getHostHealth('mac')` and opens a drawer titled `Health: mac` that lists the failed units returned.
- [ ] **Step 3: Implement.** In `baseCols`, after `Booted`, add:
  - **Running**: `h.health?.running ?? '—'`, wrapped in a link-style `Button type="link"` that opens the drawer when `health` exists, with an antd `Badge count={h.health.failedUnitCount}` next to it when the count is > 0;
  - **Last report**: `h.health?.receivedAt ?? '—'`.

  Add a `Drawer` with state `healthFor: string | null` and data loaded by `getHostHealth`, showing the kernel, firmware, vendor/product, a failed-units list and the journal lines in a `<pre>`.
- [ ] **Step 4:** Run `cd web && npx tsc -b && npm test`. Expected: pass. Commit: `git add web/src && git commit -m "feat(web): Running and Last report columns and a health drawer on the Hosts page"`.

### Task 10: Docs

- [ ] **Step 1:** `docs/CONFIGURATION.md`: add a `--nodeReports` row (default `false`) and a short "Node reports" section covering:
  - what is injected;
  - which OSes (Flatcar, CoreOS, and Debian **debianconfig** only; raw preseed files and Talos are never touched);
  - where the key lives (`<dataDir>/node-report-key`, 0600);
  - the limits: per IP 10/min, per host 1 per 10 s, 64 KiB;
  - that a `d-i preseed/late_command` written in `raw_preseed:` replaces the composed one, health install included;
  - that the token sits inside each host's config, so anyone who can fetch that config can forge that host's reports;
  - that rotation takes effect on the next config fetch, which for an installed Debian host means reinstalling.
- [ ] **Step 2:** `docs/schema/API.md`: `POST /node/report`, `GET /api/v1/hosts/{mac}/health`, `POST /api/v1/hosts/{mac}/report-token/rotate`, and the `health` field on list-hosts. `docs/schema/DATABASE.md`: `host_health` and `hosts.report_token_gen`. `docs/schema/STORAGE.md`: `node-report-key`.
- [ ] **Step 3:** `docs/releases/UNRELEASED.md` under `## Features`: `Opt-in node health reports (--nodeReports): Flatcar, CoreOS and debianconfig Debian hosts report their running OS version, failed units and boot errors; shown on the Hosts page.`
- [ ] **Step 4:** Commit: `git add docs && git commit -m "docs: node health reports"`.

### Phase exit check

`go build ./... && go vet ./... && go test -race ./...` and `cd web && npx tsc -b && npm test` → all pass.
