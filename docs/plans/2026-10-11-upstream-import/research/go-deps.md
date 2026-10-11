# Go dependency bump research — booty fork (design doc items 1, 8, 12, 13)

Date: 2026-10-11. All module sources read from `/root/go/pkg/mod` (GOMODCACHE), downloaded via
`go mod download` in the scratch module `.../scratchpad/godeps/`. `/home/user/booty` was not modified.
Short names used below: `$M=/root/go/pkg/mod`, `$G=$M/golang.org/toolchain@v0.0.1-go1.26.2.linux-amd64/src`.

## 0. Headline: what was verified end-to-end

I copied the tree (no `.git`) to `scratchpad/godeps/booty-copy`, dropped both `replace`s, ran
`go get` for all seven targets, made **only two source edits**, then ran `go mod tidy`:

| file:line | old | new |
|---|---|---|
| `pkg/tftp/tftp.go:21` | `"github.com/pin/tftp"` | `"github.com/pin/tftp/v3"` |
| `pkg/http/ignition.go:15` | `coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"` | `coreOSType "github.com/coreos/ignition/v2/config/v3_4/types"` |

Result: `go build ./...` OK, `go vet ./...` OK, `go test -count=1 ./...` 11/11 packages ok (49 s).
No other compile-time API changes affect the fork. The go.mod also needs:
- remove the `replace` block (both entries), and remove `github.com/pin/tftp v2.2.0+incompatible`
  from `require`.

`go mod tidy` diff (copy vs current go.mod), for the record:
- direct: jsonparser 1.6.1, butane 0.29.0, ignition/v2 2.27.0, dhcp …234b97448fae, godotenv 1.5.1,
  `pin/tftp/v3 v3.2.0`, viper 1.21.0, `golang.org/x/sync` 0.20.0→0.22.0
- indirect added: `go-viper/mapstructure/v2 v2.4.0`, `sagikazarmark/locafero v0.11.0`,
  `sourcegraph/conc`, `pelletier/go-toml/v2 v2.3.0`, `go.yaml.in/yaml/v3 v3.0.4`,
  `aws/aws-sdk-go-v2 v1.43.4` (replaces `aws/aws-sdk-go v1.47.9`, via ignition)
- indirect removed: `mitchellh/mapstructure`, `hashicorp/hcl`, `magiconair/properties`,
  `pelletier/go-toml` (v1), `spf13/jwalterweatherman`, `gopkg.in/ini.v1`
- indirect bumped: go-systemd/v22 22.7.0, mdlayher/socket 0.6.0, afero 1.15.0, cast 1.10.0,
  gotenv 1.6.0, x/crypto 0.54.0, x/net 0.57.0, x/sys 0.47.0, x/text 0.40.0,
  pmezard/go-difflib pseudo-version.

## 1. github.com/pin/tftp → github.com/pin/tftp/v3 v3.2.0

**Fork usage** (grep): only `pkg/tftp/tftp.go`
- `:21` import; `:110` `rf.(tftp.OutgoingTransfer).RemoteAddr()`; `:111` `rf.(tftp.RequestPacketInfo).LocalIP()`;
  `:430-433` `writeHandler` returns nil; `:438` `StartTFTP() *tftp.Server`; `:447` `tftp.NewServer(readHandler, writeHandler)`;
  `:448` `SetBlockSize(512)`; `:449` `EnableSinglePort()`; `:450` `SetTimeout(60s)`; `:452` `ListenAndServe(":69")`.
- `cmd/main.go:456` `tftp.StartTFTP()`; `cmd/main.go:496-503` wraps `tftpServer.Shutdown` in `stopWithTimeout`
  (`cmd/shutdown.go:34-40` comment explains why).

Old = `$M/github.com/pin/tftp@v0.0.0-20210809155059-0161c5dd2e96` (module path `github.com/pin/tftp`, go.mod line 1).
New = `$M/github.com/pin/tftp/v3@v3.2.0` (module path `github.com/pin/tftp/v3`, go 1.23.0, requires x/net v0.43.0).

| API | v3.2.0 source | Change vs old |
|---|---|---|
| `NewServer(readHandler func(string, io.ReaderFrom) error, writeHandler func(string, io.WriterTo) error) *Server` | server.go:19-33 | Same signature. |
| `RequestPacketInfo{ LocalIP() net.IP }` | server.go:39-44 | Same (old only had a doc typo). |
| `OutgoingTransfer{ SetSize(int64); RemoteAddr() net.UDPAddr }` | sender.go:16-31; impl sender.go:55-56 (`RemoteAddr`, `LocalIP`) | Same. `IncomingTransfer` receiver.go:16-23. |
| `SetBlockSize(i int)` | server.go:153-159 | Same: **only applies if `512 < i < 65465`**; `SetBlockSize(512)` is a no-op (both versions). |
| `SetBlockSizeNegotiation(bool)` | server.go:168-172 | NEW. Default `smartBlock: true` (server.go:25) = old MTU-clamping behaviour. |
| `EnableSinglePort()` | server.go:123-131 | Same signature; no longer starts the internal GC goroutine. |
| `SetTimeout(d)` | server.go:136-144 | Same (`<=0` → default 5 s, backoff.go:9). |
| `SetHook(Hook)`, `Hook{OnSuccess(TransferStats); OnFailure(TransferStats, error)}` | server.go:84-88, 112-116 | Same. |
| `SetRetries`, `SetBackoff`, `SetAnticipate` | server.go:177-193, 99-109 | Same. |
| `ListenAndServe(addr string) error` | server.go:197-207 | Same (ResolveUDPAddr+ListenUDP, then `Serve`). |
| `Serve(conn net.PacketConn) error` | server.go:213-267 | Available in both. Use `net.ListenPacket("udp","127.0.0.1:0")` + `go s.Serve(conn)` for ephemeral-port tests (verified, below). v3 no longer `defer conn.Close()` at entry; single-port path closes on shutdown (single_port.go:33). |
| `Shutdown()` | server.go:327-348 | **Changed.** Single-port: `cancelFn()`, `conn.SetReadDeadline(time.Now())`, **returns immediately** (does not wait). The serve loop then sees the deadline, keeps 1 s read deadlines while transfers are active, and when none are active closes the conn, `wg.Wait()`s, and `Serve` returns nil (single_port.go:8-49). Non-single-port: closes conn, cancels, `wg.Wait()`. Old version: `s.quit <- q; <-q` (old server.go:305-313) — blocks until the serve loop reads `quit`, which in single-port mode is stuck in `ReadFrom`. |

**Nil handler behaviour** (server.go:419-429 WRQ, 475-485 RRQ): nil write handler →
`wt.abort(fmt.Errorf("server does not support write requests"))`; `receiver.abort` (receiver.go:257-276)
sends `packERROR(r.send, 1, err.Error())` (packet.go:110-116) = TFTP ERROR opcode 5, **error code 1**
("File not found" code), message `server does not support write requests`. Same code path in old version
(old server.go:371-379, old receiver.go:257-272).

**Empirical (scratch `godeps/tftpv3`, go1.26.2, single-port, 127.0.0.1:0, v3 client):**
- nil write handler: raw WRQ reply bytes `00 05 00 01 "server does not support write requests" 00`;
  v3 client `Send` error: `sending block 0: code=1, error: server does not support write requests`.
- fork's current handler (returns nil without calling `WriteTo`): `receiver.terminate()` (receiver.go:217-242)
  sends `r.send[:4]` which was never initialised → reply is **4 zero bytes (opcode 0, block 0)**, a malformed
  packet; v3 client `Send` fails with `i/o timeout`. So the design doc's "client is told it succeeded"
  (item 3) is inaccurate — the client receives a malformed packet and times out. Fix (nil handler) is still right.
- RRQ: `RemoteAddr()` = `127.0.0.1:<port>`, `LocalIP()` = `127.0.0.1` in single-port mode.
- v3 `Shutdown()` returned in ~10 µs; `Serve` returned `nil` ~0.1-0.25 ms later (idle server).
- Same program against the **old** pinned version (`godeps/tftpold`): RRQ and nil-handler WRQ identical, then
  `Shutdown()` **never returned** (killed by `timeout 40`). This confirms the hang that `cmd/shutdown.go:34-40`
  works around. With v3 `stopWithTimeout` is no longer needed for the hang, but harmless; to wait for drain,
  wait for `Serve`/`ListenAndServe` to return instead (it now actually returns).

**Required code changes:** import path only (`pkg/tftp/tftp.go:21`). Item 3: `NewServer(readHandler, nil)` and
delete `writeHandler` (`:429-433`). Item 8 notes: `--tftpBlockSize` default 512 maps to "don't cap" because
`SetBlockSize` ignores values `<=512`/`>=65465` silently — validate the flag yourself if you want an error.
For the error channel: `ListenAndServe` returns bind errors synchronously from `net.ListenUDP`; or listen first
with `net.ListenUDP` in `StartTFTP` (returning the error) and run `Serve(conn)` in the goroutine.

## 2. github.com/spf13/viper v1.10.1 → v1.21.0

**Fork usage** (non-test): `viper.BindPFlags` (cmd/main.go:274, return value ignored), `SetDefault`
(cmd/main.go:276,280; pkg/config/config.go:75-100,103), `Set` (cmd/main.go:278,282,371), `BindEnv(key, ENVNAME)`
(pkg/config/config.go:99,102,105,111), `GetString`/`GetBool`/`GetInt`/`GetInt64` (pkg/cache/reconciler.go:131)/
`GetDuration` (cmd/main.go:450). Tests also use `Get`, `Set(key, nil-able)` (cmd/main_test.go:60-61) and `Reset`.
**Not used anywhere:** `Unmarshal`, `ReadInConfig`, `SetConfigFile/Type`, `AutomaticEnv`, `SetEnvPrefix`,
`SetEnvKeyReplacer`, `IsSet`, `Sub`, direct `mapstructure`.

Signatures (old `viper@v1.10.1/viper.go` → new `viper@v1.21.0/viper.go`): `Get` 839→714, `GetString` 907→790,
`GetBool` 914→797, `GetInt` 921→804, `GetInt64` 935→818, `GetDuration` 977→874, `BindPFlags` 1088→1061,
`BindEnv` 1139→1111, `SetDefault` 1428→1480, `Set` 1447→1499, `Reset` 335→296. Only `interface{}`→`any`.

Behaviour:
- `(*Viper).Get` (new :716-763): identical logic to old (:841-886) plus a `[]time.Duration` case.
- `(*Viper).Set`, `(*Viper).BindEnv`, `(*Viper).BindFlagValues`: function bodies diff-identical (except `any`).
- `Reset()` (new :296-…): now also calls `resetRemote()`; drops `SupportedRemoteProviders`. No effect on fork.
- `find()` (new :1149): adds pflag types boolSlice/uintSlice/float64Slice/durationSlice/stringToInt; the
  AutomaticEnv branch now uses `envKey := strings.Join(append(v.parents, lcaseKey), ".")` (new :1226-1231) —
  only under `automaticEnvApplied`, which the fork never sets. Explicit `BindEnv` lookup (new :1237-1244) unchanged.
- Key case: `Get` still `strings.ToLower(key)` (new :717). Unchanged.
- Duration: `--cacheInterval` is a pflag `DurationVar` (cmd/main.go:94-95); `find` returns `flag.ValueString()`
  (e.g. `"5m0s"`) and `GetDuration` → `cast.ToDuration`. cast 1.4.1 `ToDurationE` (caste.go:55-78) vs cast 1.10.0
  (time.go:63-99): string case identical (`ParseDuration`, adding `"ns"` if no unit). Default `5*time.Minute`
  returned as `time.Duration`. No change.
- Ints: from int/int64 pflags (decimal `ValueString`) → `cast` `ParseInt(…, 0, 0)` both versions
  (cast 1.10.0 number.go:408). No change for fork inputs.
- Release notes fetched: v1.20.0 (github.com/spf13/viper/releases/tag/v1.20.0): breaking = "Drop encoding formats:
  HCL, Java properties, INI"; finder replaces file search; remote split to separate module; mapstructure v2.
  v1.21.0 (…/tag/v1.21.0): YAML lib → `go.yaml.in/yaml/v3`, config-type resolution fixes; nothing labelled breaking.
  UPGRADE.md in module (`viper@v1.21.0/UPGRADE.md:86` "BREAKING: mitchellh/mapstructure replaced", `:107`
  "BREAKING: HCL, Java properties, INI removed from core"). None affect the fork (no config files, no Unmarshal).
- Pre-existing quirk (not a change): `viper.BindPFlags`/`BindEnv` errors are ignored (`errcheck`-style).

**Required code changes: none.** Verified by build+tests on the bumped copy.

## 3. coreos/butane v0.19.0 → v0.29.0, coreos/ignition/v2 v2.17.0 → v2.27.0

**Fork usage:** `pkg/http/ignition.go:13-15` (imports), `:128-148` hand-built reboot config using
`coreOSType.Config`/`coreOSType.Unit` with `Ignition.Version = "3.4.0"`, `:153` `butaneConfig.TranslateBytes(…, butaneCommon.TranslateBytesOptions{Pretty: true})`
and `report.Entries`/`IsFatal`/`String`; `pkg/http/render.go:10-11,97-102` same TranslateBytes call.

- `TranslateBytes(input []byte, options common.TranslateBytesOptions) ([]byte, report.Report, error)`:
  butane@v0.29.0 config/config.go:136; function body diff-identical to v0.19.0 (:112).
  `TranslateBytesOptions{TranslateOptions; Pretty bool; Raw bool}` (common/common.go:23-27); struct diff-identical.
- butane v0.29.0 go.mod requires `ignition/v2 v2.26.0` (MVS picks 2.27.0 when the fork requires it).

**Variant → Ignition spec** (butane@v0.29.0 config/config.go:71-103; v0.19.0 :59-80):

| variant/version | v0.19.0 emits | v0.29.0 emits |
|---|---|---|
| fcos 1.0.0–1.5.0 | 3.0 / 3.1 / 3.2 / 3.2 / 3.3 / 3.4 | same |
| fcos 1.6.0-experimental | 3.5.0-experimental | **removed** ("No translator exists…") |
| fcos 1.6.0 / 1.7.0 | n/a | 3.5.0 / 3.6.0 |
| fcos 1.8.0-experimental | n/a | 3.7.0-experimental |
| flatcar 1.0.0 / 1.1.0 | 3.3.0 / 3.4.0 | same |
| flatcar 1.2.0-experimental | **3.5.0-experimental** | **3.7.0-experimental** (flatcar/v1_2_exp/translate.go:21,48) |
| r4e 1.0/1.1/1.2-exp | 3.3/3.4/3.5-exp | 3.3/3.4/3.7-exp |
| fiot 1.0.0 / 1.1.0-exp | n/a | 3.4 / 3.7-exp |
| openshift 4.8–4.22, 4.23.0-exp | (4.8–4.15 range in 0.19) | registered |

Empirical (scratch `godeps/ign_old` vs `ign_new`, identical program): outputs are **byte-identical** for
the reboot config (old v3_5_experimental types vs new v3_4 types, both declare 3.4.0), for
`examples/config/ignition.yaml` (flatcar 1.1.0, templated values substituted), fcos 1.5.0, the relative-path
fatal report case (same error + same report), and `version: 0.0.0` (same "No translator…" error, empty report).
Differences: fcos 1.6.0-experimental now errors; fcos 1.6.0 now works (3.5.0); flatcar 1.2.0-experimental
now emits 3.7.0-experimental. So the design doc's "No emitted spec version changes" holds for stable
variants only; an operator config using `fcos 1.6.0-experimental` breaks and `flatcar 1.2.0-experimental`
changes spec version.

**Ignition v2.27.0 `config/` packages** (`ls`): `v3_0 v3_1 v3_2 v3_3 v3_4 v3_5 v3_6 v3_7_experimental`
(+ shared, translate, util, validate, merge, doc). So: `v3_4/types` YES, `v3_5/types` YES,
`v3_5_experimental` NO, **`v3_6_experimental` NO — 3.6 is stable `v3_6`; the experimental one is
`v3_7_experimental`** (design doc item 12 says "v3_5 stable and v3_6_experimental" — wrong). MaxVersion:
v3_4 = 3.4.0, v3_5 = 3.5.0, v3_6 = 3.6.0, v3_7_experimental = 3.7.0-experimental (each `types/config.go:27`).
v2.17.0 had `v3_0…v3_4, v3_5_experimental`. `v3_4/types.Unit` (schema.go:249-255) is field-identical to
v2.17 `v3_5_experimental/types.Unit`.

**Parsing an arbitrary 3.x config** — `github.com/coreos/ignition/v2/config.Parse` (v2.27.0 config/config.go:32-50):
`func Parse(raw []byte) (types_exp.Config, report.Report, error)` where `types_exp` = `v3_7_experimental/types`.
It calls `v3_7_experimental.ParseCompatibleVersion` (v3_7_experimental/config.go:64-78), which recursively
parses older versions and **translates up to 3.7.0-experimental**; then (NEW in 2.27 vs 2.17's one-liner)
**if JSON parsing fails it tries Butane YAML transpilation** via the bundled `ignition/v2/butane/config`.
Empirical (`godeps/ign_new/parse`):
- `3.0.0`, `3.4.0`, `3.5.0`, `3.6.0`, `3.7.0-experimental` JSON → ok, returned version `3.7.0-experimental`.
- `3.5.0-experimental`, `2.3.0` → `unsupported config version`.
- invalid content (`path` not absolute) → `config is not valid` + report `error at $.storage.files.0.path … path not absolute`.
- `variant: fcos\nversion: 1.5.0` (Butane YAML!) → **accepted**, err nil.
- `not json` → `config is not valid`; `` → `not a config (empty)`.
- Alternative `v3_6.ParseCompatibleVersion` (v3_6/config.go:64): accepts 3.0–3.6, rejects 3.7.0-experimental
  and Butane YAML (`config is not valid`), returns 3.6.0 types. Use this if "valid Ignition JSON, stable spec
  only" is the intent; use `config.Parse` only if accepting Butane YAML and experimental is acceptable.
  Neither preserves the original version — parse with `util.GetConfigVersion` if you need it.

**Required code change:** `pkg/http/ignition.go:15` import → `config/v3_4/types` (verified identical output).

## 4. github.com/insomniacslk/dhcp …20260603135910-a415979eb11e → …20260901064844-234b97448fae

`diff -r` of the two module trees: the `dhcpv4` package (excluding `nclient4`) and `iana` are **identical**.
Changed files: `dhcpv4/nclient4/conn_unix.go` (drop frames whose IP payload < UDP header; bounds-check
`dhcpLen`), new `nclient4/conn_unix_test.go`, `dhcpv6/{dhcpv6message.go,modifiers.go,options.go}` + new
`option_preference.go`, CI/README, and go.mod (`go 1.23.0`+`toolchain go1.24.2` → `go 1.25.0`;
x/net 0.38.0→0.55.0, x/sys 0.31.0→0.45.0).
So `ToBytes` (dhcpv4packet.go:476), option ordering, `NewReplyFromRequest` (:241), `ClientArch` (:635),
`UserClass` (:810) and `WithOptionCopied` (modifiers.go:47) are unchanged; **`WithOptionCopied` already exists
in the old version** (old modifiers.go:47). Fork imports only `dhcpv4` and `iana` (pkg/proxydhcp/proxydhcp.go:18-19,
proxydhcp_test.go:8-9); it does not use nclient4/dhcpv6. **No code changes; no serialisation change.**
Golden-file parity in item 1 therefore does not depend on this bump (byte output is the same either way).

## 5. godotenv v1.4.0 → v1.5.1; jsonparser v1.1.1 → v1.6.1

**godotenv** — fork: `pkg/ostype/ignition.go:56` `godotenv.Parse(bytes.NewReader(body))` reading Flatcar
`version.txt`, key `FLATCAR_VERSION`. Signature same (`Parse(r io.Reader) (map[string]string, error)`, v1.5.1
godotenv.go:30) but implementation rewritten (now `UnmarshalBytes` with new parser.go). Empirical (`godeps/env_old`
vs `env_new`): a realistic version.txt, CRLF, `export`+comments all give identical `FLATCAR_VERSION`. Only error
**text** changed for malformed input (`Can't separate key from value` → `unexpected character "<" in variable name near …`).
No fork test asserts that text (grep). No code changes.

**jsonparser** — fork: `jsonparser.GetString` at `pkg/ostype/ignition.go:121,140,167,171`; only checks `err != nil`.
v1.6.1 parser.go:2041-2061: new `NullValueError` for JSON `null` (old: `Value is not a string: null`); otherwise
same. Empirical: found string, missing key (`Key path not found`), escaped string, object value — identical; null
differs only in error text. Empty key component no longer panics (CHANGELOG v1.3.0). No code changes.
Note (observation, not a defect): v1.1.1 was released 2021-01-08; v1.2.0…v1.6.1 were all published 2026-07-27…30
(proxy `Time` for v1.6.1: 2026-07-30T14:05:47Z; CHANGELOG.md:3-141), a large burst rewriting parser internals
(SWAR scanning etc.). go.mod has no dependencies. Worth a human glance at the upstream diff before adopting.

## 6. Go 1.26 `os.Root` semantics (go1.26.2, `$G/os`)

Toolchain: `go version` in /home/user/booty → `go1.26.2 linux/amd64` (GOROOT `$M/golang.org/toolchain@v0.0.1-go1.26.2.linux-amd64`).
System `go` is `/usr/local/go/bin/go` = **go1.24.7** with `GOTOOLCHAIN=auto`; go.mod's `toolchain go1.26.2`
triggers the auto-download. (A go1.26.0 toolchain is also cached; between 1.26.0 and 1.26.2 `root_unix.go` differs
only by `f.inRoot = true` at :107.) Empirical checks were re-run with `GOTOOLCHAIN=go1.26.2` (`godeps/osroot`, `osroot2`).

Docs: root.go:35-48 — "Methods on Root will follow symbolic links, but symbolic links may not reference a location
outside the root. Symbolic links must not be absolute." Does not stop bind mounts, /proc, device files (:45-46).
Errors from `Open` are `*PathError{Op:"openat"}` (root_unix.go:101-104).

| case | rule (source) | observed error |
|---|---|---|
| (a) symlink → outside root (relative `../secret`) | resolved via `splitPathInRoot` + `..` handling (root_openat.go:350-375, 307-321) | `openat rel_out: path escapes from parent` |
| (a') symlink with absolute target (outside **or inside** root) | `splitPathInRoot` rejects leading separator (root.go:301-307) | `path escapes from parent` — **even an absolute link pointing inside the root fails** (`abs_in`) |
| (b) relative symlink inside root (file, dir component, `../file` from subdir) | followed, max 8 links (root.go:73-78, root_openat.go:357-362) | ok; loop → `too many levels of symbolic links` (ELOOP) |
| (c) `..` | resolved by restarting from root after removing components (root_openat.go:307-327); error if more `..` than preceding parts (:318-321). `..` after a symlink applies to the link target (root_test.go "dotdot after symlink" → `b/target`) | `sub/../sub/file` ok; `sub/dir/../../sub/file` ok; `..`, `../secret`, `sub/../../secret` → `path escapes from parent`; `../ROOT/target` (re-entering root) also rejected (root_test.go:310-315) |
| (d) absolute path | root.go:305-306 | `openat /etc/passwd: path escapes from parent` (also `/sub/file`) |
| (e) directory via `Root.Open` | opening a dir succeeds (O_RDONLY) | `Open("sub")` ok, `Stat().IsDir()` true; `Read`/`io.Copy` → `read …/sub: is a directory` (EISDIR). Must check `IsDir()` yourself to return ErrNotExist. |
| (f) NUL byte | `unix.Openat` → `syscall.BytePtrFromString` (internal/syscall/unix/at.go:28-32; syscall/syscall.go:48-51) | `openat sub file: invalid argument` (EINVAL, errno 22) — note the error message contains the raw NUL |
| empty name | root.go:302-303 | `openat : empty path` |
| trailing slash on file | | `not a directory` (ENOTDIR) |
| missing | | ENOENT, `errors.Is(err, fs.ErrNotExist)` true |

Important: the escape error is `errPathEscapes = errors.New("path escapes from parent")` (os/file.go:421),
**unexported** and **not** `fs.ErrNotExist`/`fs.ErrPermission`; callers wanting "ErrNotExist to the client"
must map all non-nil open errors (or at least escape/EINVAL/ELOOP) themselves. Go's own tests cover the NUL
case (`root_test.go:1209-1211`, "nul byte") and escape cases (`root_test.go:242-350`).

## 7. Toolchain, build and tests on the unmodified tree

- `go version` (in repo): go1.26.2 linux/amd64 (system default go1.24.7, auto-switch).
- `go build ./...`: **exit 0**, 130 s wall (first run, included downloading ~60 modules into the cache).
- `go test -count=1 ./...`: **exit 0**, 61 s wall (first run; second run with `-json`: 41 s).
  11 packages `ok`, 0 FAIL, 1 `[no test files]` (`web`). Per-test (from `-json`): **719 top-level tests pass,
  166 subtests pass, 0 fail, 1 skip** (`pkg/cache TestLandArtifactLargeHashReadFailureIsAnErrorNotAVerdict`).
- node: `/opt/node22/bin/node` v22.22.0; npm 10.9.4. `web/node_modules` **does not exist** → `npm test` not run
  (per instructions, nothing installed). `web/package.json:11` `"test": "vitest run"`, vitest `^2.1.1`.
- Bumped copy (section 0): build/vet/test all green (tests 49 s).

## UNVERIFIED / caveats

- Viper: verified only the code paths the fork exercises (no config files, no Unmarshal, no AutomaticEnv). The
  release-note summaries came via WebFetch of the GitHub release pages; the UPGRADE.md lines were read directly.
- Old-version TFTP WRQ-with-handler-returning-nil packet (4 zero bytes): verified on v3 empirically and on the old
  version by source only (`receiver.terminate` code identical; the old empirical run hung in `Shutdown` before
  reaching that case).
- Real-network behaviour of `LocalIP()` with multiple interfaces / broadcast (server.go:357-359 maps
  255.255.255.255 to 0.0.0.0) was not tested — only loopback.
- The `.hex` golden parity for item 1 was not tested (upstream files not in this repo); only that dhcpv4 is
  byte-for-byte identical source between the two pins.
- ignition v2.27 release notes not fetched; behaviour of `config.Parse` (Butane fallback) is from source + test.
- `-race` was not run (design doc item 12 asks for `go test -race`).
