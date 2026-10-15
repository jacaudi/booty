# Design: a new machine appears in the UI, survives a restart, and is approved from the UI

**Issues:** closes [#89](https://github.com/jacaudi/booty/issues/89) and the first half of
[#78](https://github.com/jacaudi/booty/issues/78). The second half of #78, the boot-event log, moves
to its own issue (§9).
**Milestone:** M0. Exit test from `docs/roadmap.md`: *a new machine appears in the UI, survives a
restart, and is approved from the UI.*
**Status:** design, awaiting Gate 1.
**Related:** #158 (config routes serve an unapproved host its real config), #88 (client-asserted
`?mac=` identity, a separate design).

---

## 1. Why

A QEMU VM PXE-booted against `main` @ `4a9a931` on 2026-10-14. Here is what happened:

1. The VM chained into booty and sat in the holding loop ("Booty: this host is not yet approved.
   Waiting...", every 30 s).
2. The UI showed **Hosts 0** and "Nothing needs attention", and the Pending tab said "No data".
   `GET /api/v1/hosts` did return `"unknown":["52:54:00:12:34:58"]`, but no UI code reads that
   field, and `web/src/api/client.ts` has no create-host call.
3. The only way forward was `curl -X POST /api/v1/hosts`. Once the host existed, the UI's
   **Allow** button worked and the VM booted Flatcar from booty's cache to a shell.
4. The unknown list lives in memory (`pkg/hardware/mac.go:82-89`, cleared by `Load`), so a restart
   loses it. Any lookup miss adds to it, including a config route that then returns 500 (#89).

The maintainer's words, 2026-10-14: *"I would rather approve pending than add it manually."*

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. A machine's first boot attempt creates a durable *discovered* record: MAC, detected arch,
  source IP, first seen, last seen.
- G2. The record survives a restart.
- G3. The Hosts page's Pending tab lists discovered machines. **Approve** picks an OS (and
  optionally a config and roles) and turns the machine into an approved, assigned host. It boots
  that OS on its next holding-loop poll, with no `curl`.
- G4. Spoofed `?mac=` traffic cannot evict a real discovered machine and cannot grow the table
  without bound.
- G5. The Home dashboard counts and lists discovered machines, so "Nothing needs attention" is
  never shown while a machine is waiting.

**Non-goals**

- The boot-event log (#78 part 2), §9.
- Per-host arch on the assigned-boot path. The boot path keeps its hardcoded arch
  (`pkg/bootscript/tokens.go:252-262`, `pkg/http/preseed.go:139`); that is separate multi-arch work.
- Filling in `hosts.ip` and `hosts.booted` for registered hosts. Both stay empty today; this
  feature does not change that.
- Fixing #158. It is a separate M0 issue.
- Hostname entry in the Approve dialog. It is set later through `POST /api/v1/hosts` (an upsert).

**Anti-goals** (failure even if it "works")

- A discovered machine gets any config or boot script beyond the holding script before approval.
- A request to any route other than `/booty.ipxe` creates a discovered record.
- Spam from one source IP pushes a real machine out of the list.

## 3. Decisions

### Maintainer's decisions (2026-10-14)

- **M1. Approve from Pending, not add by hand.** Discovered machines are approved in place.
- **M2. Spam defence: a per-source-IP limit on new records, plus refuse-when-full.** Each source IP
  may create at most a few new discovered records per window. The table holds at most 1024 rows.
  When it is full, new MACs are refused and existing rows are never evicted. Junk is cleared by
  the operator (Dismiss). The ARP-verified alternative was considered and rejected: it breaks
  behind NAT and in the QEMU user-network lab, where every request comes from 127.0.0.1.
- **M3. Persist discovered hosts in the database** (`docs/roadmap.md` M0, #89).

### Calls made in this design, with evidence

- **D1. A separate `discovered_hosts` table, not an unapproved `hosts` row.** The config routes
  gate only on a `hosts` row existing, not on approval: `pkg/http/ignition.go:169`,
  `pkg/http/machineconfig.go` (`serveBoundMachineConfig` / `serveFileMachineConfig`),
  `pkg/http/preseed.go:145` (#158). A spoofed MAC that became a `hosts` row would be served real
  config, including `{{ .JoinString }}`. A row in `discovered_hosts` grants nothing:
  `GetMacAddress` misses, every route behaves as for an unknown host, and `/booty.ipxe` serves
  the holding script (`bootDispatch`, `pkg/bootscript/tokens.go:48-52`).
- **D2. Only `GET /booty.ipxe` records.** That is the handler every boot attempt reaches (the TFTP
  stub chains to it, `pkg/bootscript/render.go:38`, and so does the holding loop,
  `pkg/bootscript/pxe_config.go:35`). The config routes, `/menu/`, TFTP menu selection
  (`pkg/tftp/tftp.go:43`) and every API handler stop recording. This closes #89's "a 500 still
  poisons the list".
- **D3. The in-memory list is deleted, not kept alongside the table.** `unknownHosts`,
  `unknownOrder`, `maxUnknownHosts`, `trackUnknown`, `clearUnknown`, `ListUnknownHosts` and the
  `BootyData.UnknownHosts` field go from `pkg/hardware/mac.go`. `GetMacAddress` loses its side
  effect, and `Load` no longer clears anything. This removes package-level mutable state
  (CLAUDE.md: "No new package-level mutable state"; #93), and the comments in
  `pkg/http/api_hosts.go` that explain choosing `HasHost` over `GetMacAddress` to avoid poisoning
  become obsolete and are removed or reworded.
- **D4. Discovery code lives in `pkg/db` (storage) and `pkg/http` (policy), not `pkg/hardware`.**
  `pkg/hardware` reads a package-level injected store (#93 debt). New code receives `*db.Store`
  explicitly: `bootScriptHandler` gains a `store *db.Store` parameter, and the per-IP limiter
  lives in the handler's closure. The node-report receiver does the same
  (`pkg/http/nodereport.go:122-132`).
- **D5. Arch comes from iPXE's `${buildarch}`.** Every chain line to `/booty.ipxe` becomes
  `/booty.ipxe?mac=${mac}&arch=${buildarch}`. iPXE documents the values `i386`, `x86_64`, `arm32`
  and `arm64`, as "the CPU architecture for which this version of iPXE was compiled"
  (https://ipxe.org/cfg/buildarch, fetched 2026-10-14). booty maps them through a fixed allowlist:
  `x86_64` → `amd64`, `i386` → `amd64`, `arm64` → `arm64`. Anything else, including absent, maps
  to `""` (unknown), so no client string is ever stored verbatim. `i386` maps to `amd64` because
  BIOS iPXE (`undionly.kpxe`) is an i386 build that runs on 64-bit x86 hardware, and booty boots
  only `amd64` and `arm64` OSes.
- **D6. One source for the chain URL.** The `/booty.ipxe?mac=${mac}` string is written out in six
  places: `pkg/bootscript/render.go:38`, `pkg/bootscript/pxe_config.go:35`,
  `pkg/bootscript/tool_scripts.go:70,118,150` and `pkg/bootscript/menu.go:202`. It is an external
  contract (DRY tier A), so it becomes one constant all six use, with `&arch=${buildarch}` added
  there once. `pkg/http` reads the `arch` query key from a constant in the same package.
- **D7. Spam-defence numbers (implementing M2).**
  - Table cap: 1024 rows, the same number as today's `maxUnknownHosts`.
  - Per-IP limit: **8 new records per source IP per 10 minutes**. One IP is normally one machine;
    8 leaves room for several VMs behind one NAT address.
  - Limiter capacity: 4096 source IPs, matching `limiterCapacity` in `pkg/http/nodereport.go:24`.
  - The limit applies only to creating a new row. Refreshing an existing row (last seen, IP, arch)
    is never limited, so a real machine's holding loop always keeps its row current. The limiter
    reuses `nodereport.MustNewLimiter` (`pkg/nodereport/limit.go:32`), which already bounds its
    own key count by LRU.
  - A refusal (limit or cap) still serves the holding script and logs at Debug, as node-report
    rejections do, so a flood cannot fill the logs.
- **D8. Source IP is `r.RemoteAddr`.** No `X-Forwarded-For` trust, the same as the node-report
  receiver.
- **D9. Approving a discovered machine extends `POST /api/v1/hosts/{mac}/approve`.** A new route
  such as `/hosts/discovered/{mac}` was rejected: it would conflict with `/hosts/{mac}/approve`
  in `net/http`'s mux (a request to `/hosts/discovered/approve` matches both, and neither pattern
  is more specific). The approve body gains an optional `os`:
  - If a `hosts` row exists: today's behaviour, unchanged. `os` is ignored if sent.
  - Else, if a `discovered_hosts` row exists: `os` is required, and a missing or empty `os` is a
    422. It is validated the way create-host validates it (the `osFamily` bridge,
    `docs/schema/API.md:326`), and an unknown OS is a 422 `"unknown OS <os>"`. Optional
    `configId` / `roleIds` are validated against that OS before anything is written, as today.
    Then, in one transaction: the `hosts` row is created with `os`, `assigned_arch` = the detected
    arch, approved and assigned, and the `discovered_hosts` row is deleted. The config and roles
    are written as today. The response is the created host.
  - Else: 404 "host not found", as today.
- **D10. Dismiss extends `DELETE /api/v1/hosts/{mac}`.** If a `hosts` row exists it is deleted, as
  today. Otherwise, if a `discovered_hosts` row exists it is deleted (204). Otherwise 404.
- **D11. Creating a host clears its discovered row.** `POST /api/v1/hosts` (create or upsert)
  deletes any `discovered_hosts` row for that MAC in the same transaction, matching today's
  `clearUnknown` on write. A deleted host that boots again is rediscovered.
- **D12. API shape: `unknown` is replaced by `discovered`.** `GET /api/v1/hosts` returns
  `{"hosts":[…],"discovered":[{"mac","arch","ip","firstSeen","lastSeen"}]}`. `discovered` is always
  an array, never null, newest `lastSeen` first. The `unknown` field is removed rather than having
  its element type changed: a renamed field fails loudly in old clients, and a retyped one fails
  silently. The UI is the only in-repo consumer. This is a breaking API change, called out in the
  PR and in `docs/schema/API.md`.
- **D13. Invariant wording.** CLAUDE.md says "Boot-path endpoints are read-only". M3 makes
  `/booty.ipxe` write a bounded discovery record. The invariant is amended to: *"…are read-only,
  except that `/booty.ipxe` records a first-seen machine in `discovered_hosts` (per-IP limited,
  capped, grants nothing)."* The amendment ships in this change.
- **D14. "Allow" becomes "Approve".** The Hosts view's button and modal title say "Allow"
  (`web/src/views/HostsView.tsx:161,225`); Home and the API say "approve". Every label in that view
  becomes "Approve".

## 4. Data model

Migration `pkg/db/migrations/0011_discovered_hosts.sql`:

```sql
CREATE TABLE discovered_hosts (
    mac        TEXT PRIMARY KEY,
    arch       TEXT NOT NULL DEFAULT '' CHECK (arch IN ('', 'amd64', 'arm64')),
    ip         TEXT NOT NULL DEFAULT '',
    first_seen TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

- `mac` is the `hardware.NormalizeMAC` canonical form. Recording rejects anything `NormalizeMAC`
  rejects.
- `pkg/db/migrate_test.go`'s hard-coded `user_version` assertions move from 10 to 11 (CLAUDE.md
  migration convention).
- Store methods on `*db.Store` (names fixed here, so the plan and tests agree):
  - `TouchDiscoveredHost(ctx, mac, ip, arch string) (bool, error)` updates `last_seen` and `ip`,
    and `arch` only when the new value is non-empty. It reports whether a row existed.
  - `InsertDiscoveredHost(ctx, mac, ip, arch string, maxRows int) (bool, error)` inserts only while
    the row count is below `maxRows`, in one statement. It reports whether a row was inserted.
    Losing an insert race to the same MAC counts as inserted.
  - `ListDiscoveredHosts(ctx) ([]DiscoveredHost, error)` returns rows newest `last_seen` first.
  - `GetDiscoveredHost(ctx, mac) (DiscoveredHost, error)` returns `sql.ErrNoRows` when absent.
  - `DeleteDiscoveredHost(ctx, mac) (bool, error)` reports whether a row was deleted.
  - `PromoteDiscoveredHost(ctx, h Host) error` inserts the approved, assigned `hosts` row and
    deletes the `discovered_hosts` row in one transaction.
  - `UpsertHost` also deletes the MAC's `discovered_hosts` row inside its transaction (D11).

## 5. Boot-path flow (`GET /booty.ipxe`)

1. Resolve the MAC exactly as today (`?mac=`, else ARP on the source IP). `identifyHostWith` is
   changed to return the resolved canonical MAC alongside the host, so the handler does not
   resolve it twice.
2. If a `hosts` row exists: render as today and record nothing.
3. If no MAC was resolved: serve the holding script and record nothing.
4. Otherwise (MAC resolved, no `hosts` row):
   - Map `?arch=` through the D5 allowlist, and take the IP from `r.RemoteAddr`.
   - `TouchDiscoveredHost`. If the row existed, stop.
   - Else, if `limiter.Allow(sourceIP)` is false, Debug-log "discovery refused: rate limited" and
     stop.
   - Else `InsertDiscoveredHost(..., 1024)`. If nothing was inserted (table full), Debug-log
     "discovery refused: table full".
   - A database error is logged at Warn and never changes the response.
5. Serve the holding script, as today.

The response body is byte-identical to today's for every case.

## 6. UI

- **Hosts → Pending tab** has two sections:
  1. **"New machines"**: one row per `discovered` entry, with columns MAC, Arch (`amd64`,
     `arm64`, or `—`), IP, First seen, Last seen, and the actions **Approve** and **Dismiss**.
     Dismiss asks for confirmation (AntD `Popconfirm`), then calls `DELETE /api/v1/hosts/{mac}`.
     The section is hidden when empty.
  2. The existing table of registered, unapproved hosts, unchanged except for D14.
  The Pending badge counts both.
- **Approve dialog for a discovered machine**: the existing modal with a required **OS** select
  first, then Config and Roles. OS options come from `GET /api/v1/os`. The Config list is
  filtered by the selected OS's family, using the same `familyAllowsKind` rule the view already
  applies to a host's OS (`web/src/views/HostsView.tsx:109`). Submitting calls
  `approveHostWith(mac, { os, configId, roleIds })`.
- **Home**: the Hosts card's "pending approval" count and the Needs-attention list include
  discovered machines. A discovered entry in Needs-attention shows the MAC and arch, and its
  **Approve** links to `/hosts`, because approving needs an OS choice. Registered pending hosts
  keep their inline Approve.
- Labels: no emoji; AntD components and tokens (project UI rules).

## 7. Testing

**Every test is written first and seen to fail on the unfixed tree.**

- **`pkg/db`:** migration to version 11. Touch/insert/list/get/delete semantics. The insert cap
  refuses row 1025 and evicts nothing. `PromoteDiscoveredHost` is atomic. `UpsertHost` clears the
  discovered row. A **restart test**: insert, close the store, reopen the same file, and the row
  is still listed.
- **`pkg/http` boot path:**
  - An unknown `?mac=` creates a row with the mapped arch and the source IP.
  - A second request updates `last_seen` and is not rate limited.
  - The 9th new MAC from one IP within 10 minutes is refused, and a different IP is still
    allowed.
  - A full table refuses a new MAC, keeps every existing row, and still serves the holding script.
  - `arch=sparc` and an absent `arch` store `""`.
  - The body is unchanged versus today.
- **`pkg/http` non-recording routes:** `/ignition.json`, `/machineconfig`, `/preseed`,
  `/menu/...`, and an API approve or delete for an unknown MAC create no row. This includes an
  `/ignition.json` that returns 500 (the #89 reproduction).
- **`pkg/http` API:**
  - Approving a discovered machine: missing `os` → 422, unknown `os` → 422, a valid `os` creates
    an approved, assigned host with `assigned_arch` set and removes the discovered row, and a bad
    `configId` leaves both tables untouched.
  - Dismiss: discovered → 204, absent → 404.
  - `GET /api/v1/hosts` returns `discovered` as an array (empty `[]`) and has no `unknown` key.
- **`pkg/bootscript`:** every chain line contains `&arch=${buildarch}` (one assertion over all six
  sources, through the shared constant).
- **Route tables:** `TestOnlyAllowlistedRoutesAnswerWithoutACredential` stays green. No route is
  added, and `/booty.ipxe` stays `mutates: false` in the open-route table, whose `mutates` flag
  means "changes server-side state on the caller's behalf". This is the one behaviour a builder
  must not reinterpret: the discovery record is a bounded side record, not a client-directed
  mutation. The `why` text for `GET /booty.ipxe` is updated to mention it.
- **Web (Vitest):** the Pending tab renders "New machines" from `discovered`. Approve requires an
  OS and posts `{os, configId, roleIds}`. Dismiss confirms and then calls DELETE. The Home count
  includes discovered machines. "Allow" appears nowhere in the Hosts view.
- **Acceptance (the M0 exit test, run before the PR):** the QEMU user-network lab. booty runs on
  the host with `--serverIP 10.0.2.2 --tftpPort 16969 --httpPort 18080`, and the BIOS guest boots
  `-netdev user,tftp=<stub dir>,bootfile=booty.ipxe` with the stub fetched from booty's TFTP. Steps
  and pass criteria:
  1. The VM appears under Pending → New machines with arch `amd64`.
  2. Restart booty: the row is still there.
  3. Approve it in the UI with OS `flatcar`.
  4. The VM boots Flatcar to a shell.
  Capture screenshots for steps 1, 2 and 4.

## 8. Documentation changes (in this change)

- `docs/schema/API.md`:
  - The `GET /api/v1/hosts` row: `discovered` replaces `unknown` (breaking).
  - The approve row: the `os` field and the discovered-machine path.
  - The delete row: dismiss.
  - The migration table at lines 44-45.
- `docs/schema/DATABASE.md`: the `discovered_hosts` table, replacing the "unknown hosts" section.
- `.claude/CLAUDE.md`:
  - The D13 invariant amendment.
  - "How a boot works" step 4 ("appear as unknown hosts until approved" becomes "are recorded as
    discovered machines and appear under Pending until approved").

## 9. Follow-ups (filed as issues, not done here)

- The boot-event log (#78 part 2): a new issue, M0 or later at the maintainer's choice. #78 is
  closed by this change with a comment pointing at it.
- #158 stays open and separate.
