# Phase 8: Wake-on-LAN (item 9) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Read `README.md` in this directory first.

**Goal:** An operator can wake a registered host from the API (`POST /api/v1/hosts/{mac}/wake`) and from a **Wake** button on the Hosts page.

**Architecture:** New stdlib-only package `pkg/power` (ported); a huma operation in `pkg/http/api_hosts.go` that receives a waker through `APIDeps`; a client function and a `Popconfirm` action in the web UI.

**Tech Stack:** Go stdlib; React + Ant Design (`Popconfirm`), Vitest.

**Spec:** design item 9.

## Global Constraints

See `README.md`. `pkg/power` must not import `pkg/hardware`: that import would drag in `pkg/db` and `pkg/config` (`research/upstream-port.md` §4).

## Review Focus

1. Waking a MAC that is not a registered host → 404, and no packet is sent. Task 2 test.
2. A malformed MAC in the path → 422, matching sibling host operations. Task 2 test.
3. A host with no recorded IP is still woken, through broadcasts only. Covered by the ported `Destinations` test.
4. Cancelling the request context stops the remaining rounds. Covered by the ported `TestWakeStopsWhenCancelled`.
5. The Wake button needs a confirmation before calling the API. Task 3 test.

---

### Task 1: Port `pkg/power`

**Files:** Create `pkg/power/wol.go`, `pkg/power/broadcast_unix.go`, `pkg/power/wol_test.go`.

**Interfaces:**
- Produces:
  - `var ErrInvalidMAC = errors.New("invalid MAC")`
  - `const WoLPort = 9`
  - `func MagicPacket(mac string) ([]byte, error)`
  - `type Waker struct { Port, Rounds int; Spacing time.Duration; Interfaces func() ([]net.Addr, error); Extra []string; Sleep func(context.Context, time.Duration) error }`
  - `func (w *Waker) Destinations(hostIP string) []string`
  - `func (w *Waker) Wake(ctx context.Context, mac, hostIP string) ([]string, error)`

- [ ] **Step 1:** `git show upstream/main:pkg/power/wol_test.go > pkg/power/wol_test.go`. Change nothing except adding `// Ported from jeefy/booty pkg/power/wol_test.go @ 6809c54 (MIT).` under the package clause. If the test refers to `hardware.ErrInvalidMAC`, replace that with `ErrInvalidMAC` and remove the `hardware` import.
- [ ] **Step 2:** Run `go test ./pkg/power/`. Expected: FAIL (no non-test files).
- [ ] **Step 3:** `git show upstream/main:pkg/power/wol.go > pkg/power/wol.go`, then edit:
  - Replace the package doc comment (lines 1-6) with: `// Package power sends Wake-on-LAN magic packets. Ported from jeefy/booty pkg/power/wol.go @ 6809c54 (MIT).`
  - Add `var ErrInvalidMAC = errors.New("invalid MAC")`.
  - Replace both `hardware.ErrInvalidMAC` references (upstream lines 31 and 34) with `ErrInvalidMAC`, and remove the `hardware` import.
  - Keep everything else, including the 6-byte length check.
- [ ] **Step 4:** `git show upstream/main:pkg/power/broadcast.go > pkg/power/broadcast_unix.go`. Add `//go:build unix` as line 1, then a blank line, before `package power`.
- [ ] **Step 5:** Run `go vet ./pkg/power/ && go test -race ./pkg/power/`. Expected: `ok`, with 4 tests passing.
- [ ] **Step 6:** Commit: `git add pkg/power && git commit -m "feat(power): Wake-on-LAN sender ported from upstream"`.

### Task 2: `POST /api/v1/hosts/{mac}/wake`

**Files:**
- Modify: `pkg/http/api.go:15-20` (the `APIDeps` struct)
- Modify: `pkg/http/api_hosts.go` (add the operation after `revoke-host`, around line 348)
- Modify: `cmd/main.go` (`bootyHTTP.APIDeps{...}`, line 459)
- Test: `pkg/http/api_hosts_test.go`

**Interfaces:**
- Consumes: `power.Waker` (Task 1).
- Produces: field `Waker interface { Wake(ctx context.Context, mac, hostIP string) ([]string, error) }` on `APIDeps`. A nil value makes the operation answer 503. The operation is `wake-host`.

- [ ] **Step 1: Write the failing tests** in `pkg/http/api_hosts_test.go`. Add a fake: `type fakeWaker struct{ calls []string; sent []string; err error }`, whose `Wake` appends `mac+"|"+hostIP` to `calls` and returns `(sent, err)`. Use `deps := hostsTestSetup(t)`, set `deps.Waker = fw`, then `api := newTestAPI(t, deps)`. Paths include the `/api/v1` prefix, as the existing tests do (`api_hosts_test.go:37`).

  | case | setup | request | expected |
  |---|---|---|---|
  | wakes known host | `hardware.WriteMacAddress("aa:bb:cc:dd:ee:b0", hardware.Host{MAC: "aa:bb:cc:dd:ee:b0", IP: "10.0.0.7"})`; `fw.sent = []string{"10.0.0.255:9"}` | `api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:b0/wake")` | 200; body JSON `{"sentTo":["10.0.0.255:9"]}`; `fw.calls == []string{"aa:bb:cc:dd:ee:b0|10.0.0.7"}` |
  | unknown host | none | `api.Post("/api/v1/hosts/aa:bb:cc:dd:ee:b1/wake")` | 404; `len(fw.calls) == 0` |
  | malformed MAC | none | `api.Post("/api/v1/hosts/zz/wake")` | 422 |
  | send failed | known host; `fw.err = errors.New("no destination accepted the packet")` | wake | 502 |
  | no waker | `deps.Waker = nil` | wake | 503 |

- [ ] **Step 2:** Run `go test ./pkg/http/ -run Wake`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Add `Waker` to `APIDeps` in `pkg/http/api.go`, with the comment `// Waker sends Wake-on-LAN packets; nil disables wake-host (503).`
  - In `api_hosts.go`, register:

```go
	huma.Register(api, huma.Operation{
		OperationID: "wake-host", Method: http.MethodPost, Path: "/hosts/{mac}/wake",
		Summary: "Send Wake-on-LAN packets to a host", Tags: []string{"hosts"},
	}, func(ctx context.Context, in *struct {
		MAC string `path:"mac"`
	}) (*struct{ Body struct{ SentTo []string `json:"sentTo"` } }, error) {
		...
	})
```

    The body, in order:
    - `deps.Waker == nil` → `huma.Error503ServiceUnavailable("wake-on-LAN is not available")`;
    - `ok, err := hardware.HasHost(in.MAC)`: `err != nil` → `huma.Error422UnprocessableEntity("invalid MAC", err)`; `!ok` → `huma.Error404NotFound("host not found")`;
    - `h, err := hardware.GetMacAddress(in.MAC)` (`err` → 500);
    - `sent, err := deps.Waker.Wake(ctx, h.MAC, h.IP)`; `err` → `huma.Error502BadGateway("wake failed", err)`;
    - return `SentTo: sent`.

    The registration goes inside `registerHosts(api huma.API, deps APIDeps)` (`pkg/http/api_hosts.go:88`), so `deps.Waker` is in scope.
  - In `cmd/main.go`'s `APIDeps{...}` literal, add `Waker: &power.Waker{}`. The zero value uses upstream defaults: port 9, 3 rounds, 1 s spacing, live interfaces.
- [ ] **Step 4:** Run `go vet ./... && go test -race ./pkg/http/ ./cmd/`. Expected: `ok`. `TestEveryRegisteredOperationIsGated` (`auth_test.go:178-226`) already substitutes `{mac}`, so it covers the new operation automatically.
- [ ] **Step 5:** Commit: `git add pkg/http cmd && git commit -m "feat(http): POST /api/v1/hosts/{mac}/wake sends Wake-on-LAN packets"`.

### Task 3: Wake button

**Files:** Modify `web/src/api/client.ts` (after `setMenuMode`, line 89); Modify `web/src/views/HostsView.tsx` (imports at lines 2 and 5; both Actions renderers, lines 118-134 and 141-153); Test `web/src/views/HostsView.test.tsx`, `web/src/api/client.test.ts`.

- [ ] **Step 1:** `cd web && npm ci`. Expected: completes, and `node_modules/` exists.
- [ ] **Step 2: Write the failing tests.**
  - `client.test.ts`: `wakeHost('aa:bb')` calls `fetch` with URL `/api/v1/hosts/aa:bb/wake` and method `POST`. Follow the existing pattern in that file (`vi.stubGlobal('fetch', ...)`, lines 7-40).
  - `HostsView.test.tsx`:
    - `it('wake asks for confirmation then calls wakeHost', ...)`:
      - `listHosts` returns `[host({ mac: 'w1', approved: true })]`;
      - `wakeHost` resolves `{ sentTo: ['10.0.0.255:9'] }`;
      - click the Approved tab, then the `Wake` button;
      - expect `client.wakeHost` not yet called;
      - click the button named `Wake now`;
      - expect `client.wakeHost` toHaveBeenCalledWith `'w1'`.
    - The same check for a pending host, without switching tabs.
- [ ] **Step 3:** Run `npx vitest run src/views/HostsView.test.tsx src/api/client.test.ts`. Expected: FAIL.
- [ ] **Step 4: Implement.**
  - `client.ts`: `export function wakeHost(mac: string): Promise<{ sentTo: string[] } | undefined> { return request(`/hosts/${mac}/wake`, { method: 'POST' }) }`
  - `HostsView.tsx`:
    - import `Popconfirm` from `antd` and `wakeHost` from `../api/client`;
    - in **both** Actions `<Space>` blocks, after the `Boot menu` button, add:

```tsx
          <Popconfirm
            title={`Send Wake-on-LAN to ${h.mac}?`}
            okText="Wake now"
            onConfirm={() => act(wakeHost, h.mac, `Wake-on-LAN sent to ${h.mac}`)}
          >
            <Button size="small">Wake</Button>
          </Popconfirm>
```

- [ ] **Step 5:** Run `npx tsc -b && npm test`. Expected: type check clean, and all tests pass.
- [ ] **Step 6:** Commit: `git add web/src && git commit -m "feat(web): Wake action with confirmation on the Hosts page"`.

### Task 4: Docs

- [ ] **Step 1:** In `docs/schema/API.md`, add `POST /api/v1/hosts/{mac}/wake` to the hosts operations: `Sends Wake-on-LAN magic packets (3 rounds, 1 s apart, UDP 9) to every local broadcast address, 255.255.255.255 and the host's last IP. 200 {sentTo}, 404 unknown host, 422 bad MAC, 502 nothing sent, 503 unavailable. The NIC/firmware must have WoL enabled and booty must share the L2 segment, or directed broadcast must be routed.`
- [ ] **Step 2:** README "What it does": add the bullet `**Wakes machines.** Send Wake-on-LAN from the Hosts page or the API.`
- [ ] **Step 3:** Commit: `git add docs README.md && git commit -m "docs: Wake-on-LAN"`.

### Phase exit check

`go build ./... && go vet ./... && go test -race ./...` and `cd web && npx tsc -b && npm test` → all pass.
