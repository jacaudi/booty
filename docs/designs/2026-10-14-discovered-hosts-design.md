# Design: a new machine appears in the UI, survives a restart, and is approved from the UI

**Issues:** closes [#89](https://github.com/jacaudi/booty/issues/89) and the first half of
[#78](https://github.com/jacaudi/booty/issues/78). The second half of #78, the boot-event log, is
[#159](https://github.com/jacaudi/booty/issues/159) (§10).
**Milestone:** M0. The exit test from `docs/roadmap.md`: *a new machine appears in the UI, survives a
restart, and is approved from the UI.*
**Status:** design, revised after Gate 1 and the builder check (§11).
**Related:** #158 (config routes serve an unapproved host its real config); #88 (client-asserted
`?mac=` identity, a separate design).

---

## 1. Why

What happened when a QEMU VM PXE-booted against `main` @ `4a9a931` on 2026-10-14:

1. The VM chained into booty and sat in the holding loop ("Booty: this host is not yet approved.
   Waiting..." every 30 s).
2. The UI said **Hosts 0** and "Nothing needs attention", and the Pending tab said "No data".
   `GET /api/v1/hosts` returned `"unknown":["52:54:00:12:34:58"]`, but no UI code reads that field,
   and `web/src/api/client.ts` has no create-host call.
3. The only way forward was `curl -X POST /api/v1/hosts`. After that, the UI's **Allow** button
   worked and the VM booted Flatcar from booty's cache to a shell.
4. The unknown list is in memory (`pkg/hardware/mac.go:82-89`), and `Load` clears it, so a restart
   loses it. Any lookup miss adds to it, including a config route that then returns 500 (#89).

The maintainer, 2026-10-14: *"I would rather approve pending than add it manually."*

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. A machine's first boot attempt creates a durable *discovered* record: MAC, detected arch,
  source IP, first seen and last seen.
- G2. The record survives a restart.
- G3. The Hosts page's Pending tab lists discovered machines. **Approve** picks an OS (plus,
  optionally, a schematic for Talos, a config and roles), and the machine becomes an approved,
  assigned host that boots that OS on its next holding-loop poll. No `curl`.
- G4. Spoofed `?mac=` traffic can neither evict a real discovered machine nor grow the table without
  bound. When the table is full, the UI says so.
- G5. The Home dashboard counts and lists discovered machines, so "Nothing needs attention" never
  shows while a machine is waiting.

**Non-goals**

- The boot-event log (#159).
- Per-host arch on the boot path. Debian's assigned path hardcodes `amd64`
  (`pkg/bootscript/tokens.go:252-262`). Flatcar, CoreOS and Talos read the per-deployment arch flags
  (`tokens.go:218,223,231`). That is separate multi-arch work. **The detected arch is recorded and
  shown, and nothing else uses it. `hosts.assigned_arch` stays unwritten.**
- Filling in `hosts.ip` and `hosts.booted` for registered hosts.
- Fixing #158, or the pre-existing exposure of templates to unknown hosts: `/machineconfig` renders
  a host-less config with `JoinString` (`pkg/http/machineconfig.go:115-138`), and `/preseed` serves
  its default file (`pkg/http/preseed.go:64-83`). These predate this design. This design adds no new
  exposure.
- Hostname entry in the Approve dialog.

**Anti-goals** (failure even if it "works")

- A discovered machine gets anything an unknown host does not already get today.
- A request to any route other than `GET /booty.ipxe` creates or changes a discovered record.
- Spam from one source IP pushes a real machine out of the list.
- Dismissing a discovered machine touches a `hosts` row.

## 3. Decisions

### Maintainer's decisions (2026-10-14)

- **M1. Approve from Pending, not add by hand.**
- **M2. Spam defence: a per-source-IP limit on new records, plus refuse-when-full.** The table holds
  at most 1024 rows. When it is full, new MACs are refused and existing rows are never evicted.
  Junk is cleared by the operator. The ARP-verified alternative was rejected: it breaks behind NAT
  and in the QEMU user-network lab, where every request arrives from 127.0.0.1.
- **M3. Persist discovered hosts in the database** (`docs/roadmap.md` M0, #89).

### Researched calls, with evidence

- **D1. A separate `discovered_hosts` table, not an unapproved `hosts` row.** The config routes check
  that a `hosts` row exists, not that it is approved:
  - `pkg/http/ignition.go:169`
  - `pkg/http/machineconfig.go:47-61`
  - `pkg/http/preseed.go:38-43`

  That is #158. If a spoofed MAC became a `hosts` row, it would get real config. A
  `discovered_hosts` row grants nothing: `GetMacAddress` misses, and every route behaves as it does
  for an unknown host.
- **D2. Only `GET /booty.ipxe` records.** Every boot attempt reaches it. The TFTP stub chains to it
  (`pkg/bootscript/render.go:38`), and so does the holding loop (`pkg/bootscript/pxe_config.go:35`).
  The config routes, `/menu/`, TFTP menu selection (`pkg/tftp/tftp.go:43`) and every API handler
  stop recording. This closes #89's "a 500 still poisons the list".
- **D3. The in-memory list is deleted.** `pkg/hardware/mac.go` loses `unknownHosts`, `unknownOrder`,
  `maxUnknownHosts`, `trackUnknown`, `clearUnknown`, `ListUnknownHosts`, `BootyData` and `GetData`.
  `GetData` and `BootyData` are used only by tests, so their tests go with them. `GetMacAddress`
  loses its side effect, and `Load` no longer clears anything. In `pkg/http/api_hosts.go`, the
  comments explaining `HasHost` over `GetMacAddress` lose their poisoning rationale. Keep `HasHost`
  (it avoids a full row fetch) and reword the comments to say only that.
  - **Tests.** Tests of the unknown list itself are deleted: `pkg/hardware/mac_test.go:121-135`
    (TracksMiss), `:365-385`, `:387-413`, `:415-435`. Tests that use `GetData` only to inspect
    stored hosts switch to `hardware.ListHosts` and keep their subject:
    `normalize_test.go:95,124,198` and `mac_test.go:277`.
  - **Stale comments to reword or delete:** `pkg/http/http.go:35-38`, `pkg/hardware/mac.go:23`,
    `:280-287`, `:309-313`, `pkg/http/api_hosts.go:30-33`, `:279-283` and `:488-492`.
- **D4. Storage goes in `pkg/db` and policy in `pkg/http`; nothing goes in `pkg/hardware`.**
  `pkg/hardware` reads a package-level injected store (#93 debt). New code receives `*db.Store`
  explicitly:
  - `bootScriptHandler` gains a `store *db.Store` parameter (`http.go:51` passes `deps.Store`). A
    nil store skips recording, so existing handler tests that build it without a store
    (`bootscript_test.go:87`) keep working.
  - The per-IP limiter is created once per handler and captured in its closure, as `wakeLimit` is
    (`pkg/http/api_hosts.go:110`).
  - A `HEAD /booty.ipxe` (which `GET /booty.ipxe` also matches) never records.
- **D5. The arch comes from iPXE's `${buildarch}` and passes an allowlist.**
  - iPXE documents its values as `i386`, `x86_64`, `arm32` and `arm64`: "the CPU architecture for
    which this version of iPXE was compiled" (https://ipxe.org/cfg/buildarch). Gate 1 also found it
    in the iPXE v2.0.0 source.
  - Mapping: `x86_64` → `amd64`, `i386` → `amd64`, `arm64` → `arm64`. Anything else, including an
    absent value, maps to `""`. No client string is stored verbatim, and the column's CHECK enforces
    this.
  - `i386` maps to `amd64` because BIOS iPXE (`undionly.kpxe`) is an i386 build that runs on 64-bit
    x86 hardware, and booty boots only `amd64` and `arm64` OSes.
  - iPXE splits commands only on whitespace, so a `&` inside a URL is safe (Gate 1 checked
    `src/core/exec.c:122-195`).
- **D6. One source for the chain URL.** `bootscript` exports two constants:
  - `ArchQueryKey = "arch"`.
  - `ChainPath = "/booty.ipxe?mac=${mac}&" + ArchQueryKey + "=${buildarch}"`.

  The six Go sources that spell the chain URL use `ChainPath`: `render.go:38`, `pxe_config.go:35`,
  `tool_scripts.go:70,118,150` and `menu.go:202`. `pkg/http` reads the query with
  `bootscript.ArchQueryKey`.
  **`boot/embed.ipxe:4` is deliberately left alone.** It is compiled into the embedded iPXE binaries,
  and changing it means rebuilding them with `boot/build-ipxe.sh`. It is also only the last-resort
  HTTP fallback, after two TFTP attempts. A machine that arrives that way is recorded with arch `""`,
  and a later poll that carries an arch fills it in (D7).
- **D7. The numbers behind M2, and refresh debouncing.**
  - **Cap:** 1024 rows, the same number as today's `maxUnknownHosts`.
  - **Per-IP limit:** at most 8 *new* records per source key per 10 minutes. One IP is normally one
    machine; 8 leaves room for several VMs behind one NAT address.
  - **The source key** is the host part of `net.SplitHostPort(r.RemoteAddr)`, as in
    `nodereport.go:146-149`.
    - An IPv4 address, or an IPv4-mapped IPv6 address, is keyed by the full IPv4 address.
    - Any other IPv6 address is keyed by its /64 prefix, so one host cannot evade the limit by
      cycling addresses inside its /64. The zone is dropped, so all link-local clients share one
      budget. That only matters with an IPv6 `--serverIP`, and is accepted.
    - If `netip.ParseAddr` fails on the host part, the raw host string is the key, matching
      `nodereport.go:146-149`.
  - **The `ip` column** stores `addr.Unmap().WithZone("").String()` for a parsed address, else the
    raw host string.
  - **The limiter** is `nodereport.MustNewLimiter(8, 10*time.Minute, 4096, time.Now)`. Its key
    count is bounded by LRU (`pkg/nodereport/limit.go:32`). 4096 matches `limiterCapacity`
    (`pkg/http/nodereport.go:24`).
  - **The limit applies only to creating a row.**
  - **A refresh** (updating `last_seen` and `ip`, and `arch` when the new value is non-empty) is
    *debounced*: it writes only when the stored `last_seen` is at least 20 s old. The holding loop
    sleeps 30 s (`pxe_config.go:34`), so a real machine refreshes on every poll. A flood against a
    known MAC costs one write per 20 s per MAC. Every DB call shares one connection
    (`pkg/db/db.go:32`), and an undebounced UPDATE measured 45 µs against 7.5 µs for a read.
  - **A refusal** (limit or cap) still serves the holding script and logs at Debug, matching the
    node-report rejections. This keeps refusals at the same level as other pre-auth rejections; it
    does not bound total log volume, which the existing per-request Info lines already set
    (`http.go:261`, `bootscript.go:16`).
- **D8. The approve flow reuses the existing endpoints. There is no promote endpoint.** Approving a
  discovered machine in the UI makes three calls in order:
  1. `POST /api/v1/hosts {"mac","os"}`. This creates an unapproved `hosts` row and, by D10, deletes
     the `discovered_hosts` row.
  2. Only for `os=talos` with a schematic chosen: `POST /api/v1/hosts/{mac}/schematic`, exactly as
     today's Allow flow does (`web/src/views/HostsView.tsx:91-96`).
  3. `POST /api/v1/hosts/{mac}/approve {configId?, roleIds?}`, unchanged.

  Today's dialog already does steps 2 and 3. Step 1 is the only new call.
  - **If step 2 or 3 fails,** the UI shows the error and calls `DELETE /api/v1/hosts/{mac}` to undo
    step 1, so the machine is not left as a registered unapproved host (#158 exposure). Its next
    holding-loop poll (≤ 30 s) rediscovers it (D10). If that DELETE also fails, both errors are
    shown, and the host stays in the registered Pending table, where the normal Approve retries
    it. The view reloads in a `finally`, whatever the outcome.
  - **Rejected alternative:** a single atomic promote endpoint. It needs a new transaction, its own
    409 and 404 handling, and a second copy of the schematic, config and role validation
    (Gate 1, S6). The three-call flow uses code that is already tested.
  - **Cost:** between steps 1 and 3 (milliseconds on success; until the rollback DELETE on failure),
    the machine is a registered unapproved host and so falls under #158. #158 is a separate M0
    issue at priority/high, and fixing it closes that window.
- **D9. Boot vocabulary and an assignable-OS guard (Gate 1 B1).**
  - **What is broken today:** `GET /api/v1/os` lists canonical names (12 OSes, including
    `fedora-coreos` and 8 tool OSes), and create-host accepts all of them (`osFamily`). Gate 1
    rendered each one through `RenderBootScript`. `fedora-coreos` gave an empty script, and the 8
    tool OSes gave the holding loop while the UI showed the host as approved.
  - **Fix 1, create-host stores the boot-vocabulary name.** `pkg/cache` exports
    `CanonicalToCacheName` (rename the unexported `canonicalToCacheName`, `pkg/cache/layout.go:98`,
    and update its callers). `POST /api/v1/hosts` stores `cache.CanonicalToCacheName(os)`, so
    `fedora-coreos` is stored as `coreos`. Validation is unchanged. The rename touches 9 non-test
    sites in 7 files (`list.go:141`, `scan.go:36`, `debiandvd.go:331,472`, `migrate.go:105`,
    `evict.go:55,73`, `verify.go:424`, `reconcile.go:170`), the tests `evict_test.go:55` and
    `layout_test.go:40-41,48`, and the comment at `layout.go:96`.
    Migration 0011 also rewrites rows stored before this fix:
    `UPDATE hosts SET os='coreos' WHERE os='fedora-coreos'` and the same for `assigned_os`.
    `api_hosts_test.go:511`, which expects a 201 for `fedora-coreos`, still holds, and gains an
    assertion that the stored value is `coreos`.
  - **Fix 2, approve refuses an OS that assignment cannot boot.** `bootscript` exports
    `Assignable(os string) bool`. It returns true exactly when `PXEConfig[os+".ipxe"]` exists,
    `os != "holding"` and `!isToolOS(os)`. Today that is `talos`, `flatcar`, `coreos` and `debian`.
    `POST /api/v1/hosts/{mac}/approve` returns 422 when the host's `os` is non-empty and not
    assignable: `"os <os> cannot be booted by assignment; use the boot menu"`. The check runs before
    anything is written. A host with an empty `os` approves as it does today.
  - **Fix 3, the UI offers only assignable OSes.** The Approve dialog's OS options are the
    `GET /api/v1/os` entries whose `family` is not `"tool"`, sent by canonical name. The server maps
    them under Fix 1 and guards them under Fix 2, so the server is the authority and the UI filter
    is only for presentation.
- **D10. Creating a host clears its discovered row.** `UpsertHost` (`pkg/db/host.go:75-94`) has no
  transaction today. It gains one: the `hosts` upsert and
  `DELETE FROM discovered_hosts WHERE mac = ?` run in a single `tx`, and every statement in it goes
  through `tx`, never `s.db`. The store has one connection, so a `s.db` call inside an open
  transaction deadlocks; Gate 1 reproduced this. A deleted host that boots again is rediscovered.
- **D11. Dismiss gets its own discovered-only routes (Gate 1 B2 and S5).**
  - `DELETE /api/v1/discovered/{mac}` returns 204 when it deletes a row, 404 when there is none and
    422 for a bad MAC.
  - `DELETE /api/v1/discovered` deletes every row and returns 200 with `{"deleted": <n>}` (a 204 cannot carry a body). That is
    bulk recovery after spam.
  - Both are registered by a new `registerDiscovered(api huma.API, deps APIDeps)` in a new file
    `pkg/http/api_discovered.go`, called from `RegisterAPI` next to the other `register*` calls.
  - Neither route can touch `hosts`. `/api/v1/discovered/...` overlaps no existing pattern (Gate 1
    registered the shapes on a stdlib mux). `DELETE /api/v1/hosts/{mac}` is unchanged. Both routes
    sit behind the auth gate, like every `/api/v1` operation.
- **D12. A discovered row is never created for a registered MAC.**
  - The insert carries `AND NOT EXISTS (SELECT 1 FROM hosts WHERE mac = ?)` (§4). Gate 1 verified
    the guard in SQLite.
  - The boot handler records only when the host lookup failed with `hardware.ErrNotFound`. Any other
    lookup error records nothing.
  - `ListDiscoveredHosts` also excludes MACs present in `hosts`.
- **D13. The API shape: `unknown` is replaced by `discovered` and `discoveredFull`.**
  - `GET /api/v1/hosts` returns
    `{"hosts":[…],"discovered":[{"mac","arch","ip","firstSeen","lastSeen"}],"discoveredFull":bool}`.
  - `discovered` is always an array, `[]` when empty and never null. It is sorted by `lastSeen`
    descending, then `mac` ascending.
  - `firstSeen` and `lastSeen` are RFC 3339 UTC strings (`2026-10-15T05:19:27Z`), converted from
    SQLite's `datetime('now')` form (`2026-10-15 05:19:27`, UTC).
  - `discoveredFull` is true when `CountDiscoveredHosts >= 1024`. The `?approved=` filter does not apply
    to `discovered`.
  - `unknown` is removed rather than retyped: a renamed field fails loudly in old clients, while a
    retyped one fails silently. Nothing in the repo reads `unknown` (the UI never did). This is a breaking API change,
    called out in the PR and in `docs/schema/API.md`.
- **D14. The invariant wording changes.** `.claude/CLAUDE.md`'s security invariants currently say
  "Boot-path endpoints (…) are read-only. The one open route that writes is `POST /node/report`
  (…)". That sentence becomes: *"Two open routes write: `POST /node/report` (node health,
  per-host HMAC token plus an ARP match), and `GET /booty.ipxe`, which records a machine it does
  not know in `discovered_hosts`. New rows are limited per source IP and capped at 1024 with no
  eviction, refreshes are debounced to one write per 20 s per MAC, and a row grants nothing."*
  In the last bullet, "(`/booty.ipxe`, `/menu/`, `/boot/` have neither credential nor rate
  limit)" becomes "(`/menu/` and `/boot/` have neither credential nor rate limit; `/booty.ipxe`
  limits only its discovery writes)". The change ships in this PR.
- **D15. The open-route table marks `/booty.ipxe` as mutating (Gate 1 S2).**
  - In `pkg/http/open_routes_test.go`, `"GET /booty.ipxe"` gets `mutates: true`, with a `why` that
    names the bounds in D14.
  - The expected list of mutating routes becomes `[]string{"/node/report", "GET /booty.ipxe"}`,
    sorted as the test sorts them (`slices.Sorted(maps.Keys(table))`).
  - The comments "Exactly one open route may set it" (`open_routes_test.go:30-31`) and "Exactly one
    allowlisted route may change state" (`:171`) both say "two" and name them.
- **D16. "Allow" becomes "Approve".** The Hosts view's button and modal title
  (`web/src/views/HostsView.tsx:161,225`) say "Allow", while Home and the API say "approve". Every
  label in that view becomes "Approve".

## 4. Data model

The migration is `pkg/db/migrations/0011_discovered_hosts.sql`:

```sql
CREATE TABLE discovered_hosts (
    mac        TEXT PRIMARY KEY,
    arch       TEXT NOT NULL DEFAULT '' CHECK (arch IN ('', 'amd64', 'arm64')),
    ip         TEXT NOT NULL DEFAULT '',
    first_seen TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

- `mac` is in `hardware.NormalizeMAC`'s canonical form. Recording skips anything `NormalizeMAC`
  rejects.
- The hard-coded `user_version` assertions in `pkg/db/migrate_test.go` change from 10 to 11.
- New code takes a `context.Context` first, as `pkg/db/health.go` does.

| Method on `*db.Store` | SQL (pinned) | Returns |
|---|---|---|
| `TouchDiscoveredHost(ctx, mac, ip, arch string) (bool, error)` | Existence: `SELECT 1 FROM discovered_hosts WHERE mac=?`. If present: `UPDATE discovered_hosts SET last_seen=datetime('now'), ip=?, arch=CASE WHEN ?='' THEN arch ELSE ? END WHERE mac=? AND last_seen <= datetime('now','-20 seconds')` | whether the row exists (true even when the debounce skipped the write) |
| `InsertDiscoveredHost(ctx, mac, ip, arch string, maxRows int) (bool, error)` | `INSERT INTO discovered_hosts (mac, ip, arch) SELECT ?, ?, ? WHERE (SELECT count(*) FROM discovered_hosts) < ? AND NOT EXISTS (SELECT 1 FROM hosts WHERE mac = ?) ON CONFLICT(mac) DO NOTHING` | `RowsAffected()==1`. A 0 means the table is full, the MAC is registered or a concurrent request inserted it. The caller logs the first two at Debug as "discovery refused" and cannot tell them from the race, which is harmless because the row then exists. |
| `ListDiscoveredHosts(ctx) ([]DiscoveredHost, error)` | `SELECT … FROM discovered_hosts d WHERE NOT EXISTS (SELECT 1 FROM hosts h WHERE h.mac = d.mac) ORDER BY last_seen DESC, mac ASC` | rows; empty slice, not nil |
| `CountDiscoveredHosts(ctx) (int, error)` | `SELECT count(*) FROM discovered_hosts` | the count, for `discoveredFull` |
| `DeleteDiscoveredHost(ctx, mac) (bool, error)` | `DELETE FROM discovered_hosts WHERE mac=?` | `RowsAffected()==1` |
| `DeleteAllDiscoveredHosts(ctx) (int64, error)` | `DELETE FROM discovered_hosts` | rows deleted |

`DiscoveredHost` is `{MAC, Arch, IP, FirstSeen, LastSeen string}`, with the two times in SQLite
form. `pkg/http` converts them to RFC 3339.

Gate 1 checked the capped insert in modernc SQLite 3.53.2 with this repo's DSN and one connection.
Under 64 concurrent goroutines it held the cap, and the `NOT EXISTS` guard returned 0 rows for a
registered MAC.

## 5. Boot-path flow (`GET /booty.ipxe`)

1. Resolve the MAC exactly as today: `?mac=`, else ARP on the source IP. `identifyHostWith`
   (`pkg/http/preseed.go:187`) gains a sibling `resolveBootHost(r, arp) (host *hardware.Host, mac string,
   err error)` (the name `identifyHost` is taken, `preseed.go:180`), and `identifyHostWith` becomes a one-line wrapper returning its `host`:
   - `mac` is the canonical MAC, or `""` when none was resolved or `NormalizeMAC` rejected it.
   - `err` is the `GetMacAddress` error.

   The existing callers (`/preseed`, `/menu/`) use only `host` and keep today's behaviour.
2. If `host != nil`: render as today. No recording.
3. If `mac == ""`, or `err` is not `hardware.ErrNotFound`: serve the holding script. No recording.
4. Otherwise:
   - Map `r.URL.Query().Get(bootscript.ArchQueryKey)` through D5, and take the source key from D7.
   - Call `TouchDiscoveredHost`. If the row exists, stop.
   - Else, if `limiter.Allow(key)` is false, Debug-log "discovery refused: rate limited" and stop.
   - Else call `InsertDiscoveredHost(..., 1024)`. On false, Debug-log
     "discovery refused: table full or registered".
   - A database error is logged at Warn and never changes the response.
5. Serve the holding script.

**Response invariant (replaces "byte-identical to today", Gate 1 B3):** the response body is
identical to what the same request returns with recording disabled. The holding script's text
changes once, through D6. Its chain line becomes:

```
chain http://[[server]]/booty.ipxe?mac=${mac}&arch=${buildarch} || shell
```

## 6. UI

- **Hosts → Pending tab**, two sections:
  1. **"New machines"**, from `discovered`. The columns are MAC, Arch (`amd64`, `arm64` or `—`), IP,
     First seen and Last seen (local time, from the RFC 3339 value), plus the actions **Approve** and
     **Dismiss**.
     - Dismiss uses an AntD `Popconfirm`, then `DELETE /api/v1/discovered/{mac}`.
     - The section header has **Dismiss all** (a `Popconfirm` that names the count, then
       `DELETE /api/v1/discovered`).
     - When `discoveredFull` is true, the section shows an AntD `Alert` (type `warning`): "The
       new-machine list is full (1024). booty is not recording new machines until you dismiss
       some."
     - The section is hidden when `discovered` is empty and `discoveredFull` is false.
  2. The existing table of registered, unapproved hosts, unchanged except for D16.

  The Pending badge counts both sections.
- **The Approve dialog for a discovered machine** is the existing modal with a required **OS**
  select first, populated per D9's third fix.
  - Selecting `talos` reveals the existing schematic field. Selecting another OS hides it.
  - The Config list is filtered by the selected OS with the helper the view already uses for a
    registered host, `kindsForHostOS` (`web/src/api/configKinds.ts:88`), keyed on the form's
    selected value (`Form.useWatch('os', form)`) instead of `allowing?.os`. Changing the OS clears
    `configId`.
  - Submit runs the D8 sequence.
  - The registered-host dialog is unchanged apart from D16.
- **Home:**
  - The Hosts tile's number stays the count of registered hosts. Its "pending approval" subtitle
    counts registered pending hosts plus discovered machines.
  - Needs-attention lists discovered machines (MAC and arch) with an **Approve** link to `/hosts`,
    because approving needs an OS choice. Registered pending hosts keep their inline Approve.
  - The fresh-install "Get started" card (`web/src/views/HomeView.tsx:43-44`, `fresh` = every count
    is 0) also requires `discovered.length === 0`. Otherwise a machine booted before the first
    catalog download would be hidden behind it.
- No emoji. Use AntD components and tokens (the project's UI rules).

## 7. Testing

Write every test first and watch it fail on the unfixed tree.

**`pkg/db`**

- The migration reaches version 11.
- Touch: reports whether the row exists. Within 20 s it does not write. After 20 s it writes, and
  an empty arch keeps the stored arch.
- Insert:
  - Refuses row 1025 and evicts nothing.
  - Refuses a registered MAC.
  - Returns false on conflict.
- List excludes registered MACs and sorts as specified.
- Delete and DeleteAll return the counts specified.
- `UpsertHost` deletes the discovered row in the same transaction.
- Restart: insert a row, close the store, reopen the same file, and the row is still listed.

**`pkg/http` boot path**

- An unknown `?mac=` with `arch=x86_64` creates a row with `amd64` and the source IP.
- A repeat within 20 s does not write. The test drives the clock by seeding `last_seen` backwards
  in SQL.
- The 9th new MAC from one IP within the window is refused. A different IP is still allowed. Two
  IPv6 addresses in one /64 share a budget.
- A full table refuses a new MAC, keeps every row, and still serves the holding script.
- `arch=sparc` and an absent `arch` store `""`.
- A registered MAC records nothing.
- A lookup error other than not-found records nothing.
- The body equals the recording-disabled body.

**`pkg/http` non-recording routes**

- `/ignition.json`, `/machineconfig`, `/preseed`, `/menu/...`, the `approve`, `delete` and
  `schematic` operations, and TFTP menu selection create no row for an unknown MAC.
- An `/ignition.json` that returns 500 creates no row (the #89 reproduction).

**`pkg/http` API**

- `GET /api/v1/hosts` has `discovered` (`[]` when empty), `discoveredFull`, and no `unknown` key.
- `DELETE /api/v1/discovered/{mac}` returns 204 or 404, and a `hosts` row with the same MAC is
  untouched.
- `DELETE /api/v1/discovered` returns 200 and the count.
- `POST /api/v1/hosts {"os":"fedora-coreos"}` stores `coreos`.
- Approve returns 422 for a host whose `os` is a tool OS, and the host stays unapproved.
- Approve of a `talos`, `flatcar`, `coreos` or `debian` host succeeds.
- `TestEveryRegisteredOperationIsGated` covers the two new operations.

**`pkg/bootscript`**

- Every rendered chain line contains `&arch=${buildarch}`: the stub, the holding script, the three
  tool-script chains and the menu.
- `Assignable` returns true for exactly talos, flatcar, coreos and debian among
  `ostype.All()` ∪ {coreos}.

**`open_routes_test.go`:** D15.

**Web (Vitest)**

- "New machines" renders from `discovered`.
- Approve requires an OS. Its options exclude tool OSes. It calls createHost, then bindSchematic
  (Talos with a schematic only), then approveHostWith, in that order.
- On a failed approve, an error message appears and a reload shows the host in the registered
  Pending table.
- Dismiss and Dismiss all confirm, then call DELETE.
- The full-table Alert renders when `discoveredFull` is true.
- The Home subtitle count includes discovered machines, and the "Get started" card is not shown while `discovered` is non-empty.
- A failed approve step 2 or 3 calls `DELETE /api/v1/hosts/{mac}` and then reloads.
- No "Allow" text remains in the Hosts view.

**Acceptance (the M0 exit test, run before the PR):** the QEMU user-network lab.

- Setup:
  - booty runs on the host with `--serverIP 10.0.2.2 --httpPort 18080 --serverHttpPort 18080
    --tftpPort 16969`.
  - Put booty's TFTP stub, fetched with `curl tftp://127.0.0.1:16969/booty.ipxe`, in a directory.
  - Boot a BIOS guest with `-netdev user,tftp=<dir>,bootfile=booty.ipxe` and `-m 3072`.
  - Write a minimal Flatcar Butane to `<dataDir>/config/ignition.yaml`.
  - Before step 3, wait until `GET /api/v1/cache` lists a verified `flatcar` entry: the default
    catalog downloads it on a fresh dataDir (`pkg/cache/catalog.go:219-226`).
- Pass criteria:
  1. The VM appears under Pending → New machines with arch `amd64`.
  2. After `kill -TERM` and a restart of booty, it is still there.
  3. Approve it in the UI with OS `flatcar`.
  4. The VM boots Flatcar to a shell.
- Screenshots of steps 1, 2 and 4 go in the PR.
- UNVERIFIED: what QEMU's bundled iPXE ROM reports for `${buildarch}`. Step 1 checks it. If it
  reports nothing, the row shows `—` and the D5 tests are still the proof of mapping.

## 8. Documentation (in this change)

- `docs/schema/API.md`:
  - The `GET /api/v1/hosts` row: `discovered` and `discoveredFull` replace `unknown` (breaking).
  - New rows for the two `DELETE /api/v1/discovered` operations.
  - The approve row: the assignable-OS 422.
  - The create-host row: the stored boot-vocabulary name.
  - The retired-routes table at lines 44-45.
  - The `/booty.ipxe` row: the `arch` parameter and recording.
- `docs/schema/DATABASE.md`: the `discovered_hosts` table replaces the "unknown hosts" section.
- `.claude/CLAUDE.md`:
  - The D14 invariant.
  - "How a boot works", step 4: unknown MACs "are recorded as discovered machines and appear under
    Pending until approved".
  - Under the repository map, `pkg/db/` mentions discovered hosts.

## 9. Concurrency notes for the builder

- The store has one connection (`SetMaxOpenConns(1)`). Inside a transaction, use only `tx`.
- The boot handler's Touch → Allow → Insert sequence is not atomic, and does not need to be. The
  insert's own guards (cap, `NOT EXISTS hosts`, `ON CONFLICT DO NOTHING`) are what keep the
  invariants. A race costs at most one extra limiter token.

## 10. Follow-ups (filed, not done here)

- #159: the boot-event log. #78 is closed by this change with a comment pointing at #159.
- #158 stays open and separate. D8's millisecond window is noted there.

## 11. Review record

- **Builder check:** two questions.
  1. Which OSes can the dialog offer and the endpoint accept? Resolved by D9.
  2. Does `boot/embed.ipxe` get the arch? Resolved by D6: it is excluded, and why.
- **Gate 1 (sr-eng-review):**
  - **Blocking:**
    - B1, the dialog offered 9 OSes the boot path cannot boot. Resolved by D9.
    - B2, a leftover discovered row let Dismiss delete a real host. Resolved by D11 and D12.
    - B3, "byte-identical" contradicted D6. Resolved in §5.
  - **Significant:**
    - S1, the refresh write had no limit. Resolved by D7 and D14.
    - S2, the open-route `mutates` flag. Resolved by D15.
    - S3, the Talos schematic flow. Resolved by D8 step 2.
    - S4, the seventh chain site. Resolved by D6.
    - S5, the route shapes. Resolved by D11.
    - S6, the promote transaction. Removed by D8, which has no promote endpoint. The `UpsertHost`
      transaction and single-connection trap remain in D10.
    - S7, spam recovery and limit evasion. Resolved by D7 (IPv6 /64, port) and §6 (Dismiss all,
      the full-table alert).
  - **Minor:**
    - 1, the wrong preseed line. Fixed in D1.
    - 2, the "hardcoded arch" wording. Fixed in the non-goals.
    - 3, `assigned_arch`. Not written (non-goals).
    - 4, the anti-goal wording. Reworded.
    - 5, the insert's return value. Pinned in §4.
    - 6, timestamps. D13.
    - 7, where the arch key lives. D6.
    - 8, `?approved=` and the Home tile. D13 and §6.
    - 9, the log-flood reasoning. Corrected in D7.
    - 10, dead `GetData` and `BootyData`. D3.
- **Gate 1, second pass (no blocking):**
  - S1, a fresh-install Home hid a waiting machine. Fixed in §6 (Home).
  - S2, the D8 window on failure. Fixed by the rollback DELETE in D8.
  - S3, D3 dropped coverage. Fixed by D3's test split.
  - Minor 1-13 folded in: the `fedora-coreos` row rewrite in 0011 (D9), `NOT EXISTS` in List (§4),
    HEAD and nil store (D4), link-local and the `ip` column (D7), citations (D4, §6), reload in
    `finally` (D8), both open-route comments (D15), the stale-comment list (D3), the D6 test list
    (§7), the rename sites (D9), `>= 1024` (D13), the registrar file (D11), the consumer wording
    (D13), and waiting for the Flatcar cache (§7).
