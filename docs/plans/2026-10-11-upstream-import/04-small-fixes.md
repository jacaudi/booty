# Phase 4: MAC on config URLs, hostname validation, serverIP detection (items 2, 4, 5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:**
- Flatcar, CoreOS and Debian boots identify themselves by `?mac=`.
- Hostnames entering the API are RFC 1123-valid, and stored ones can no longer inject lines into rendered configs.
- `--serverIP` is detected when unset.

**Architecture:** Template string edits in `pkg/tftp`; a ported validator plus a control-character stripper in `pkg/hardware`; a pure resolver in `cmd`.

**Tech Stack:** Go stdlib (`regexp`, `unicode`, `net`).

**Spec:** `docs/designs/2026-10-11-upstream-import-design.md` items 2, 4, 5.

## Global Constraints

See `README.md`.

## Review Focus

1. A hostname stored before this change containing `\n` must not produce a second line in Butane, Talos YAML or preseed output. Task 3 tests.
2. A debianconfig whose `hostname:` is the template `{{ .Hostname }}` must still save and render. Task 4 test.
3. `--serverIP` set explicitly must never be replaced by detection, even if detection would fail. Task 5 table.
4. A detector that returns `127.0.0.1` (a host whose only route is loopback) must cause a startup error, not a silent 127.0.0.1. Task 5 table.
5. An empty `?hostname=` on `/machineconfig` must still render, since DHCP often provides none. Task 2 test.

---

### Task 1: `?mac=${mac}` on config URLs (item 2)

**Files:** Modify `pkg/tftp/pxe_config.go:29,36,56`; Modify `pkg/tftp/tftp_test.go:395`; Test in `pkg/tftp/tftp_test.go`.

- [ ] **Step 1: Write the failing test** (append to `pkg/tftp/tftp_test.go`):

```go
func TestConfigURLsCarryMAC(t *testing.T) {
	for key, want := range map[string]string{
		"flatcar.ipxe": "ignition.config.url=http://[[server]]/ignition.json?mac=${mac}",
		"coreos.ipxe":  "set CONFIGURL http://[[server]]/ignition.json?mac=${mac}",
		"debian.ipxe":  "preseed/url=http://[[server]]/preseed?mac=${mac}",
	} {
		if !strings.Contains(PXEConfig[key], want) {
			t.Errorf("%s does not contain %q:\n%s", key, want, PXEConfig[key])
		}
	}
	if strings.Contains(PXEConfig["flatcar"], "${mac}") {
		t.Error("pxelinux flatcar entry must stay free of iPXE variables")
	}
}
```

- [ ] **Step 2:** Run `go test ./pkg/tftp/ -run TestConfigURLsCarryMAC`. Expected: FAIL for all three keys.
- [ ] **Step 3:** Edit `pkg/tftp/pxe_config.go`:
  - line 29: `ignition.config.url=http://[[server]]/ignition.json` → `ignition.config.url=http://[[server]]/ignition.json?mac=${mac}`
  - line 36: `set CONFIGURL http://[[server]]/ignition.json` → `set CONFIGURL http://[[server]]/ignition.json?mac=${mac}`
  - line 56: `preseed/url=http://[[server]]/preseed` → `preseed/url=http://[[server]]/preseed?mac=${mac}`
- [ ] **Step 4:** In `pkg/tftp/tftp_test.go:395`, change the asserted substring to `preseed/url=http://10.0.0.1:8080/preseed?mac=${mac}`.
- [ ] **Step 5:** Run `go test -race ./pkg/tftp/`. Expected: `ok`. If any other existing test asserted an unchanged URL, it fails here; update its expected string to include `?mac=${mac}` and nothing else.
- [ ] **Step 6:** Commit: `git add pkg/tftp && git commit -m "fix(tftp): pass ?mac=\${mac} on Flatcar, CoreOS and Debian config URLs"`.

### Task 2: `ValidateHostname` and its two entry points (item 4)

**Files:**
- Create: `pkg/hardware/hostname.go`, `pkg/hardware/hostname_test.go`
- Modify: `pkg/http/api_hosts.go` (create-host, before line 176 `existing, err := hardware.GetMacAddress`)
- Modify: `pkg/http/machineconfig.go` (top of `machineConfigHandler`)
- Test: `pkg/http/api_hosts_test.go`, `pkg/http/machineconfig_test.go`

**Interfaces:**
- Produces: `hardware.MaxHostnameLen = 253`, `hardware.ErrInvalidHostname`, `func hardware.ValidateHostname(name string) error`.

- [ ] **Step 1:** Create `pkg/hardware/hostname.go` with `git show upstream/main:pkg/hardware/hostname.go`, **lines 1-41 only** (package clause, imports `errors`, `fmt`, `regexp`, `strings`, then `MaxHostnameLen`, `ErrInvalidHostname`, `hostnameRe`, `ValidateHostname`). Drop the `bytes` and `text/template` imports. Add `// Ported from jeefy/booty pkg/hardware/hostname.go @ cae75c8 (MIT).`
- [ ] **Step 2:** Create `pkg/hardware/hostname_test.go` with `TestValidateHostname`, copied from `git show upstream/main:pkg/hardware/hostname_test.go` lines 8-40. Add three rows: `{"a\nb", false}`, `{"a\n", false}`, `{"\x7fa", false}`.
- [ ] **Step 3:** Run `go test ./pkg/hardware/ -run TestValidateHostname`. Expected: `ok`.
- [ ] **Step 4: Write the failing API test** in `pkg/http/api_hosts_test.go`. Use `hostsTestSetup(t)` and `newTestAPI(t, deps)`:
  - `api.Post("/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:90", "hostname": "bad_name"})` → status 400;
  - `api.Post("/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:91", "hostname": "good-name"})` → status 201;
  - `api.Post("/hosts", map[string]any{"mac": "aa:bb:cc:dd:ee:92"})` → status 201.
- [ ] **Step 5:** In create-host, before `existing, err := hardware.GetMacAddress(in.Body.MAC)`, add:

```go
		if in.Body.Hostname != "" {
			if err := hardware.ValidateHostname(in.Body.Hostname); err != nil {
				return nil, huma.Error400BadRequest("invalid hostname", err)
			}
		}
```

- [ ] **Step 6: Write the failing machineconfig test** in `pkg/http/machineconfig_test.go`, using the setup of `TestMachineConfig_RendersTemplate` (lines 29-58). Call `machineConfigHandler(store, failingARP)` (phase 1 test helper) so no real ARP runs:
  - `/machineconfig?hostname=bad_name` → 400;
  - `/machineconfig?hostname=` → 200;
  - `/machineconfig?hostname=node-x` → 200 and the body contains `node-x`.
- [ ] **Step 7:** At the top of the handler returned by `machineConfigHandler` (phase 1), before MAC resolution, add:

```go
		if h := r.URL.Query().Get("hostname"); h != "" {
			if err := hardware.ValidateHostname(h); err != nil {
				writeError(w, http.StatusBadRequest, "invalid hostname", err)
				return
			}
		}
```

- [ ] **Step 8:** Run `go test -race ./pkg/hardware/ ./pkg/http/`. Expected: `ok`.
- [ ] **Step 9:** Commit: `git add pkg/hardware pkg/http && git commit -m "fix(http): validate hostnames on create-host and /machineconfig (RFC 1123)"`.

### Task 3: Strip control characters at render; warn about stored invalid names (item 4)

**Files:**
- Modify: `pkg/hardware/hostname.go` (add two functions)
- Modify: `pkg/http/ignition.go` (`ignitionVars` line 190; file path line 105)
- Modify: `pkg/http/machineconfig.go` (`machineConfigVarsCore` host override around line 174; file path host override around line 102)
- Modify: `pkg/http/preseed.go` (`preseedVars` line 137)
- Modify: `cmd/main.go` (after `hardware.Load()`, line 394)

**Interfaces:**
- Produces:
  - `func hardware.StripControl(s string) string`: returns `s` with every rune for which `unicode.IsControl` is true removed.
  - `func hardware.InvalidHostnames() ([]string, error)`: for every host from `ListHosts()` with a non-empty hostname failing `ValidateHostname`, returns the string `mac + "=" + strconv.Quote(hostname)`, sorted.

- [ ] **Step 1: Write the failing tests** in `pkg/hardware/hostname_test.go`:
  - `TestStripControl`, as a table:

    | input | output |
    |---|---|
    | `"node1"` | `"node1"` |
    | `"a\nb"` | `"ab"` |
    | `"a\r\n\tb\x00"` | `"ab"` |
    | `"web_01"` | `"web_01"` (unchanged: not a control character) |

  - `TestInvalidHostnames`: `setupTempDB(t)` (`pkg/hardware/mac_test.go:17-28`), then `WriteMacAddress` three hosts with hostnames `good`, `web_01` and `a\nb`. Assert the result equals `[]string{"<mac2>=\"web_01\"", "<mac3>=\"a\\nb\""}`, sorted by MAC.
- [ ] **Step 2:** Run `go test ./pkg/hardware/ -run 'TestStripControl|TestInvalidHostnames'`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement both functions in `pkg/hardware/hostname.go`. `StripControl` uses `strings.Map` returning `-1` for control runes.
- [ ] **Step 4: Write the render tests** in a new file `pkg/http/hostname_render_test.go`. Every test uses `s := servingStore(t)` (`pkg/http/serving_test.go:18`) and seeds `hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: <os>, Hostname: "evil\nbad", Approved: true})`. The stripped value is `evilbad`. Cases:
  - `TestVarsStripStoredHostname` (DB-bound sites): assert all three of these equal `"evilbad"`:
    - `ignitionVars(s, host).Hostname`;
    - `preseedVars(s, host).Hostname`;
    - `machineConfigVarsCore(s, host, "", "", "").Hostname`.
  - `TestIgnitionFileTemplateStripsHostname`: OS `flatcar`; `viper.Set(config.IgnitionFile, "config/ignition.yaml")`; `writeFile(t, "config/ignition.yaml", "variant: flatcar\nversion: 1.1.0\nstorage:\n  files:\n    - path: /etc/hostname\n      contents:\n        inline: {{ .Hostname }}\n")`. Request `GET /ignition.json?mac=<mac>` through `handleIgnitionRequest(s)`. Assert status 200 and that the body contains `evilbad`. Without the fix the newline splits the YAML and the handler returns 500.
  - `TestMachineConfigFileTemplateStripsHostname`: OS `talos`; `viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")`; `writeFile(t, "config/machineconfig.yaml", "machine:\n  network:\n    hostname: {{ .Hostname }}\n")`. Request `GET /machineconfig?mac=<mac>` through `handleMachineConfigRequest(s)`. Assert 200, the body contains `hostname: evilbad`, and it does not contain `evil\nbad`.
  - `TestPreseedFileTemplateStripsHostname`: OS `debian`; `viper.Set(config.PreseedFile, "config/preseed.cfg")`; `writeFile(t, "config/preseed.cfg", "d-i netcfg/get_hostname string {{ .Hostname }}\n")`. Request `GET /preseed?mac=<mac>` through `handlePreseedRequest(s)`. Assert 200 and that the body contains `get_hostname string evilbad`.
  Run `go test ./pkg/http/ -run 'StripsHostname|TestVarsStripStoredHostname'`. Expected: FAIL (hostnames still contain the newline; the Ignition case returns 500).
- [ ] **Step 5:** Apply `hardware.StripControl` at each site where a **stored** host hostname enters render vars:
  - `ignitionVars`: `vars.Hostname = hardware.StripControl(host.Hostname)`;
  - `ignition.go:105`: `templateData.Hostname = hardware.StripControl(host.Hostname)`;
  - `machineConfigVarsCore`: where `host.Hostname` overrides the query hostname;
  - `machineconfig.go`, file path: `templateData.Hostname = hardware.StripControl(host.Hostname)`;
  - `preseedVars`: `vars.Hostname = hardware.StripControl(host.Hostname)`.

  The query `?hostname=` needs no stripping: Task 2 refuses invalid values.
  Then run `go test ./pkg/http/ -run 'StripsHostname|TestVarsStripStoredHostname'`. Expected: `ok`.
- [ ] **Step 6:** In `cmd/main.go`, right after the `hardware.Load()` error check (line 394-396), add:

```go
	if bad, err := hardware.InvalidHostnames(); err != nil {
		slog.Warn("hardware: hostname check failed", "err", err)
	} else if len(bad) > 0 {
		slog.Warn("hosts with hostnames that are not valid RFC 1123 names; control characters are stripped when rendering, fix them in the UI", "hosts", bad)
	}
```

- [ ] **Step 7:** Run `go vet ./... && go test -race ./pkg/hardware/ ./pkg/http/ ./cmd/`. Expected: `ok`.
- [ ] **Step 8:** Commit: `git add pkg/hardware pkg/http cmd && git commit -m "fix(http): strip control characters from stored hostnames when rendering; warn at startup"`.

### Task 4: debianconfig refuses control characters after templating (item 4)

**Files:** Modify `pkg/http/debiangen.go` (`translateDebianConfig`, lines 606-620); Test `pkg/http/debiangen_test.go`.

- [ ] **Step 1: Write the failing tests** in `pkg/http/debiangen_test.go`. The existing helper `translate(t, src string) string` (`debiangen_test.go:11-18`) fails the test on error, so the error cases call `translateDebianConfig([]byte(src))` directly:
  - `TestDebianconfigRefusesControlCharacters`, as a table:

    | source | expected error contains |
    |---|---|
    | `"hostname: \"a\\nb\"\n"` | `hostname contains a control character` |
    | `"domain: \"ex\\tample.com\"\n"` | `domain contains a control character` |

  - `TestDebianconfigPlainHostnameStillEmits`: assert `translate(t, "hostname: node1\n")` contains `d-i netcfg/get_hostname string node1`.
  - `TestDebianconfigTemplateHostnameRenders`:
    - `out, _, _, err := renderConfig("debianconfig", []byte("hostname: \"{{ .Hostname }}\"\n"), TemplateVars{Hostname: "node7"})`;
    - assert `err == nil`;
    - assert `string(out)` contains `get_hostname string node7`.
- [ ] **Step 2:** Run `go test ./pkg/http/ -run 'Debian|debianconfig'`. Expected: `TestDebianconfigRefusesControlCharacters` FAILS (no error returned); the other two pass.
- [ ] **Step 3:** Add `"unicode"` to the imports of `pkg/http/debiangen.go`. In `translateDebianConfig`, after `yaml.Unmarshal` succeeds and before `buildPreseedView`, add:

```go
	for field, v := range map[string]string{"hostname": spec.Hostname, "domain": spec.Domain} {
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("http: debianconfig: %s contains a control character", field)
		}
	}
```

- [ ] **Step 4:** Run `go test -race ./pkg/http/`. Expected: `ok`.
- [ ] **Step 5:** Commit: `git add pkg/http && git commit -m "fix(http): refuse a debianconfig whose templated hostname or domain has a control character"`.

### Task 5: Auto-detect `--serverIP` (item 5)

**Files:**
- Modify: `pkg/config/config.go` (imports lines 3-20; add two functions)
- Modify: `cmd/main.go` (`serverIP` flag default line 183; `run` near line 371)
- Test: `cmd/main_test.go`, `pkg/config/config_test.go`

**Interfaces:**
- Produces:
  - `func config.DetectServerIP() (string, error)`: port of upstream `pkg/config/config.go:542-555`.
  - `func config.CloseQuietly(c io.Closer, what string)`: port of upstream `pkg/config/config.go:618-625`.
  - `func resolveServerIP(explicit string, detect func() (string, error)) (string, error)` in `cmd/main.go`.

- [ ] **Step 1: Write the failing table test** in `cmd/main_test.go` (add `errors` and `strings` to its imports if absent):

```go
func TestResolveServerIP(t *testing.T) {
	ok := func(ip string) func() (string, error) { return func() (string, error) { return ip, nil } }
	fail := func() (string, error) { return "", errors.New("no route") }
	cases := []struct {
		name     string
		explicit string
		detect   func() (string, error)
		want     string
		wantErr  bool
	}{
		{"explicit wins over a failing detector", "10.0.0.5", fail, "10.0.0.5", false},
		{"explicit wins over a working detector", "10.0.0.5", ok("192.168.1.9"), "10.0.0.5", false},
		{"empty uses detector", "", ok("192.168.1.9"), "192.168.1.9", false},
		{"detector error", "", fail, "", true},
		{"detector returns loopback", "", ok("127.0.0.1"), "", true},
		{"detector returns unspecified", "", ok("0.0.0.0"), "", true},
		{"detector returns IPv6", "", ok("fd00::1"), "", true},
		{"detector returns garbage", "", ok("not-an-ip"), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveServerIP(c.explicit, c.detect)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("resolveServerIP(%q) = %q, %v; want %q, err=%v", c.explicit, got, err, c.want, c.wantErr)
			}
			if c.wantErr && !strings.Contains(err.Error(), "set --serverIP") {
				t.Fatalf("error must tell the operator what to do: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2:** Run `go test ./cmd/ -run TestResolveServerIP`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement `resolveServerIP` in `cmd/main.go`:
  - If `explicit != ""`, return it unchanged.
  - Otherwise call `detect()`. On error, return `fmt.Errorf("no default route (%w); set --serverIP to the address clients reach booty on", err)`.
  - Parse the result with `net.ParseIP`. If it is nil, loopback, unspecified, or `To4() == nil`, return `fmt.Errorf("detected %q is not a usable LAN IPv4 address; set --serverIP to the address clients reach booty on", ip)`.
- [ ] **Step 4:** Port `DetectServerIP` and `CloseQuietly` into `pkg/config/config.go` and add the `net` import (`io` and `log/slog` are already imported). Add `TestDetectServerIPReturnsAnIPOrError` in `pkg/config/config_test.go`: call it, and if `err == nil` assert `net.ParseIP(ip) != nil`. Do not assert non-loopback: the function does not guarantee it, and `resolveServerIP` enforces it.
- [ ] **Step 5:** In `cmd/main.go`:
  - change the `serverIP` flag default from `"127.0.0.1"` to `""`, with help text `"IP address clients use to reach booty; detected from the default route when empty"`;
  - immediately before the `ServerHttpPort` resolution line (371), add:

```go
	serverIP, err := resolveServerIP(viper.GetString(config.ServerIP), config.DetectServerIP)
	if err != nil {
		return err
	}
	if viper.GetString(config.ServerIP) == "" {
		slog.Info("serverIP autodetected", "ip", serverIP)
	}
	viper.Set(config.ServerIP, serverIP)
```

  If `err` is already declared in `run` at that point, use `=` instead of `:=` for `err` and declare `serverIP` with `var serverIP string` first. The compiler tells you which.
- [ ] **Step 6:** Run `go build ./... && go vet ./... && go test -race ./cmd/ ./pkg/config/`. Expected: `ok`.
- [ ] **Step 7: Docs.**
  - `README.md:256`: the `--serverIP` row default becomes `detected` and its text `LAN IP clients use to reach booty; detected from the default route when empty (set it on multi-homed hosts)`.
  - `docs/CONFIGURATION.md:22`: same text.
  - Append to `docs/releases/UNRELEASED.md` `## Changes`: `--serverIP is detected from the default route when unset. Hosts or containers with no default route (for example --network none) now refuse to start until --serverIP is set; previously they started on 127.0.0.1, which no client can reach.`
- [ ] **Step 8:** Commit: `git add pkg/config cmd README.md docs && git commit -m "feat(cmd): detect --serverIP from the default route when unset"`.

### Phase exit check

Run: `go build ./... && go vet ./... && go test -race ./...`. Expected: all `ok`.
