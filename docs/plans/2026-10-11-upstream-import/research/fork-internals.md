# booty fork internals — touch points for the upstream-import plan

Repo: /home/user/booty @ f36482c. All paths relative to repo root. Every fact below was read in the
file at the cited line. "NOTE" marks a finding the plan must account for (often a mismatch with the
design doc's assumptions).

---

## 1. cmd/: flags, config, startup, shutdown, tests

### 1.1 Flag registration pattern
- Flags are plain pflag `*Var` calls into a package-level `args` struct (cmd/main.go:33-71), inside
  `func init()` (cmd/main.go:78-284). **There is no per-flag `viper.BindPFlag`.** One bulk call binds
  every flag: `viper.BindPFlags(flags)` (cmd/main.go:274). Viper key == flag name.
- Code then reads values ONLY through viper with constants from pkg/config (e.g.
  `viper.GetBool(config.ProxyDHCPEnabled)`, cmd/main.go:517). `args.*` fields are never read.
- Examples (verbatim):
  - string (cmd/main.go:180-185):
    ```go
    flags.StringVar(
        &args.serverIP,
        "serverIP",
        "127.0.0.1",
        "IP address that clients can connect to",
    )
    ```
  - int (cmd/main.go:186-191): `flags.IntVar(&args.serverHttpPort, "serverHttpPort", 0, "Client-facing HTTP port ...")`
  - bool (cmd/main.go:200-205):
    ```go
    flags.BoolVar(
        &args.proxyDHCPEnabled,
        "proxyDHCPEnabled",
        false,
        "Enable the proxyDHCP responder (PXEClient-only, assigns no leases; UDP/67 needs CAP_NET_BIND_SERVICE)",
    )
    ```
  - duration/int64 exist too: cmd/main.go:93-110.
- To add a flag (e.g. `--proxyDHCPRelay`, `--tftpPort`, `--tftpBlockSize`, `--nodeReports`):
  1. field in `args` struct (cmd/main.go:33-71);
  2. `flags.XxxVar(...)` in `init()` before line 270 (`Cmd.AddCommand`);
  3. constant in pkg/config/config.go const block (lines 22-59), e.g. `ProxyDHCPEnabled = "proxyDHCPEnabled"` (config.go:37);
  4. optionally `viper.SetDefault` in `LoadConfig` (config.go:74-112) — this is how tests/other
     packages see a default without cobra (see config_test.go:17 `TestLoadConfig_ProxyDHCPDefaults`).
- `defaultDataDir = "/data"` lives in cmd/token.go:16 (shared by main + token subcommands).

### 1.2 Defaults in pkg/config/config.go
- `LoadConfig(cmd)` (config.go:74-112) only does `viper.SetDefault`/`BindEnv`; called from `run`
  at cmd/main.go:308. It duplicates the flag defaults for proxyDHCP (config.go:76-79), talos,
  preseed, cache, signaturePolicy etc. `ServerIP` has **no** SetDefault (only the flag default
  "127.0.0.1", main.go:183). Env bindings: IGNITION_FILE (99), HARDWARE_MAP (102),
  DATABASE_PATH (105), BOOTY_API_TOKEN (111).
- `DatabasePathValue()` → `<DataDir>/booty.db` unless DATABASE_PATH set (config.go:170-175).
- Validation helpers called from run: `ValidateSignaturePolicy` (config.go:205), `ValidateSecretsKey` (secretskey.go).
- No `DetectServerIP` exists yet (grep). Item 5 adds it here.

### 1.3 serverHttpPort resolution
cmd/main.go:301-303:
```go
func resolveServerHTTPPort(serverHTTPPort, httpPort int) int {
	return cmp.Or(serverHTTPPort, httpPort)
}
```
applied at cmd/main.go:371:
```go
viper.Set(config.ServerHttpPort, resolveServerHTTPPort(viper.GetInt(config.ServerHttpPort), viper.GetInt(config.HttpPort)))
```
This is the natural slot for serverIP auto-detection (item 5): same `viper.Set` idiom, after token
bootstrap, before `db.Open`.

### 1.4 Startup order in `run` (cmd/main.go:305-510)
1. `setupLogging` (306), log "Starting Booty!", `config.LoadConfig(cmd)` (308)
2. `ValidateSignaturePolicy` (310), `ValidateSecretsKey` (314)
3. API token store `auth.NewStore(<dataDir>/api-token)` + noAuth/explicit/Load switch (322-364)
4. serverHttpPort resolve (371)
5. `db.Open(config.DatabasePathValue())` (376), `defer store.Close()`, `hardware.SetStore(store)` (381), `tftp.SetStore(store)` (382)
6. warn if clusters exist w/o secretsKey (388-392)
7. `hardware.Load()` (394), `hardware.PreserveBoot()` (397)
8. `cache.MigrateChannelLayout(store)` (408), `cache.LoadCatalog()` (416)
9. `bootyHTTP.SeedVanillaSchematic(store)` (423)
10. `signal.NotifyContext(SIGINT,SIGTERM)` (429); `watchSIGHUP` (442)
11. `cache.NewReconciler(...)`, `reconciler.Start(ctx)` (448-454)
12. `tftpServer := tftp.StartTFTP()` (456) — returns `*tftp.Server`, no error
13. `bootyHTTP.StartHTTP(bootyHTTP.APIDeps{Store, Trigger: reconciler.Trigger, Auth: tokenStore, NoAuth})` (459-467) — returns `(*http.Server, error)`
14. `proxyDHCPServer := startProxyDHCP()` (471)
15. block on `<-ctx.Done()` (477); shutdown with 5s ctx (480)

Shutdown order (main.go:485-506) via `shutdown(slog.Default(), steps)` (cmd/shutdown.go:19-28):
proxydhcp (if non-nil) → http (`httpServer.Shutdown(shutdownCtx)`) → reconciler.Stop → tftp
(`stopWithTimeout(tftpServer.Shutdown, 5*time.Second)`, shutdown.go:38-52; exists because pin/tftp
single-port Shutdown can hang, comment main.go:496-499).

- TFTP start: `tftp.StartTFTP()` pkg/tftp/tftp.go:438-460 — resolves `absDataDir` via
  `filepath.Abs(viper DataDir)` (os.Exit(1) on error, 439-443), `tftp.NewServer(readHandler, writeHandler)` (447),
  `SetBlockSize(512)` (448), `EnableSinglePort()` (449), `SetTimeout(60s)` (450), goroutine
  `ListenAndServe(":69")` with `os.Exit(1)` on error (451-457). Port and block size are hard-coded.
- HTTP start: pkg/http/http.go:66-96 (refuses nil Auth unless NoAuth, 67-69; listen error →
  `os.Exit(1)` inside goroutine, 87-92).
- proxyDHCP start: `startProxyDHCP()` cmd/main.go:516-541 — returns nil if disabled; rejects nil /
  loopback / unspecified serverIP (520-525); builds `proxydhcp.Config{ServerIP, BootfileBIOS, BootfileUEFI, BootfileARM64}`;
  `NewServer` then `Start`; any failure logs and returns nil (best-effort).

### 1.5 cmd tests (cmd/main_test.go, 170 lines; 21 Test funcs across cmd/)
- **No test calls `run()` or `main()`.** Tests exercise extracted helpers directly:
  `startProxyDHCP()` (main_test.go:19-56), `resolveServerHTTPPort` table test (67-90),
  `handleHUP` (94, 134), `watchSIGHUP` (157). NOTE: design item 5 says "Tests that start `main`
  inject the detector" — there are none; follow the existing pattern instead: extract a pure
  `resolveServerIP(explicit string, detect func() (net.IP, error)) (string, error)` and table-test it.
- viper isolation helper (main_test.go:58-62):
  ```go
  func restoreViper(t *testing.T, key string) {
  	t.Helper()
  	prev := viper.Get(key)
  	t.Cleanup(func() { viper.Set(key, prev) })
  }
  ```
  Used as `restoreViper(t, config.ProxyDHCPEnabled); viper.Set(config.ProxyDHCPEnabled, true)` (46-47).
  Other packages use `viper.Reset(); t.Cleanup(viper.Reset)` instead (e.g. pkg/http/machineconfig_test.go:36-37, pkg/tftp/tftp_test.go:178).
- shutdown_test.go covers `shutdown`/`stopWithTimeout`.

---

## 2. pkg/tftp

Files: tftp.go (460), menu.go, pxe_config.go, tool_scripts.go + tests tftp_test.go (608),
menu_test.go (305), tool_scripts_test.go (340). 47 Test funcs.

### 2.1 Functions (tftp.go)
| func | line | role |
|---|---|---|
| `var absDataDir string` | 27 | set only in StartTFTP |
| `var errPathEscapes` | 31 | |
| `SetStore(*db.Store)` / `currentStore()` | 44 / 50 | RWMutex-guarded `tftpDB` |
| `safeJoin(requested) (string, error)` | 64-82 | rejects uninit, abs path, escape; no EvalSymlinks |
| `bootDispatch(host) (kind, osToLoad)` | 90-105 | holding/assigned/menu |
| `readHandler(filename, rf io.ReaderFrom) error` | 108-215 | see 2.2 |
| `applyTokens` | 219 | `[[token]]` replace |
| `holdingScript(urlHost)` | 231 | tokens `[[server]]`, `[[server-ip]]` |
| `bootTokensFor(os, segment, arch, version, urlHost)` | 243-277 | |
| `isToolOS` | 281 | |
| `debianBaseURL`, `debianSourceMode` | 289, 299 | |
| `hostParams` | 318 | |
| `clusterPinnedTalosVersion` | 335 | |
| `bootTokens(os, urlHost, host)` | 359-413 | |
| `assignedScript(host, urlHost)` | 419-427 | refuses tool OS → holding |
| `writeHandler` | 430-433 | logs and returns **nil** (item 3) |
| `StartTFTP() *tftp.Server` | 438-460 | |

menu.go: `itemKey` (37), `menuItemText` (44), `renderMenuSelection(filename, urlHost)` (67-87),
`menuSelectionScript(host, filename, urlHost)` (100-117, gates on bootDispatch=="menu", builds holding
inline at 101-104 — a duplicate of `holdingScript`), `renderMenu(inWindow, archived, serverIP)` (129-…).
`renderMenu` takes the **bare serverIP** and emits `chain tftp://<serverIP>/...` at menu.go:172, 184, 198, 202.
pxe_config.go: `PXEConfig` map filled in `init()`; holding.ipxe chains `tftp://[[server-ip]]/booty.ipxe` (pxe_config.go:47);
flatcar.ipxe `ignition.config.url=http://[[server]]/ignition.json` (29), coreos `CONFIGURL http://[[server]]/ignition.json` (36),
debian `preseed/url=http://[[server]]/preseed` (56), talos already `&mac=${mac}` (22), pxelinux flatcar (17).
tool_scripts.go: `chain tftp://[[server-ip]]/booty.ipxe || shell` at lines 69, 117, 149.

### 2.2 readHandler behaviour, per branch (tftp.go:108-215)
0. Always: `rf.(tftp.OutgoingTransfer).RemoteAddr()` and `rf.(tftp.RequestPacketInfo).LocalIP()` type
   assertions (110-111) — **panics with any fake that does not implement both**. Then
   `arping.Ping(raddr.IP)` (115); on success `hardware.GetMacAddress` (120). If host found,
   `DoInstall && filename=="booty.ipxe"` → flips DoInstall false via `WriteMacAddress` (125-133).
   urlHost = serverIP, plus `:port` unless port == 80 (137-141).
1. `menu/` prefix + `/boot.ipxe` suffix → `menuSelectionScript(host, filename, urlHost)` (143-153).
2. `booty.ipxe` → `bootDispatch`: assigned → `assignedScript`; menu → `renderMenu(PartitionCached...)` with **bare `viper ServerIP`** (168); default → `holdingScript(urlHost)` (155-180).
3. `pxelinux.cfg/default` → `PXEConfig[host.OS or "flatcar"]` with `[[server]]` (182-196). NOTE: `PXEConfig` keys "talos"/"coreos" (non-.ipxe) don't exist, so a non-flatcar host gets an empty string.
4. Anything else → `safeJoin` → on error `os.ErrNotExist` (+warn only on escape, 197-203) → `os.Open` → `rf.ReadFrom(file)` (204-214). **No allowlist: any file under dataDir is served** (item 0 bug confirmed).

### 2.3 How tests reach this code
- **No test calls `readHandler` or `StartTFTP`** (grep: only cmd/main.go:456 and a comment at
  pkg/proxydhcp/proxydhcp.go:23 / tftp_test.go:148). Tests call the pure helpers: `bootDispatch`,
  `bootTokens`, `bootTokensFor`, `renderMenu`, `renderMenuSelection`, `menuSelectionScript`,
  `assignedScript`, `safeJoin`. No fake `io.ReaderFrom` exists. To test item 0 / 6 / 8 the plan must
  either (a) factor the file-serving decision into a pure function (`openDataFile(name) (io.ReadCloser, error)`)
  and unit-test that, or (b) start a real pin/tftp server on an ephemeral port in the test and use
  `tftp.NewClient`. (a) matches the codebase's existing style.
- `absDataDir` is set directly in tests (tftp_test.go:25-28):
  ```go
  prev := absDataDir
  absDataDir = abs
  t.Cleanup(func() { absDataDir = prev })
  ```
- `safeJoin` is tested only in `TestSafeJoin` (tftp_test.go:19-64): cases simple, subdir, empty,
  ".", "a//b" (ok); "../etc/passwd", "a/../../etc/passwd", "/etc/passwd" (errPathEscapes).
- Store fixture: `newDebianModeStore(t)` (tftp_test.go:420-441) — `db.Open(t.TempDir()/booty.db)`,
  `SetStore(s)`, cleanup `SetStore(nil); s.Close()`.
- Typical setup: `viper.Reset(); root := t.TempDir(); viper.Set(config.DataDir, root)` (tftp_test.go:178-181).

---

## 3. pkg/http

### 3.1 baseMux (http.go:32-54)
```go
myHandler.HandleFunc("/", handleRequest)                    // 302 -> /ui/ (request.go:13-15)
myHandler.HandleFunc("/ignition.json", handleIgnitionRequest(deps.Store))
myHandler.HandleFunc("/machineconfig", handleMachineConfigRequest(deps.Store))
myHandler.HandleFunc("/preseed", handlePreseedRequest(deps.Store))
myHandler.HandleFunc("/version.txt", handleVersionRequest)
myHandler.HandleFunc("/version.json", handleVersionRequest)
myHandler.HandleFunc("/healthz", handleHealthz)
myHandler.HandleFunc("/login", handleLogin(deps.Auth))
myHandler.HandleFunc("/logout", handleLogout(deps.Auth))
myHandler.Handle("/data/", http.StripPrefix("/data/", dataFileHandler(viper.GetString(config.DataDir))))
... myHandler.Handle("/ui/", http.StripPrefix("/ui/", uiHandler(uiFS)))
```
`StartHTTP` (http.go:66-96) = `baseMux(deps)` + `RegisterAPI(myHandler, deps)` (77), handler wrapped
in `logRequest` (81). Item 6's "route /boot/ before ServeMux" wrapper belongs between `logRequest`
and `myHandler` (http.go:81), or a wrapper returned by a new func that tests can call.

Tests of base-mux registration use `baseMux(APIDeps{...})` + `httptest` directly:
- `TestRetiredRoutesAreGone` (http_test.go:405-430): catch-all answers **302** for unmounted paths;
  so a test that a new route is mounted asserts `rr.Code != http.StatusFound`.
- `TestLoginAndLogoutAreMountedOnTheBaseMux` (auth_test.go:534-556).
- `/data/` tests use a stand-in `dataMux(dataDir)` (http_test.go:19-23), `getData` (26), `getDataFollow` (43, follows ServeMux 301/307 redirects).

### 3.2 /data/ allowlist (http.go:105-158) — verbatim
```go
func dataFileHandler(dataDir string) http.Handler {
	dataFS := http.FileServer(noListingDir{http.Dir(dataDir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := path.Clean("/" + r.URL.Path)
		if !isAllowedDataPath(cleaned) || isPartialPath(cleaned) {
			http.NotFound(w, r)
			return
		}
		dataFS.ServeHTTP(w, r)
	})
}

var dataSubtrees = []string{"/" + cache.DirName, "/public"}   // cache.DirName = "cache" (pkg/cache/layout.go:24)

func isAllowedDataPath(p string) bool {
	cleaned := path.Clean("/" + p)
	for _, root := range dataSubtrees {
		if strings.HasPrefix(cleaned, root+"/") {
			return true
		}
	}
	return false
}
```
`noListingDir` refuses directories (168-183); `isPartialPath` blocks `.partial` and `cache.DownloadSuffix`
case-insensitively (201-204). Tests: `TestIsAllowedDataPathCleansItsInput` (http_test.go:90-107),
`TestDataFileHandler_ServesOnlyAllowedSubtrees` (123), `_NoDirectoryListing` (257), `_BlocksPartial` (318),
`TestPartialSuffixExcluded` (70), `TestTokenFileIsNotServedOverData` (auth_test.go:471).
NOTE for item 0: TFTP filenames have no leading "/" (and pin/tftp passes the raw name), so a TFTP
allowlist should reuse the same "clean with leading slash, then prefix match" idiom, plus an exact-name
set for top-level iPXE binaries. `isAllowedDataPath` is unexported in package http; pkg/tftp cannot
import it without exporting (pkg/tftp does not import pkg/http today and pkg/http does not import pkg/tftp — check for cycles if
item 7 makes http import tftp: tftp imports cache, config, db, hardware, ostype only, tftp.go:15-22, so http→tftp is cycle-free).

Related: `resolveWithinDataDir(name)` (pkg/http/datapath.go:29-60) — the EvalSymlinks-checked join
used for template files; `within(root,p)` (64-66).

### 3.3 Auth: what is open
- The gate is **only** on the huma `/api/v1` group: `grp.UseMiddleware(authMiddleware(api, deps.Auth, deps.NoAuth))`
  (api.go:50), installed before every registrar (api.go:51-59). `authMiddleware` (auth.go:34-50)
  passes when `disabled || store == nil || authorized(store, ctx)` — **nil store passes**, which is how
  API tests bypass auth (they build `APIDeps{Store, Trigger}` with no Auth).
- `authorized` (auth.go:52-76): `X-Booty-Token` header (constant-time `store.Verify`) or session cookie.
- Everything on the base mux is unauthenticated except `/logout` (needs valid cookie, auth.go:161-181)
  and `/login` (token). Huma adapter-level docs/openapi/schemas are open (auth_test.go:235-258).
- **Therefore new base-mux routes `/booty.ipxe`, `/menu/...`, `/boot/x`, `/node/report` are OPEN**
  (no gate) — `/node/report` must do its own token check. Anything registered via `huma.Register` on
  the group (e.g. `wake-host`, `report-token/rotate`) is gated automatically.
- `TestEveryRegisteredOperationIsGated` (auth_test.go:178-226) walks the OpenAPI doc, substitutes only
  `{id}`, `{mac}`, `{v}` (line 209) — new path params need adding there — and asserts
  `checked >= 47` (222). New ops increase the count; the floor need not change.

### 3.4 huma operation example (revoke-host, api_hosts.go:337-348)
```go
huma.Register(api, huma.Operation{
	OperationID: "revoke-host", Method: http.MethodPost, Path: "/hosts/{mac}/revoke",
	Summary: "Revoke a host", Tags: []string{"hosts"},
}, func(ctx context.Context, in *struct {
	MAC string `path:"mac"`
}) (*struct{}, error) {
	if err := hardware.Revoke(in.MAC); err != nil {
		return nil, huma.Error422UnprocessableEntity("revoke", err)
	}
	return nil, nil
})
```
Path is relative to the group prefix `/api/v1` (api.go:38). Body-returning op pattern: `(*struct{ Body *hardware.Host }, error)`
(menu-host, api_hosts.go:353-376). Existence check idiom: `hardware.HasHost(in.MAC)` → 422 on
malformed MAC, `huma.Error404NotFound("host not found")` on absent (api_hosts.go:359-364) — use HasHost,
not GetMacAddress, to avoid polluting the unknown list (comment 242-246).
Custom status: `DefaultStatus: http.StatusCreated` in the Operation (146) or a `Status int` field in
the output struct (154-157).
NOTE: there is **no `GET /api/v1/hosts/{mac}`** (operations listed api_hosts.go:91,144,233,304,339,354,380,396 + api_schematics.go:27).
`put-host` (api_hosts.go:378-386) is a stub returning `huma.Error403Forbidden(msgDestructiveNotImplemented)`.
Design item 4 ("validate in put-host") and item 10 ("GET /hosts/{mac} gains health") assume
endpoints that do not exist; the plan must either add them or drop those clauses.
Hostname enters the API only via create-host (`in.Body.Hostname`, api_hosts.go:150, merged 205-207).

### 3.5 Errors (errors.go)
- API: huma helpers `huma.Error4xx/5xx(msg, errs...)`, rendered as problem+json.
  For 400 use `huma.Error400BadRequest`. Hostname-invalid → 400 per design.
- Base-mux / boot handlers: `writeError(w, status, msg, err)` (errors.go:21-28): logs cause via slog,
  `http.Error(w, msg, status)` plaintext. `writeJSONError(w, status, msg)` (auth.go:203-207) for /login-style JSON.
- `msgDestructiveNotImplemented` (errors.go:15).

### 3.6 API test helpers
- `newTestAPI(t, deps) humatest.TestAPI` (api_test.go:11-16): `humatest.New(t)` + `registerOperations(api, deps)`.
  Calls: `api.Get/Post/Delete(path, body...)`, `resp.Code`, `resp.Body.String()`.
- `hostsTestSetup(t) APIDeps` (api_hosts_test.go:12-29): `db.Open(t.TempDir()/t.db)`, `hardware.SetStore(store)`,
  `hardware.Load()`, cleanup `store.Close(); hardware.SetStore(nil)`; returns `APIDeps{Store: store, Trigger: func(){}}` (no Auth → gate open).
- `hostsTestDeps` (118) seeds `aa:bb:cc:dd:ee:40`.
- Auth tests: `gatedHarness(t, store, noAuth) http.Handler` (auth_test.go:23-28), `tokenStore(t, tok)` (43),
  `liveCookie` (52), `getOS` (33).
- Other per-file store helpers: `ignitionTestStore` (ignition_test.go:21), `machineConfigTestStore`
  (machineconfig_test.go:19), `servingStore` (serving_test.go:18), `resolveStore` (resolve_test.go:12),
  `withDataDir` (datapath_test.go:17), `seedCache` (request_test.go:15), `translate` (debiangen_test.go:12).
- Boot handler test pattern (machineconfig_test.go:29-58): `viper.Reset(); t.Cleanup(viper.Reset)`;
  set DataDir/TalosConfigFile/ServerIP/ServerHttpPort; `req := httptest.NewRequest(GET, "/machineconfig?...&mac=...", nil)`;
  `req.RemoteAddr = "192.0.2.1:12345"`; call `handleMachineConfigRequest(store)(rec, req)` directly.

---

## 4. pkg/db

### 4.1 Migrations (pkg/db/migrate.go)
- `//go:embed migrations/*.sql` → `migrationsFS` (migrate.go:13-14).
- Ordinal = 1-based index in **sorted** `fs.ReadDir` order (72-73); applied when ordinal > `PRAGMA user_version`;
  each in its own tx, then `PRAGMA user_version = N` (86-98). Naming: `NNNN_snake_name.sql`
  (0001…0009). **No down migrations.** Whole file executed with one `tx.Exec` (86) — multiple statements OK.
- When ≥1 migration runs, FKs are turned OFF on a dedicated conn, `PRAGMA foreign_key_check` after,
  then ON (67-118). Pre-flight hook `preflightPreseedRemoval` (122-165) shows how to add a Go check gated on ordinal.
- Called from `Open` (db.go:25-43): DSN pragmas WAL, foreign_keys(ON), busy_timeout(5000); `SetMaxOpenConns(1)`.
- NOTE: adding `0010_host_health.sql` requires updating **five hard-coded `user_version` == 9 asserts**:
  pkg/db/migrate_test.go:32-33, 56-57, 69-70, 301-302, 471-472 (and the comment at 64).

### 4.2 hosts schema (all columns)
From 0001_init.sql:30-48: `mac TEXT PK`, `hostname`, `ip`, `booted`, `ignition_file`, `os` (TEXT NOT NULL DEFAULT ''),
`do_install INTEGER NOT NULL DEFAULT 0`, `schematic`, `approved INTEGER NOT NULL DEFAULT 0`,
`boot_mode TEXT NOT NULL DEFAULT 'menu' CHECK (IN ('assigned','menu'))`, `assigned_os`, `assigned_arch`,
`assigned_params TEXT NOT NULL DEFAULT '{}'`, `uuid`, `serial`, `first_seen`, `last_seen` (TEXT DEFAULT datetime('now')).
Added: `config_id INTEGER DEFAULT NULL` (0003:40); `cluster_id INTEGER REFERENCES clusters(id)`,
`machine_type TEXT`, `node_config_id INTEGER REFERENCES cluster_node_configs(id)` (0005:44-46).
Related: `host_roles(host_mac REFERENCES hosts(mac) ON DELETE CASCADE, role_id ...)` (0003:31-35) —
the precedent for `host_health(mac ... REFERENCES hosts(mac) ON DELETE CASCADE)`.

### 4.3 Adding a table/column — examples
- Plain ADD COLUMN with constant default + CHECK (0008_debian_source_mode.sql:4-8):
  ```sql
  ALTER TABLE targets ADD COLUMN source_mode TEXT NOT NULL DEFAULT 'netinst'
      CHECK (source_mode IN ('netinst','dvd'));
  ALTER TABLE targets ADD COLUMN dvd_count INTEGER NOT NULL DEFAULT 1;
  ```
  → `ALTER TABLE hosts ADD COLUMN report_token_gen INTEGER NOT NULL DEFAULT 0;` is valid SQLite.
- New table + host columns in one file: 0005_clusters.sql:16-46.
- Migration test pattern: `TestMigrate0008_DebianColumns` (migrate_test.go:465-494) uses `newTestStore`,
  checks `user_version`, inserts a row without new cols, asserts defaults, asserts CHECK rejection.

### 4.4 db.Host ↔ hardware.Host
- `db.Host` struct (pkg/db/host.go:12-37); `hostCols` constant (39-41); `scanHost` (43-61, NULL-able cols via sql.Null*).
- `UpsertHost` writes ONLY legacy columns mac..schematic + bumps last_seen (75-94). Later columns
  have dedicated setters: ApproveHost (124), RevokeHost (132), SetAssignment (141), SetBootMode (153),
  SetHostSchematic (165), SetHostCluster (177), SetHostMachineType (188), SetHostNodeConfig (200).
  Pattern for a new column (`report_token_gen`): add field + to `hostCols` + `scanHost`, plus a dedicated
  `BumpHostReportTokenGen(mac)` setter; do not add to UpsertHost.
- `GetHost` returns `sql.ErrNoRows` on miss (63-71). `db.ErrNotFound = errors.New("db: not found")`
  exists (pkg/db/cache.go:11) but GetHost does not use it.
- Mapping: `toDBHost` / `fromDBHost` (pkg/hardware/mac.go:210-232) — field-by-field; a new field must be added in both.
- Test helper: `newTestStore(t) *Store` (pkg/db/targets_test.go:8-16): `Open(filepath.Join(t.TempDir(), "booty.db"))` + Cleanup Close.
  No in-memory DB is used anywhere; always a temp file.

---

## 5. pkg/hardware (mac.go, 560 lines)

Exported API (line): `type Host` (22-45, JSON tags: mac, hostname, ip, booted, ignitionFile, os,
doInstall, schematic, approved, bootMode, assignedOS, assignedArch, assignedParams, uuid, serial,
configId, clusterId, machineType, nodeConfigId), `BootyData` (49), `NormalizeMAC(mac) (string, error)`
(57-63, `net.ParseMAC` → lowercase colon form; error `hardware: invalid MAC %q: %w`), `ErrNotFound`
(66 — `errors.New("hardware: host not found")`), `SetStore(*db.Store)` (88-94; nil resets),
`Load()` (112-133; opens own store if none injected, resets unknown list, imports hardware.json),
`ListUnknownHosts()` (237), `GetData()` (244), `HasHost(mac) (bool, error)` (279-301, no side effect),
`GetMacAddress(mac) (*Host, error)` (305-331; miss → `trackUnknown` + ErrNotFound; malformed MAC →
NormalizeMAC error, not ErrNotFound; nil store → ErrNotFound), `WriteMacAddress(mac, Host)` (336-353;
`UpsertHost`; errors "store not initialized" if nil), `RemoveMacAddress` (357), `Approve` (369),
`Revoke` (372), `SetAssignment` (388), `SetHostConfig` (404), `SetHostRoles` (419), `SetBootMode` (436),
`SetSchematic` (455), `SetHostCluster` (471), `SetHostMachineType` (487), `SetHostNodeConfig` (503),
`ListHosts() ([]*Host, error)` (518), `PreserveBoot()` (537).
Wrapping: every call goes through `withRLockedStore(fn)` (99-107) under `storeMu.RLock`; a nil store
yields `had=false`. No hostname validation exists (no hostname.go). Hardware tests use
`setupTempDB(t)` (mac_test.go:17-28: viper.Reset, DataDir=t.TempDir(), Load()).
DoInstall: only writer outside DB plumbing is pkg/tftp/tftp.go:125-133; nothing reads it (grep).

---

## 6. Rendering paths and hostname substitution

### 6.1 /ignition.json — pkg/http/ignition.go:26-178
1. MAC: `?mac=` else ARP on RemoteAddr (35-53). Lookup only if non-empty (62-70).
2. **DB-bound path** (75-87): `resolveConfig(store, host)` and `kind == "butane"` →
   `vars := ignitionVars(store, host)` (184-196: Hostname=host.Hostname, MAC, IP, Roles, ServerIP
   "ip:port") → `renderConfig("butane", src, vars)` → write with ct `application/json`.
3. **File-template path** (89-176): templateData{JoinString, ServerIP "ip:port", Hostname} (91-98);
   `ignitionFile = IGNITION_FILE` or `host.IgnitionFile` (100-104); **Hostname substituted at line 105**
   (`templateData.Hostname = host.Hostname`); `resolveWithinDataDir` (107); `template.ParseFiles` +
   `Execute` (116-125) — NOTE: this runs **even when host == nil**, so the reboot config still needs
   the template file to exist (ignition_test.go:37 comment).
4. **Reboot config** when host == nil (127-151): `coreOSType.Config{}` from
   `github.com/coreos/ignition/v2/config/v3_5_experimental/types` (import at 15), `Ignition.Version = "3.4.0"`,
   one unit `Name: "Reboot now please"`, `ExecStart=reboot`, `json.Marshal`, `w.Write` (no Content-Type set).
5. Else `butaneConfig.TranslateBytes(tpl, Pretty:true)` (153), report handling (156-174), `w.Write(ignCfg)` (176).
   Item 10's JSON-level merge must hook both step 2 (after `renderConfig`) and step 5.

### 6.2 render.go
`TemplateVars` (render.go:18-30: Hostname, MAC, IP, UUID, Serial, ServerIP, ServerHTTPPort, JoinString,
Roles, TalosVersion, Schematic). `renderConfig(kind, source, vars) (out, contentType, report, err)` (84-119):
**text/template execute first** (85-93), then per kind: butane → `butaneConfig.TranslateBytes` (97-104, ct
application/json); machineconfig → as-is text/yaml (106); debianconfig → `translateDebianConfig(rendered)`
text/plain (107-115). `renderPreseedFile(source, vars)` (69-79) for rung-4 raw preseed.
Hostname enters only through `vars.Hostname`; a single sanitisation point for DB-bound paths is
wherever vars are built (ignitionVars, preseedVars, machineConfigVarsCore) — or inside renderConfig.
Callers of renderConfig: ignition.go:78, machineconfig.go:66, preseed.go:37, api_configs.go:228
(preview, vars from `stubVars()` api_configs.go:381-386 or `previewVars` 394-401), api_configs.go:347
(save-time validation via `validateConfigSource`, stubVars Hostname "preview-host").

### 6.3 machineconfig.go
Handler (26-130): MAC from `?mac=` else ARP (30-41); frozen cluster node config served verbatim if
`host.NodeConfigID != nil` (58-61, `serveFrozenNodeConfig` 193-206 — no hostname substitution);
DB-bound: `renderConfig("machineconfig", src, machineConfigVars(store, r, host))` (65-74);
file path: templateData with **`Hostname: r.URL.Query().Get("hostname")` (line 89)** overridden by
`host.Hostname` if non-empty (101-103), template `TalosConfigFile` via resolveWithinDataDir (111).
`machineConfigVars` (138-141) → `machineConfigVarsCore(store, host, hostname, uuid, serial)` (161-186;
query hostname, overridden by host.Hostname at 173-175). Preview: `machineConfigPreviewVars` (148-154).
Item 4's 400 check for a non-empty invalid `?hostname=` goes at the top of the handler (before line 52).

### 6.4 preseed.go + debiangen.go
- Handler (preseed.go:25-70): `identifyHost(r)` (147-165, ?mac= or ARP; any lookup error → nil);
  DB-bound if `familyAllowsKind("preseed", kind)`: `preseedVars` (131-143, Hostname=host.Hostname at 137)
  → `renderConfig(kind, src, vars)` → `withDVDMirror` (42) → text/plain. Rung 4: `PreseedFile` read,
  `renderPreseedFile(src, preseedVars(...))` (60-61), `withDVDMirror`.
- `translateDebianConfig(rendered []byte) ([]byte, error)` (debiangen.go:606-620): yaml.Unmarshal into
  `debianConfigSpec` → `buildPreseedView(spec)` → `preseedTmpl.Execute`. **Templating (`{{ .Hostname }}`)
  happens BEFORE translation**, in renderConfig; translate sees the already-substituted YAML. So item 4's
  "refuse when templated hostname/domain has a control char" check fits in `buildPreseedView` or
  `translateDebianConfig` on `spec.Hostname`/`spec.Domain`.
- `buildPreseedView(spec debianConfigSpec) (preseedView, error)` (442-535): **takes no host/server
  info** — signature change needed for item 10. late_command composition order (509-526):
  ssh → sudo → ESP sync → operator (`flattenLateCommand(spec.LateCommand)`), joined `" ; "` (526);
  packages: `appendIfAbsent(pkgs, "openssh-server", ...)`, then `"sudo"` (529-531). Emission template
  `preseedTemplateText` (545-597): `d-i netcfg/get_hostname string {{ .Hostname }}` (549),
  `get_domain` (550), `d-i preseed/late_command string {{ .LateCommand }}` (596), raw_preseed last (597).
- NOTE (item 10): `translateDebianConfig` is also called by the converter round-trip
  `verifyRoundTrip` (preseedconv.go:516-543, called from `ConvertPreseedToDebianConfig` 430-481) and by
  save-time validation/preview via renderConfig with stubVars. If health injection lives unconditionally
  inside `buildPreseedView`, `booty convert-preseed` would report "converter added a directive" warnings
  and previews would differ. Inject it via an explicit parameter/option only on the serve path
  (handlePreseedRequest → renderConfig), gated on `--nodeReports`.
- preseedconv.go: `ConvertPreseedToDebianConfig(preseed []byte) ([]byte, []string, error)` (430),
  maps `netcfg/get_hostname` → `o.Hostname` (222-223); `stripESPSync` (491-511) is the precedent for
  stripping a booty-generated late_command fragment.

### 6.5 Hostname substitution sites (summary)
| family | site |
|---|---|
| ignition DB-bound | ignitionVars ignition.go:190 |
| ignition file | ignition.go:105 |
| talos DB-bound | machineConfigVarsCore machineconfig.go:164 (query) / 174 (host) |
| talos file | machineconfig.go:89 (query) / 102 (host) |
| preseed DB-bound + rung 4 | preseedVars preseed.go:137 |
| preview | stubVars api_configs.go:383; previewVars → ignitionVars / machineConfigPreviewVars (151) |
| cluster gen | none — api_clusters.go:41 is an output DTO only |

---

## 7. web/

- Scripts (web/package.json): `dev` vite, `build` `tsc -b && vite build`, `postbuild`, `preview`,
  `test` `vitest run`. **No lint script** (no eslint dep). CI (web-test.yml): `npm ci`, `npx tsc -b`, `npm test`.
  node_modules is NOT installed in this checkout (`ls web/node_modules` empty) — `npm ci` needed first.
  Vitest config in vite.config.ts: jsdom, globals, setupFiles `./src/test-setup.ts` (stubs matchMedia).
- HostsView.tsx (210 lines): state hosts/loading/error/configs/roles/allowing (15-23); `load()` via
  `listHosts()` (25-35); generic `act(fn, mac, okMsg)` → `message.success` / `message.error` then reload
  (45-53) — the pattern a Wake action should reuse. Columns: `baseCols` MAC, Hostname, IP, OS, Booted
  (106-112); `pendingCols` + Actions [Allow (primary), Boot menu] (114-135); `approvedCols` + Boot Mode,
  Assigned OS, Actions [Revoke (danger), Boot menu] (137-155). Two tabs Pending/Approved (161-174);
  Allow modal (176-207). No Popconfirm / Modal.confirm used anywhere in web/src (grep) — a Wake confirm
  would be the first; antd `Popconfirm` is available from 'antd'.
- API layer web/src/api/client.ts: `request<T>(path, init)` prefixes `/api/v1`, `credentials: 'same-origin'`,
  401 → `unauthorizedHandler`, non-ok → Error with body, 204 → undefined (16-32). Host calls:
  `listHosts` (53-56), `approveHost` (60), `approveHostWith` (66), `bindHost` (70), `bindSchematic` (77),
  `revokeHost` (81-83: `request(`/hosts/${mac}/revoke`, { method: 'POST' })`), `setMenuMode` (85).
  Types: web/src/api/types.ts `Host` (3-19; no health field), `isPending` (25).
- HostsView.test.tsx mocks **modules**, not fetch: `vi.mock('../api/client')`, `'../api/configs'`,
  `'../api/roles'`, `'../api/catalog'` (11-14); `vi.mocked(client.listHosts).mockResolvedValue([...])`;
  `beforeEach` mocks `loadFamilyKinds` (24-29); `afterEach(() => vi.resetAllMocks())` (31). Revoke test
  pattern (59-67): click Approved tab, `getByRole('button', { name: 'Revoke' })`, expect
  `client.revokeHost` called with mac. client.test.ts mocks fetch: `vi.stubGlobal('fetch', vi.fn(async () => new Response(...)))`
  and asserts URL/method (client.test.ts:7-40).
- ~193 vitest `it(`/`test(` cases across web/src (grep count).

---

## 8. Docs that need updating (exact lines)

**make targets**
- README.md:287-290 (`make build|run|image|docker-buildx`), 293 ("distroless image").
- docs/standards.md:15 (`make docker-buildx`, base-debian12). docs/development.md:5,18-32 use go/npm directly (no make) — add task note.
- Makefile at repo root (targets build/run/image/image-push/image-run/docker-buildx).

**DoInstall / one-shot install**
- README.md:242-244 ("One-shot install").
- docs/schema/API.md:55 (booty.ipxe row: "DoInstall flag ... flipped off").
- docs/schema/DATABASE.md:30 (`DoInstall` field), 145 (`do_install` column).

**--serverIP default 127.0.0.1**
- README.md:256 (config table "`127.0.0.1` ... (set this!)"), README.md:124 (Quickstart `--serverIP=10.0.0.5`).
- docs/CONFIGURATION.md:22.
- cmd/main.go:183 (flag default).

**undionly.kpxe / iPXE staging**
- README.md:301 (PXE vs iPXE note), README.md:144-147 (Quickstart step 4).
- docs/CONFIGURATION.md:37-39 ("staged in `--dataDir`").
- docs/schema/API.md:83 ("must be staged in `--dataDir`").
- docs/schema/STORAGE.md:20-22 (dataDir tree lists undionly.kpxe, ipxe.efi, ipxe-arm64.efi).
- cmd/main.go:210, 216, 222 (flag help "staged in dataDir"). Root `undionly.kpxe` file exists.

**proxyDHCP behaviour**
- docs/schema/API.md:75-85 (Pass 1 on :67 returns binary; Pass 2 on :4011 returns booty.ipxe) — changes with item 1.
- README.md:12, 47, 72, 94, 145-146, 262, 299-305.
- docs/CONFIGURATION.md:36-39 (flag table; add `--proxyDHCPRelay`), 636-643 (ports table), 647-649.

**Dockerfile base image**
- Dockerfile final stage `FROM gcr.io/distroless/base-debian12`; web stage `npm install`.
- docs/standards.md:15 and 254-255 (name base-debian12). README.md:293.

**TFTP serving of dataDir**
- docs/schema/API.md:48-57 (esp. 57: "*(any other path)* | Served from the artifact cache via a
  path-escape-checked join under `--dataDir`"), 50 ("resolved by ARP").
- docs/CONFIGURATION.md:637 (port 69: "iPXE binaries and the booty.ipxe chain script").
- docs/BOOT-MENU.md:245-247 (ARP + `tftp://<serverIP>/menu/...`), 284-285 ("TFTP-only"), 288-289 (ARP layer-2).
- README.md:38, 303-308 (ARP-based identification / VLAN note).
- docs/schema/STORAGE.md:26-29 (HTTP reachability note — add a TFTP equivalent).

**Other**: `.github/workflows/docker-publish.yml`, `image.yml` exist (item 11); go.mod:7-10 `replace` block
(transpiler + `pin/tftp => v0.0.0-20210809155059-0161c5dd2e96`), requires `pin/tftp v2.2.0+incompatible`,
`viper v1.10.1`, `butane v0.19.0`, `ignition/v2 v2.17.0`, `godotenv v1.4.0`, `jsonparser v1.1.1`,
`insomniacslk/dhcp v0.0.0-20260603135910-a415979eb11e` (item 12 / item 1).

---

## 9. proxyDHCP current code (for item 1)
pkg/proxydhcp/proxydhcp.go: `bootfileIPXEScript = "booty.ipxe"` (24); `Config` (30-35); `Server` (38-45);
`selectBootfile` (50-62); `buildReply(req, cfg)` (68-108: requires PXEClient class id; DISCOVER→OFFER,
REQUEST/INFORM→ACK, no port awareness, always sets bootfile/option 66/67/43, yiaddr 0); `pxeVendorOptions`
`{0x06,0x01,0x08,0xff}` (113-115); `isIPXE` substring check (120-123); `clientArch` defaults to
INTEL_X86PC when option 93 absent (128-134); `NewServer` rejects empty bootfiles (137-145); `Start` binds
67 (+broadcast) and 4011 (149-176); `Shutdown` (180-191); `loop(conn, broadcast bool)` (199-224);
`handle(conn, src, req, broadcast)` (226-242: broadcast → 255.255.255.255:68, else unicast to src).
Tests: 13 funcs in proxydhcp_test.go (`testCfg` 13, `reqWithArch` 22, `replyCfg` 66, ...).
broadcast_unix.go is `//go:build unix`.

---

## 10. Test suite baseline
- Go: 721 `func Test` across repo (cmd 21, auth 16, cache 137, checksum 4, config 12, db 76,
  hardware 30, http 298, ostype 67, proxydhcp 13, tftp 47). Run results: see section 11.
- Web: ~193 vitest cases; not run (node_modules absent).

---

## 11. `go test -count=1 ./...` results (run 2026-10-11)
All 11 packages pass (exit 0). The first run downloaded modules and compiled. A warm second run took
**47 s wall time**, dominated by pkg/http (44.2 s). Per package: cmd 0.06s, auth 0.10s, cache 3.97s,
checksum 0.005s, config 0.02s, db 2.69s, hardware 1.34s, http 44.2s, ostype 0.05s, proxydhcp 0.005s,
tftp 0.12s; `web` has no Go tests. CI runs it with `-race` (go-test.yml), which is slower; that was not timed here.
