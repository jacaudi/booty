# Phase 2: TFTP hardening and pin/tftp v3 (items 8 and 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** TFTP runs on `github.com/pin/tftp/v3`, opens `dataDir` through `os.Root`, refuses writes, takes its port from `--tftpPort`, and reports errors to the caller instead of calling `os.Exit`.

**Architecture:** Replace package-level `absDataDir`/`safeJoin`/`StartTFTP` with a `tftp.Config`, a `tftp.Start` constructor and an unexported `server` struct whose method is the read handler. The phase 1 allowlist stays in front of every `dataDir` open.

**Tech Stack:** Go 1.26 `os.Root`; `github.com/pin/tftp/v3 v3.2.0`.

**Spec:** `docs/designs/2026-10-11-upstream-import-design.md` items 3 and 8.

## Global Constraints

See `README.md`. Keep `SetBlockSize(512)` and `EnableSinglePort()` exactly as today; do **not** add a block-size flag (design §8.2).

## Review Focus

1. A filename containing a NUL byte must be refused (design §9.4: `os.Root` returns EINVAL; we map it to not-found). Task 2 table.
2. An allowlisted name that is a directory (`public/sub`) must be refused, not streamed. Task 2 table.
3. A symlink inside `public/` pointing outside `dataDir` must be refused; an absolute symlink pointing inside must also be refused (verified `os.Root` behaviour). Task 2 table.
4. `--tftpPort` already in use must make `booty` exit with an error at startup, not run without TFTP. Task 3 test + Task 4 wiring.
5. A client WRQ gets a TFTP error, not a silent timeout. Task 3 test.

---

### Task 1: Switch to pin/tftp v3

**Files:**
- Modify: `go.mod` (remove both `replace` entries at lines 7-10 and the `github.com/pin/tftp v2.2.0+incompatible` require), `go.sum`
- Modify: `pkg/tftp/tftp.go:21` (import)

- [ ] **Step 1:** In `go.mod`, delete the whole `replace ( ... )` block. It holds the dead `container-linux-config-transpiler` entry and the `pin/tftp` pseudo-version pin. Delete the `github.com/pin/tftp v2.2.0+incompatible` require line.
- [ ] **Step 2:** In `pkg/tftp/tftp.go:21`, change `"github.com/pin/tftp"` to `"github.com/pin/tftp/v3"`. The package name stays `tftp`.
- [ ] **Step 3:** Run `go get github.com/pin/tftp/v3@v3.2.0 && go mod tidy`.
  Expected: `go.mod` now requires `github.com/pin/tftp/v3 v3.2.0`; no `replace` block.
- [ ] **Step 4:** Run `go build ./... && go test -race ./pkg/tftp/ ./cmd/`
  Expected: `ok` for both. Every call the fork makes keeps its signature in v3 (`research/go-deps.md` §1).
- [ ] **Step 5:** Commit: `git add go.mod go.sum pkg/tftp/tftp.go && git commit -m "build(deps): move to github.com/pin/tftp/v3 v3.2.0 and drop the dead replace block"`.

### Task 2: `openDataFile` on `os.Root`

**Files:**
- Create: `pkg/tftp/open.go`, `pkg/tftp/open_test.go`
- Modify: `pkg/tftp/tftp_test.go` (delete `TestSafeJoin`, lines 19-64)
- Modify: `pkg/tftp/tftp.go` (delete `absDataDir`, `errPathEscapes`, `safeJoin`: lines 25-31 and 56-82)

**Interfaces:**
- Consumes: `allowedDataFile(name string, topLevel []string) bool` (phase 1).
- Produces: `func openDataFile(root *os.Root, name string, topLevel []string) (*os.File, error)`. Every refusal returns an error for which `errors.Is(err, os.ErrNotExist)` is true.

- [ ] **Step 1: Write the failing test** in `pkg/tftp/open_test.go`. Build a temp tree: `dataDir/public/ok.txt` ("ok"), `dataDir/public/sub/` (directory), `dataDir/api-token` ("secret"), `dataDir/undionly.kpxe` ("bios"), `outside/secret.txt` next to `dataDir`. Add symlinks `dataDir/public/escape -> ../../outside/secret.txt`, `dataDir/public/abs -> <absolute path of dataDir/public/ok.txt>` and `dataDir/public/rel -> ok.txt`. Open `root, _ := os.OpenRoot(dataDir)` with `t.Cleanup(func(){ root.Close() })`. Use `top := []string{"undionly.kpxe"}`.

```go
	cases := []struct {
		name    string
		want    string // file content; "" means must be refused
	}{
		{"public/ok.txt", "ok"},
		{"public/rel", "ok"},
		{"undionly.kpxe", "bios"},
		{"public/sub", ""},
		{"public/escape", ""},
		{"public/abs", ""},
		{"api-token", ""},
		{"public/ok.txt\x00.ipxe", ""},
		{"/public/ok.txt", ""},
		{"public/missing", ""},
	}
	for _, c := range cases {
		f, err := openDataFile(root, c.name, top)
		if c.want == "" {
			if err == nil || !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%q: err = %v, want os.ErrNotExist", c.name, err)
			}
			if f != nil {
				f.Close()
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.name, err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		if string(b) != c.want {
			t.Errorf("%q: got %q, want %q", c.name, b, c.want)
		}
	}
```

- [ ] **Step 2:** Run `go test ./pkg/tftp/ -run TestOpenDataFile`. Expected: FAIL, `undefined: openDataFile`.
- [ ] **Step 3: Implement `openDataFile`** in `pkg/tftp/open.go`. Each of these returns `os.ErrNotExist` (wrap the cause with `fmt.Errorf("%w: %v", os.ErrNotExist, cause)` where a cause exists), in this order:
  - the name contains `"\x00"`;
  - the name starts with `/`;
  - `!allowedDataFile(name, topLevel)`;
  - `root.Open(name)` fails;
  - `f.Stat()` fails, or reports `IsDir()` (close `f` first).

  Otherwise return `f`.
- [ ] **Step 4:** Run `go test ./pkg/tftp/ -run TestOpenDataFile`. Expected: `ok`.
- [ ] **Step 5:** Delete `TestSafeJoin` from `pkg/tftp/tftp_test.go`, and `absDataDir`, `errPathEscapes` and `safeJoin` from `pkg/tftp/tftp.go`. Remove any import the deletion leaves unused (the compiler names them). The build breaks in `readHandler` and `StartTFTP` until Task 3; do not commit until Task 4.

### Task 3: `tftp.Config`, `tftp.Start`, server-owned read handler, nil write handler

**Files:**
- Modify: `pkg/tftp/tftp.go` (replace `readHandler`'s package-level dependencies, `writeHandler`, `StartTFTP`)
- Create: `pkg/tftp/server_test.go`

**Interfaces:**
- Consumes: `openDataFile` (Task 2), `topLevelBootFiles()` (phase 1).
- Produces:

```go
// Config is everything Start needs. Port is used as given; 0 binds an
// ephemeral port (tests). cmd always passes --tftpPort (default 69).
type Config struct {
	Port              int
	DataDir           string
	TopLevelBootFiles []string
	ARP               func(net.IP) (net.HardwareAddr, error) // nil means arping.Ping
}

// Server is a running TFTP server.
type Server struct { /* unexported: srv *tftp.Server, conn net.PacketConn, root *os.Root */ }

func Start(cfg Config, errCh chan<- error) (*Server, error)
func (s *Server) Addr() net.Addr // the bound local address
func (s *Server) Shutdown()      // stops serving, closes the socket and the root; idempotent
```

  Phase 6 adds a `BootFiles fs.FS` field to `Config`. Phase 7 adds the HTTP stub fields.

- [ ] **Step 1: Write the failing tests** in `pkg/tftp/server_test.go`, using the real v3 client against a server on an ephemeral loopback port:
  - Helper `startTestServer(t, dataDir string) *Server`: calls `Start(Config{Port: 0, DataDir: dataDir, TopLevelBootFiles: []string{"undionly.kpxe"}, ARP: func(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp in tests") }}, make(chan error, 1))`, then `t.Cleanup(s.Shutdown)`.
  - Set `viper.Reset(); t.Cleanup(viper.Reset); viper.Set(config.ServerIP, "127.0.0.1"); viper.Set(config.ServerHttpPort, 8080)` because `readHandler` still reads these.
  - `TestServerServesAllowlistedFile`: write `public/hello.txt` = `hello`; `c, _ := tftp.NewClient(s.Addr().String())`; `wt, err := c.Receive("public/hello.txt", "octet")`; `wt.WriteTo(&buf)`; assert `buf.String() == "hello"`.
  - `TestServerRefusesSecrets`: write `api-token`; `_, err := c.Receive("api-token", "octet")`; assert `err != nil`.
  - `TestServerRefusesWrites`: `_, err := c.Send("public/new.txt", "octet")`; assert `err != nil` and `strings.Contains(err.Error(), "does not support write requests")` (verified message: `pin/tftp/v3@v3.2.0/server.go:428`); assert `public/new.txt` does not exist.
  - `TestStartReturnsBindError`: `l, _ := net.ListenUDP("udp", &net.UDPAddr{Port: 0})`, `defer l.Close()`, `port := l.LocalAddr().(*net.UDPAddr).Port`; `_, err := Start(Config{Port: port, DataDir: t.TempDir()}, make(chan error, 1))`; assert `err != nil`.
  - `TestShutdownIsIdempotent`: start, call `Shutdown()` twice, no panic.
- [ ] **Step 2:** Run `go test ./pkg/tftp/ -run 'TestServer|TestStart|TestShutdown'`. Expected: build failure (`undefined: Start`).
- [ ] **Step 3: Implement** in `pkg/tftp/tftp.go`:
  - `type server struct { root *os.Root; topLevel []string; arp func(net.IP) (net.HardwareAddr, error) }`.
  - Change `readHandler` into the method `(s *server) readHandler(filename string, rf io.ReaderFrom) error`. Replace `arping.Ping(raddr.IP)` with `s.arp(raddr.IP)`; its call shape changes from three return values to two. Replace the phase 1 allowlist check plus `safeJoin`/`os.Open` fall-through with:

```go
	file, err := openDataFile(s.root, filename, s.topLevel)
	if err != nil {
		slog.Warn("TFTP rejected", "client", raddr.String(), "requested", filename, "err", err)
		return os.ErrNotExist
	}
	defer file.Close()
```

  - Delete `writeHandler` (lines 429-433).
  - Replace `StartTFTP` with `Start(cfg Config, errCh chan<- error) (*Server, error)`, in this order:
    - `root, err := os.OpenRoot(cfg.DataDir)` (error → `fmt.Errorf("tftp: open dataDir: %w", err)`);
    - use `cfg.Port` as given (0 = ephemeral);
    - if `cfg.ARP` is nil, use `func(ip net.IP) (net.HardwareAddr, error) { hw, _, err := arping.Ping(ip); return hw, err }` (`arping.Ping` returns hardware address, duration, error);
    - `srv := tftp.NewServer(s.readHandler, nil)`; `srv.SetBlockSize(512)`; `srv.EnableSinglePort()`; `srv.SetTimeout(60 * time.Second)`;
    - `conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.Port})` (error → close root, return `fmt.Errorf("tftp: listen on UDP/%d: %w", cfg.Port, err)`);
    - `go func() { if err := srv.Serve(conn); err != nil { errCh <- fmt.Errorf("tftp: %w", err) } }()`;
    - `slog.Info("TFTP server started", "addr", conn.LocalAddr().String())`.
  - `(*Server).Shutdown`: guarded by `sync.Once`. It calls `srv.Shutdown()`, then `conn.Close()`, then `root.Close()`. In single-port mode v3's `Shutdown` only cancels and sets a read deadline (`server.go:327-339`), so closing `conn` afterwards is required.
- [ ] **Step 4:** Run `go vet ./pkg/tftp/ && go test -race ./pkg/tftp/`. Expected: `ok`.

### Task 4: Wire `--tftpPort` and `tftp.Start` into `cmd`

**Files:**
- Modify: `pkg/config/config.go` (const block lines 22-59; `LoadConfig` lines 74-112)
- Modify: `cmd/main.go` (`args` struct lines 33-71; `init()`; `run` line 456; the block at line 477; shutdown step lines 496-504)
- Modify: `cmd/shutdown.go` (comment on `stopWithTimeout`)

- [ ] **Step 1:** Add `TFTPPort = "tftpPort"` to the const block in `pkg/config/config.go`, and `viper.SetDefault(TFTPPort, 69)` in `LoadConfig` next to the proxyDHCP defaults (line 76).
- [ ] **Step 2:** In `cmd/main.go`, add field `tftpPort int` to `args`, and in `init()` before line 270:

```go
	flags.IntVar(&args.tftpPort, "tftpPort", 69, "UDP port the TFTP server listens on")
```

- [ ] **Step 3:** Replace `tftpServer := tftp.StartTFTP()` (line 456) with:

```go
	tftpErr := make(chan error, 1)
	tftpServer, err := tftp.Start(tftp.Config{
		Port:              viper.GetInt(config.TFTPPort),
		DataDir:           viper.GetString(config.DataDir),
		TopLevelBootFiles: tftp.TopLevelBootFiles(),
	}, tftpErr)
	if err != nil {
		return err
	}
```

  Then export the phase 1 helper: rename `topLevelBootFiles` to `TopLevelBootFiles` in `pkg/tftp/allow.go` and in its test.
- [ ] **Step 4:** Replace `<-ctx.Done()` (line 477) with:

```go
	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case runErr = <-tftpErr:
		slog.Error("TFTP server failed; shutting down", "err", runErr)
	}
```

  Delete the old `slog.Info("shutdown signal received")` line that followed. At the end of `run`, change `return nil` to `return runErr`.
- [ ] **Step 5:** Change the tftp shutdown step to call `tftpServer.Shutdown`. Keep the `stopWithTimeout` bound. Replace the comment above it (lines 496-499) and the comment on `stopWithTimeout` in `cmd/shutdown.go:29-36` with one sentence each: the bound remains as a safety net; pin/tftp v3's single-port `Shutdown` returns promptly and `Server.Shutdown` closes the socket itself.
- [ ] **Step 6:** Run `go build ./... && go vet ./... && go test -race ./cmd/ ./pkg/tftp/ ./pkg/config/`. Expected: `ok` for all three.
- [ ] **Step 7:** Commit:

```bash
git add go.mod go.sum pkg/tftp pkg/config cmd
git commit -m "fix(tftp): open dataDir through os.Root, refuse writes, add --tftpPort

Writes were answered with an invalid packet the client timed out on;
a nil write handler now returns 'server does not support write
requests'. Symlinks out of dataDir, absolute symlinks, NUL names and
directories are refused. Bind and serve errors reach run() instead of
os.Exit inside a goroutine."
```

### Task 5: Docs

- [ ] **Step 1:** In `docs/CONFIGURATION.md`, add a row for `--tftpPort` (default `69`, "UDP port the TFTP server listens on") to the flag table that holds `--httpPort`. Update the ports table (around line 637) to say port 69 is the default of `--tftpPort`.
- [ ] **Step 2:** Append to `docs/releases/UNRELEASED.md` under a new `## Changes` heading:
  - `TFTP refuses write requests with an error instead of an invalid reply.`
  - `TFTP no longer follows absolute symlinks, or symlinks leading out of --dataDir. booty itself creates none.`
  - `New flag --tftpPort (default 69).`
- [ ] **Step 3:** Commit: `git add docs && git commit -m "docs: --tftpPort and TFTP behaviour changes"`.

### Phase exit check

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: all `ok`.
