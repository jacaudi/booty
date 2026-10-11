# Phase 6: Boot script over HTTP, identified by MAC (item 7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** `booty.ipxe` over TFTP becomes a stub that chains to `http://<urlHost>/booty.ipxe?mac=${mac}`. HTTP renders the per-host script (holding, assigned or menu) and menu selections, identifying the host by `?mac=` with ARP as fallback. Every in-script re-chain uses HTTP with `?mac=${mac}`. The `DoInstall` flip is removed.

**Architecture:** One renderer in `pkg/tftp` (exported) used by both TFTP and new HTTP handlers in `pkg/http`. `pkg/http` → `pkg/tftp` adds no import cycle (`pkg/tftp` imports only cache, config, db, hardware, ostype).

**Tech Stack:** Go `net/http` method patterns (`"GET /booty.ipxe"`).

**Spec:** design item 7; finding 2 (DoInstall).

## Global Constraints

See `README.md`. Depends on phase 1 (`arpFunc`, `arpLookup`, `requestFromMAC` in `pkg/http/arp.go`) and phase 2 (`tftp.Server` with `server.readHandler`).

## Review Focus

1. A malformed `?mac=` (e.g. `?mac=zz`) must give the holding script, not a 500 and not an ARP-identified host's script. Task 2 table.
2. A host with `DoInstall=true` must still have `DoInstall=true` after fetching `/booty.ipxe` over HTTP or TFTP. Task 3 test.
3. A menu-mode host's selection over HTTP for an uncached tuple must give the holding script. Task 2 table.
4. An unknown MAC that fetches `/booty.ipxe` must appear in the unknown-hosts list, as it did over TFTP. Task 2 test.
5. Every generated script (holding, menu, tool guard) must chain with `http://<urlHost>/...?mac=${mac}` and contain no `tftp://`. Task 1 tests.

---

### Task 1: Exported renderer and HTTP chain lines in `pkg/tftp`

**Files:**
- Modify: `pkg/tftp/pxe_config.go:47` (holding)
- Modify: `pkg/tftp/tool_scripts.go:69,117,149` (and any other `chain tftp://[[server-ip]]/booty.ipxe` line in that file; find them with `grep -n 'chain tftp' pkg/tftp/tool_scripts.go`)
- Modify: `pkg/tftp/menu.go` (`renderMenu` lines 129-204, `menuSelectionScript` lines 100-117)
- Modify: `pkg/tftp/tftp.go` (`readHandler`: the URL-host computation at lines 137-141, the DoInstall flip at 125-133, the `booty.ipxe` branch at 155-180)
- Create: `pkg/tftp/render.go`, `pkg/tftp/render_test.go`
- Modify tests: `pkg/tftp/menu_test.go:42,46,107`, `pkg/tftp/tftp_test.go:301-313` and `:579-583`, `pkg/tftp/tool_scripts_test.go:214,233` (and the matching assertion for shredos, if any)

**Interfaces:**
- Produces (package `tftp`):
  - `func URLHost() string`: `viper.GetString(config.ServerIP)`, plus `":<port>"` unless `viper.GetInt(config.ServerHttpPort) == 80`. This is the existing expression at `tftp.go:137-141`, moved.
  - `func StubScript(urlHost string) string`: returns exactly `"#!ipxe\nchain http://" + urlHost + "/booty.ipxe?mac=${mac} || shell\n"`.
  - `func RenderBootScript(host *hardware.Host, urlHost string) (script, kind string)`: the `booty.ipxe` branch body. `kind` is one of `"holding"`, `"assigned"`, `"menu"`.
  - `func RenderMenuSelection(host *hardware.Host, filename, urlHost string) string`: the renamed `menuSelectionScript`.
  - `renderMenu(inWindow, archived []cache.CacheEntry, urlHost string) string`: the parameter changes from bare server IP to `urlHost`.

- [ ] **Step 1: Write the failing tests** in `pkg/tftp/render_test.go`:

```go
func TestStubScript(t *testing.T) {
	want := "#!ipxe\nchain http://10.0.0.1:8080/booty.ipxe?mac=${mac} || shell\n"
	if got := StubScript("10.0.0.1:8080"); got != want {
		t.Fatalf("StubScript = %q, want %q", got, want)
	}
}

func TestNoTFTPChainsRemain(t *testing.T) {
	for name, tmpl := range PXEConfig {
		if strings.Contains(tmpl, "chain tftp://") {
			t.Errorf("%s still chains over TFTP:\n%s", name, tmpl)
		}
	}
	menu := renderMenu(nil, nil, "10.0.0.1:8080")
	if strings.Contains(menu, "tftp://") || !strings.Contains(menu, "chain http://10.0.0.1:8080/booty.ipxe?mac=${mac} || shell") {
		t.Errorf("menu retry chain wrong:\n%s", menu)
	}
}

func TestRenderBootScriptKinds(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.DataDir, t.TempDir())
	SetStore(nil)
	cases := []struct {
		name   string
		host   *hardware.Host
		kind   string
		marker string
	}{
		{"unknown", nil, "holding", "not yet approved"},
		{"unapproved", &hardware.Host{MAC: "aa:bb:cc:dd:ee:01"}, "holding", "not yet approved"},
		{"assigned flatcar", &hardware.Host{MAC: "aa:bb:cc:dd:ee:02", Approved: true, BootMode: "assigned", AssignedOS: "flatcar"}, "assigned", "flatcar_production_pxe.vmlinuz"},
		{"menu", &hardware.Host{MAC: "aa:bb:cc:dd:ee:03", Approved: true, BootMode: "menu"}, "menu", "menu Booty - select an image to boot"},
	}
	for _, c := range cases {
		script, kind := RenderBootScript(c.host, "10.0.0.1:8080")
		if kind != c.kind || !strings.Contains(script, c.marker) {
			t.Errorf("%s: kind=%q, want %q; marker %q missing:\n%s", c.name, kind, c.kind, c.marker, script)
		}
	}
}
```

- [ ] **Step 2:** Run `go test ./pkg/tftp/ -run 'TestStubScript|TestNoTFTPChainsRemain|TestRenderBootScriptKinds'`. Expected: FAIL (undefined).
- [ ] **Step 3: Change the chain lines.**
  - `pxe_config.go:47`: `chain tftp://[[server-ip]]/booty.ipxe || shell` → `chain http://[[server]]/booty.ipxe?mac=${mac} || shell`.
  - Every `chain tftp://[[server-ip]]/booty.ipxe || shell` in `tool_scripts.go` → `chain http://[[server]]/booty.ipxe?mac=${mac} || shell`. The tool token map already carries `[[server]]` (`bootTokensFor`, `tftp.go:243-277`).
  - In `renderMenu`, rename the parameter `serverIP` to `urlHost` and emit:
    - `chain http://<urlHost>/menu/${sel}/boot.ipxe?mac=${mac} || goto retry`
    - `chain http://<urlHost>/menu/${asel}/boot.ipxe?mac=${mac} || goto top`
    - `chain http://<urlHost>/menu/${tsel}/boot.ipxe?mac=${mac} || goto top`
    - `chain http://<urlHost>/booty.ipxe?mac=${mac} || shell`

    Update its doc comment ("urlHost is serverIP[:port]").
  - In `menuSelectionScript`, build the holding fallback with `holdingScript(urlHost)` (`tftp.go:231`) instead of the inline duplicate (lines 101-104). Rename the function to `RenderMenuSelection`.
- [ ] **Step 4: Create `pkg/tftp/render.go`** with `URLHost`, `StubScript` and `RenderBootScript`. Move the `switch kind` body from `readHandler`'s `booty.ipxe` branch (`tftp.go:155-180`) into `RenderBootScript`. In the menu arm, call `renderMenu(inWindow, archEntries, urlHost)`; previously it passed `viper.GetString(config.ServerIP)`.
- [ ] **Step 5: Simplify `readHandler`** (the method from phase 2):
  - delete the DoInstall flip (lines 125-133 at `f36482c`);
  - move the ARP + `hardware.GetMacAddress` block into a method `(s *server) hostFromARP(ip net.IP) *hardware.Host`, called **only** in the `menu/` and `pxelinux.cfg/default` branches;
  - replace the URL-host lines with `urlHost := URLHost()`;
  - `booty.ipxe` branch: serve `StubScript(urlHost)` with no ARP, logging `"kind", "stub"`;
  - `menu/` branch: `RenderMenuSelection(s.hostFromARP(raddr.IP), filename, urlHost)`.
- [ ] **Step 6: Update the existing tests that assert TFTP chains:**
  - `menu_test.go:42`: expect `chain http://10.0.0.1/menu/${sel}/boot.ipxe?mac=${mac}`.
  - `menu_test.go:46`: expect `chain http://10.0.0.1/booty.ipxe?mac=${mac}`.
  - `menu_test.go:107`: `holdingMarker := "http://" + server + "/booty.ipxe?mac=${mac}"`.
  - These tests pass `"10.0.0.1"` as the third argument; it is now treated as `urlHost`, so the expected strings above contain no port.
  - `tftp_test.go:301-313` (`TestHoldingTemplateExists`): assert the template contains `http://[[server]]/booty.ipxe?mac=${mac}` and does **not** contain `tftp://`. Replace the comment at 306-307 with: `The holding loop re-chains over HTTP so the MAC travels in the URL (design item 7).`
  - `tftp_test.go:579-583`: change the empty-marker check from `tftp:///` to `http:///` and the message to `[[server]] substituted as empty`.
  - `tool_scripts_test.go:214` and `:233`: expect `chain http://[[server]]/booty.ipxe?mac=${mac}`.
  - Callers of `menuSelectionScript` in tests: rename them to `RenderMenuSelection`.
- [ ] **Step 7:** Run `go vet ./pkg/tftp/ && go test -race ./pkg/tftp/`. Expected: `ok`.
- [ ] **Step 8:** Commit: `git add pkg/tftp && git commit -m "feat(tftp): booty.ipxe over TFTP becomes a stub; scripts re-chain over HTTP with ?mac=; drop the DoInstall flip"`.

### Task 2: HTTP `/booty.ipxe` and `/menu/.../boot.ipxe`

**Files:**
- Create: `pkg/http/bootscript.go`, `pkg/http/bootscript_test.go`
- Modify: `pkg/http/http.go` (`baseMux`, lines 32-54)

**Interfaces:**
- Consumes: `tftp.URLHost`, `tftp.RenderBootScript`, `tftp.RenderMenuSelection`, plus `arpFunc`/`arpLookup` (phase 1).
- Produces:
  - `func hostFromRequest(r *http.Request, arp arpFunc) *hardware.Host`:
    - `?mac=` present and normalisable → `hardware.GetMacAddress` (which records unknown MACs);
    - `?mac=` present but malformed → `nil`;
    - `?mac=` absent → ARP on `r.RemoteAddr`, then `GetMacAddress`;
    - any lookup error → `nil`.
  - `func bootScriptHandler(arp arpFunc) http.HandlerFunc` and `func menuSelectionHandler(arp arpFunc) http.HandlerFunc`.

- [ ] **Step 1: Write the failing tests** in `pkg/http/bootscript_test.go`. Use `s := servingStore(t)`, `viper.Set(config.ServerHttpPort, 8080)`, and `noARP := func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }`. Seed the menu host with `hardware.WriteMacAddress(mac, hardware.Host{MAC: mac})` then `hardware.Approve(mac)` (`pkg/hardware/mac.go:369`). New rows default to `boot_mode='menu'` (`pkg/db/migrations/0001_init.sql`), so no `SetBootMode` call is needed.

  | case | request | expected status / body |
  |---|---|---|
  | unknown MAC | `GET /booty.ipxe?mac=aa:bb:cc:dd:ee:a0` | 200, contains `not yet approved`, and `hardware.ListUnknownHosts()` now includes `aa:bb:cc:dd:ee:a0` |
  | malformed MAC | `GET /booty.ipxe?mac=zz` | 200, contains `not yet approved` |
  | menu host | `GET /booty.ipxe?mac=<menu mac>` | 200, contains `menu Booty - select an image to boot` |
  | ARP fallback | `GET /booty.ipxe` with `RemoteAddr` `10.0.0.9:1`, arp returning the menu MAC | 200, menu script |
  | menu selection, uncached tuple | `GET /menu/talos/x/amd64/v9.9.9/boot.ipxe?mac=<menu mac>` | 200, contains `not yet approved` |
  | wrong method | `POST /booty.ipxe` | 405 |

  The `Content-Type` of every 200 must be `text/plain; charset=utf-8`.

  For the method check, mount through a mux: `mux := http.NewServeMux(); mux.Handle("GET /booty.ipxe", bootScriptHandler(noARP))`. The other cases call the handler directly.
- [ ] **Step 2:** Run `go test ./pkg/http/ -run BootScript`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement `pkg/http/bootscript.go`.**
  - Both handlers set `Content-Type: text/plain; charset=utf-8`.
  - `bootScriptHandler`: `script, kind := tftp.RenderBootScript(hostFromRequest(r, arp), tftp.URLHost())`; log `slog.Info("boot script", "kind", kind, "mac", r.URL.Query().Get("mac"))`; write `script`.
  - `menuSelectionHandler`: `filename := strings.TrimPrefix(r.URL.Path, "/")`; write `tftp.RenderMenuSelection(hostFromRequest(r, arp), filename, tftp.URLHost())`.
- [ ] **Step 4: Register in `baseMux`** after the `/preseed` line:

```go
	myHandler.Handle("GET /booty.ipxe", bootScriptHandler(arpLookup))
	myHandler.Handle("GET /menu/", menuSelectionHandler(arpLookup))
```

- [ ] **Step 5:** Run `go vet ./pkg/http/ && go test -race ./pkg/http/`. Expected: `ok`. `TestRetiredRoutesAreGone` (`http_test.go:405-430`) must still pass. It checks retired routes only; if it lists `/booty.ipxe` as retired, remove that one entry and note it in the commit message.
- [ ] **Step 6:** Commit: `git add pkg/http && git commit -m "feat(http): serve the per-host boot script and menu selections over HTTP by ?mac= with ARP fallback"`.

### Task 3: DoInstall stays put; docs

**Files:** Test `pkg/http/bootscript_test.go`; Modify `README.md:242-244`, `docs/schema/API.md:48-57`, `docs/schema/DATABASE.md:30,145`, `docs/BOOT-MENU.md:245-289`, `README.md:303-308`.

- [ ] **Step 1: Write the test** `TestBootScriptDoesNotChangeDoInstall`:
  - seed a host via `hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, DoInstall: true})`;
  - GET `/booty.ipxe?mac=<mac>`;
  - re-read with `hardware.GetMacAddress(mac)`;
  - assert `DoInstall` is still `true`.
- [ ] **Step 2:** Run `go test ./pkg/http/ -run TestBootScriptDoesNotChangeDoInstall`. Expected: `ok` (Task 1 removed the only writer).
- [ ] **Step 3: Docs.**
  - `README.md:242-244` ("One-shot install") → `**DoInstall.** The host record carries a DoInstall flag for a future install-once feature. booty does not act on it yet.`
  - `docs/schema/API.md` TFTP table:
    - `booty.ipxe` → `A stub that chains to http://<serverIP>[:port]/booty.ipxe?mac=${mac}. The per-host script (holding / assigned OS / menu) is rendered over HTTP.`
    - Add rows for `GET /booty.ipxe?mac=` and `GET /menu/<os>/<segment>/<arch>/<version>/boot.ipxe?mac=` to the HTTP endpoint table. Both are open, read-only, identified by `?mac=` with ARP fallback.
    - Line 50: "resolved by ARP" → "identified by `?mac=` (ARP fallback)".
  - `docs/schema/DATABASE.md:30,145`: describe `do_install`/`DoInstall` as `reserved; not acted on`.
  - `docs/BOOT-MENU.md`:
    - steps table rows 1 and 3: TFTP fetches a stub, the HTTP `/booty.ipxe?mac=` renders the menu, and selections chain `http://<serverIP>[:port]/menu/.../boot.ipxe?mac=${mac}`;
    - Troubleshooting "same layer-2 segment": identification uses the MAC iPXE reports, and ARP is only a fallback;
    - Limitations: delete the "TFTP-only" bullet; change the layer-2 bullet to `Routed networks: the boot script and menu work across routers (identification by ?mac=), but TFTP and proxyDHCP still need L2 adjacency or a relay (--proxyDHCPRelay), and Talos cluster members need ARP (design item 0b).`
  - `README.md:303-308` (VLAN note): same correction as the BOOT-MENU limitation.
  - `docs/releases/UNRELEASED.md` `## Changes`: `booty.ipxe is now rendered over HTTP (/booty.ipxe?mac=) and identifies hosts by the MAC iPXE reports; TFTP serves a one-line stub. DoInstall is no longer cleared on boot; it never changed what booted.`
- [ ] **Step 4:** Commit: `git add pkg/http README.md docs && git commit -m "docs: HTTP boot script, MAC identification and DoInstall"`.

### Phase exit check

`go build ./... && go vet ./... && go test -race ./...` → all `ok`.
