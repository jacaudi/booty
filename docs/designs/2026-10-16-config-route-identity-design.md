# Design: config routes serve a host's config only to that host's own boot

**Issues:** closes [#88](https://github.com/jacaudi/booty/issues/88). Deliberate spoofing is
filed as [#166](https://github.com/jacaudi/booty/issues/166) (§12). #164 was closed (not
planned) with a "Duplicate of #88" comment.
**Milestone:** M0 (hardening and groundwork).
**Status:** design, revised after Gate 1 (§13).
**Related:**
- #158 and #162: the config routes answer an unknown, unapproved or revoked host with the
  unknown-host answer.
- #71: routed VLANs; cluster-member config and node reports still need ARP.
- #130: the discovery OS spike, a later source of a real per-machine credential.

---

## 1. Why

`GET /machineconfig`, `/preseed` and `/ignition.json` decide which host is asking from the
client-asserted `?mac=` (`pkg/http/machineconfig.go:84`, `pkg/http/preseed.go:210`,
`pkg/http/ignition.go:58`). After #158 and #162, an unknown or unapproved MAC gets nothing.
Any request that names an **approved** host's MAC, from any source IP, still gets:
- that host's bound config;
- or, when nothing is bound, the server-default template, with the join string if the template
  uses it.

Rung 4 also ignores the OS family, so a Flatcar host's MAC unlocks the Talos and Debian defaults
too. Evidence: #88's body (an executed `curl` returning a host's machineconfig) and its follow-up
comment.

The maintainer, 2026-10-16, on who this protects against: *"Basically any device that is not
approved and it doesn't necessarily mean a bad actor, just a misconfigured one."*

## 2. Goals, non-goals, anti-goals

**Goals**
1. A config route serves a host's config only to a request that came from a boot booty itself
   rendered for that host, for that route's OS family.
2. This works the same on booty's own VLAN, across a router, behind NAT, and in the QEMU
   user-mode lab, where every request arrives from 127.0.0.1.
3. Ticket issuance and ticket checking each sit behind one seam. A stronger proof can replace
   either one later without editing the config routes or the script renderer.
4. The three copies of "?mac= else ARP" become one helper with an injected ARP lookup.

**Non-goals**
- Stopping a client that **deliberately** asks `/booty.ipxe?mac=<approved MAC>` or
  `/menu/…?mac=<approved MAC>` for a ticket. That is #166.
- Changing what `/booty.ipxe`, `/menu/` or discovery recording accept (§9).
- Relaxing the cluster-member ARP check (#71).
- Telling apart two machines that share a MAC (a cloned VM). No MAC-based proof can.
- Fixing iPXE's raw `${serial}` and `${hostname}` expansion, which can put a space in the Talos
  kernel line (R12). The ticket is placed so it survives; the trailing parameters truncate today,
  and they still will.

**Anti-goals** (failure even if it technically works)
- Breaking routed-VLAN boots. They work today through `?mac=` (#71's remaining scope is members
  and node reports only).
- Breaking a same-VLAN boot that works today. The case that matters here is a Talos boot whose
  SMBIOS serial contains a space (R12).
- Any config fetch that does not come through a booty-rendered script. The maintainer: *"No, it
  should all come through booty as a cache."*
- A 5xx from `/ignition.json` when it refuses a host. Ignition retries 5xx forever, so the host
  would never reboot into a fresh ticket (R4).
- booty logging a ticket.

## 3. Decisions

### Maintainer's decisions (2026-10-16)

| # | Decision | Their words |
|---|---|---|
| M1 | Threat model: any unapproved or misconfigured device, not only hostile ones. | "any device that is not approved and it doesn't necessarily mean a bad actor, just a misconfigured one" |
| M2 | booty runs host-networked on the server VLAN and boots machines **both** on that VLAN and on routed VLANs. This rules out an ARP match on every config route. | "host networking on the server VLAN"; "Both" |
| M3 | A boot ticket that stops accidents but not deliberate spoofing is accepted, provided the design leaves room to stop the malicious case later. | "Yes, but don't code us into a corner to prevent the malicious aspect as well" |
| M4 | No escape hatch for config URLs that booty didn't render. | "No, it should all come through booty as a cache" |
| M5 | This change closes #88. Deliberate spoofing is a separate issue, opened now (#166). | "deliberate spoofing filed as its own follow-up and open that issue" |
| M6 | The ticket lasts 1 hour, fixed. Chosen over 24 hours and over a flag, after Gate 1 raised the outage cost (R3). | Picked "1 hour (Recommended)" |

### Researched calls, with evidence

**R1. A signed boot ticket, not ARP or IP pinning.** Options weighed against M1 and M2:
- **ARP on every route:** fails across routers (M2), behind NAT and in the 127.0.0.1 lab.
- **Pinning the source IP of the `/booty.ipxe` fetch:**
  - NAT and the lab share one IP.
  - The installed OS's DHCP client can get a different lease from iPXE's, for example when it
    uses a DUID-based client-id. That is UNVERIFIED per OS, and an unverified premise is a
    reason not to build on it.
- **Family gate plus docs only:** leaves the copied-URL case in M1.

The ticket is the only option that behaves the same in every topology. *My call*, accepted by
the maintainer (M3).

**R2. The ticket binds MAC + family + expiry, and carries only the expiry.**
- **Wire format:** `v1.<exp>.<sig>`, where:
  - `exp` is the expiry as decimal Unix seconds;
  - `sig` is unpadded base64url of `HMAC-SHA256(key, "booty-boot-ticket\x00v1\x00" + mac + "\x00" + family + "\x00" + exp)`;
  - `exp` is signed exactly as it appears in the ticket.
- **The verifier recomputes `sig`** from the MAC of the host it is about to serve and its own
  route's family. That is why the ticket never carries the MAC or the family.
- **Binding the family replaces "gate rung 4 on OS family".** A menu-mode host has no fixed OS
  (`pkg/bootscript/tokens.go:52-67`), so gating on `host.OS` would break a menu host that picks
  Talos. The ticket's family is the family of the script booty actually rendered, so it is right
  for assigned and menu boots alike.
- **The `v1.` prefix** lets a later format coexist (M3).
- **Pattern sources:** `nodereport.Token` (`pkg/nodereport/token.go:25-32`) for the
  domain-separated HMAC and its key-length guard, and `auth.IssueCookieValue` /
  `VerifyCookieValue` (`pkg/auth/cookie.go:29-55`) for `payload.sig`, with the signature checked
  before the expiry.
- **Length:** at most 57 characters: `v1.` (3) + up to 10 digits + `.` + 43. *My call.*

**R3. Lifetime: 1 hour, the constant `bootticket.TTL` (M6).**

Evidence (source tags: Talos v1.14.4, Ignition v2.28.0, d-i preseed 1.117/1.125):
- **Talos `talos.config=`** retries a 404, waiting `int((2^c-1)/2)` s plus up to 1 s of jitter
  between attempts, with no cap. Attempts start at about 0, 0, 1, 4, 11, 26, 57, 120, 247, 502,
  1013… s. Each attempt is bounded to 3 min and the whole load to 3 h (`metal.go:100-108`,
  `pkg/download/download.go:272-278`, `constants.go:1037,1040`, `acquire.go:214`).
  - Whether the controller restarts the load after the 3 h bound is UNVERIFIED (COSI restart
    semantics, Gate 1). Either way, a 404 is not healed by waiting.
- **Ignition** treats a 404 as fatal and goes to `emergency.target`
  (`internal/resource/url.go:397-402`, `ignition-fetch.service:18`). It retries connection
  errors and 5xx forever (`internal/resource/http.go`).
  - Flatcar's 300 s emergency reboot (`bootengine dracut/99emergency-timeout/timeout.sh`) and
    FCOS staying in the emergency shell are UNVERIFIED (research only).
  - booty never sends Ignition a 404 or a 5xx on refusal (R4), so neither case arises.
- **The Debian installer** fetches once (`preseed.sh:55`). On failure it blocks at a critical
  dialog (`preseed.sh:18,59`).

All three fetch once the network is up, within minutes of the script render. The exact gap is
UNVERIFIED per OS, and it is why the TTL is an hour, not minutes.

**The accepted cost (M6).** A Talos node or Debian installer whose ticket expires before booty can
serve it waits until it is rebooted, and a reboot always gets a fresh ticket. That happens when:
- booty is unreachable for over an hour;
- a server-side fault lasts over an hour, for example a broken template, or a member's 500 while
  `--secretsKey` is unset (`cmd/main.go:451-458`).

Each refusal is logged with its reason (§8), so an expired ticket is diagnosable. The docs state
the reboot remedy (§11).

**Legacy-GRUB Talos installs** copy `talos.config=` from `/proc/cmdline` into the installed boot
entry (`cmd/installer/pkg/install/install.go:141-142`). Talos reads it only when STATE holds no
config, so the stale ticket matters only after STATE is wiped, and then a re-PXE fixes it. Who
sets `ConfigSource` is UNVERIFIED.

**R4. A refusal gets the existing unknown-host answer, and `/ignition.json` never 5xxs on it.**
- **`/machineconfig` and `/preseed`** return `404` "no approved host for this request", the same
  body as today's `machineconfig.go:52` and `preseed.go:46`, so the answer is no oracle.
- **`/ignition.json`** serves the reboot config. Today, `serveFileIgnition` resolves, parses and
  executes the `--ignitionFile` template (`ignition.go:142-167`) **before** its
  `host == nil` reboot branch (`:169-184`). A missing or broken template therefore turns the
  reboot answer into a 500, which Ignition retries forever.
- **This change** serves the reboot config, for every caller with no approved, proven host,
  through a new `serveRebootIgnition(w)` that never reads a template. The JSON is unchanged:
  Ignition `3.4.0`, plus the `booty-reboot.service` unit from `rebootUnitContents`.
- **A visible behaviour change, deliberate:** an unknown host's reboot config no longer depends
  on the template file either. *My call.*

**R5. The key: `<dataDir>/boot-ticket-key`, 32 random bytes, mode 0600, created on first start.**
- **It loads unconditionally.** The node-report key, by contrast, loads only under
  `--nodeReports` (`cmd/main.go:522`).
- **Startup:** a key file that exists with the wrong length, or a path that can't be read, fails
  startup before any server starts.
- **The loader:** `nodereport.LoadOrCreateKey`, `writeKey` and `syncDir`
  (`pkg/nodereport/token.go:44-117`) move into a new leaf package, `pkg/keyfile`, as
  `keyfile.LoadOrCreate(path string) ([]byte, error)`.
  - The temp-file pattern becomes `"." + filepath.Base(path) + "-*"`, so the node-report key
    keeps its `.node-report-key-*` temp name, and the existing test's prefix check stays live.
  - Error text becomes generic, naming the path: for example `read key <path>: …` and
    `key <path>: N bytes, want 32`.
- **`cmd` calls it for both keys:** `node-report-key` (wrapped `node reports: %w`, unchanged) and
  `boot-ticket-key` (wrapped `boot tickets: %w`).
- **Why move it:** the shared knowledge is "a 32-byte key file, written atomically at 0600 and
  synced". It changes when the on-disk key format changes, for both keys together.
- **Rotation:** delete the file and restart. Every outstanding ticket is then refused, and
  in-flight boots fail once. This goes in the docs.
- *My call.*

**R6. Two seams, each a consumer-declared role interface (M3, and the CLAUDE.md "Seam" rule).**
- **Issuing** lives in `pkg/bootscript/ticket.go`, a new file:

  ```go
  // TicketIssuer mints the boot ticket a rendered OS script carries in its config URL.
  type TicketIssuer interface {
      Issue(mac, family string) string
  }
  ```
- **Checking** lives in `pkg/http/proof.go`, a new file:

  ```go
  // hostProof decides whether a config request proves it comes from the boot booty
  // rendered for host mac, for OS family. A nil error means proven; the error says why not.
  type hostProof interface {
      Prove(r *http.Request, mac, family string) error
  }
  ```
- One concrete type, `*bootticket.Ticketer`, satisfies both. `cmd` builds it once and passes it to
  HTTP and TFTP; that is the patchbay.
- **The asymmetry between the two deps fields:**
  - `APIDeps.Tickets` is the concrete `*bootticket.Ticketer`, because `pkg/http` needs both roles:
    it issues from `/booty.ipxe` and `/menu/`, and it checks on the config routes.
  - `tftp.Config.Tickets` is `bootscript.TicketIssuer`, because TFTP only issues.
  - `baseMux` hands `deps.Tickets` to each handler as the role interface it needs.
- **The interfaces exist now despite having one implementor** because the maintainer requires the
  extension point (M3). They let #166 swap in a proof without editing either consumer:
  - `Prove` takes the whole request, so a later proof can read an ARP match, a client certificate
    or a header.
  - The request-aware issuer #166 needs is built per request in the HTTP handler, and wraps
    `*Ticketer`.
- *My call.*

**R7. The query key is single-sourced as `bootticket.QueryKey = "ticket"`.**
- The PXE templates write it, by string concatenation as `ChainPath` does
  (`pkg/bootscript/chain.go:12`).
- `Ticketer.Prove` reads it.
- *My call.*

**R8. Family names are single-sourced as exported constants in `pkg/ostype/ostype.go`.**
- The constants are `FamilyIgnition = "ignition"`, `FamilyTalos = "talos"`,
  `FamilyDebian = "debian"` and `FamilyTool = "tool"`.
- The `families` map (`ostype.go:64-72`) and `isToolOS` (`pkg/bootscript/tokens.go:133-136`) use
  them.
- `pkg/http` names each route's family with them.
- **Which family a script's ticket gets:** `configFamily(cacheName)` in
  `pkg/bootscript/ticket.go` returns
  `ostype.Lookup(cache.CacheNameToCanonical(cacheName)).Family().Name`. It returns `""` when the
  lookup misses or the family's `ConfigKind` is empty, the existing "config-less family" test
  (`pkg/http/render.go:42`).
- **Why constants:** they are the contract between issuer and verifier, and a typo on either side
  would refuse every boot.
- **Out of scope:** the other `"tool"` literals in `pkg/cache/catalog.go:68` and
  `pkg/cache/retention.go:88` are left alone. They do not touch the ticket contract.
- *My call.*

**R9. The kernel command line has room.**
- `COMMAND_LINE_SIZE` is 2048 on x86 and arm64 (Linux v7.2: `arch/x86/include/asm/setup.h:7`,
  `arch/arm64/include/uapi/asm/setup.h:25`). That is research-cited, not re-checked by Gate 1.
- The longest rendered line is `coreos.ipxe`'s kernel line, which expands `${BASEURL}` twice.
  With a typical base URL it is a few hundred bytes, so the ticket parameter (about 65 bytes)
  keeps it far under 2048.

**R10. Talos keeps the extra query parameter.**
- `url.PopulateVariables` (Talos v1.14.4,
  `internal/app/machined/pkg/runtime/v1alpha1/platform/metal/url/url.go`) returns the URL
  unchanged when no variable matches (`:43-46`).
- When one does match, it re-encodes the query with `u.Query()` / `Encode()` (`:115`), which keeps
  unknown keys. Gate 1 notes that `MatchOnArg` keys can match on an empty value
  (`variable.go:73-77`), which takes the re-encode path; the ticket survives it.
- The ticket's characters `[A-Za-z0-9._-]` are unchanged by URL encoding.

**R11. Every boot client keeps `&ticket=…`.**
- **Debian installer (trixie, 13): measured 2026-10-16.**
  - **Setup:**
    - booted Debian's own netboot `linux` and `initrd.gz` from
      `deb.debian.org/debian/dists/trixie/main/installer-amd64/current/images/netboot/debian-installer/amd64/`;
    - QEMU 11.1.2 TCG, user networking;
    - kernel line `auto=true priority=critical preseed/url=http://10.0.2.2:8099/preseed?mac=52:54:00:12:34:56&ticket=v1.1792174489.AbC-_xyz&probe=1`;
    - served by a request-logging `uvx python -m http.server`.
  - **Result:** the server logged
    `"GET /preseed?mac=52:54:00:12:34:56&ticket=v1.1792174489.AbC-_xyz&probe=1 HTTP/1.1" 200`,
    checked byte for byte with `od -c`. The installer then went on to its keymap question.
  - **Scope:** `DefaultDebianChannel` is `"13"` (`pkg/ostype/debian.go:19`). Bookworm (12) is
    UNVERIFIED end-to-end; the research found its preseed code differs only in a hostname check.
- **Ignition (v2.28.0): verified in source.**
  - The cmdline provider splits on whitespace, then on the first `=` (`cmdline.go:108-109`), and
    parses the value with `url.Parse` (`:124`).
  - The request uses `u.String()` (`internal/resource/url.go:383`), which writes `RawQuery` back
    unchanged.
- **iPXE (v2.0.0): verified in source.**
  - Only the whole tokens `&&`, `||` and `;` act as separators (`src/core/exec.c:190-204`).
  - The ticket alphabet has no `$`, so `${…}` expansion cannot touch it.
- **Talos:** today's `talos.ipxe` already carries `&` in its kernel line, and boots.

**R12. The Talos ticket goes before `${serial}` and `${hostname}` (Gate 1 B1).**

How iPXE v2.0.0 builds the kernel line:
- It splits a script line into tokens before expanding `${…}` (`src/core/exec.c:122-140`; the
  expansion is at `:254`).
- It copies a string setting raw (`src/core/settings.c:1655-1666`).
- `kernel` joins its arguments with bare spaces and no quoting (`src/hci/commands/image_cmd.c:120`,
  `exec.c:359-384`).

The consequence:
- An SMBIOS serial such as `Default string` puts an unquoted space inside the `talos.config=`
  value, and the kernel ends the parameter there.
- **Today** that loses `mac=` and `hostname=`, and the host is still served on-L2 through the ARP
  fallback (`machineconfig.go:88-97`).
- **With the ticket last**, the host would get a 404 forever. So the ticket goes before every
  variable that can hold a space: `machineconfig?mac=${mac}&ticket=[[ticket]]&uuid=${uuid}&serial=${serial}&hostname=${hostname}`.
  `url.Values.Get` returns the first value, so a later `&ticket=` smuggled into a hostname cannot
  override it.
- **The other three templates** put `?mac=${mac}&ticket=[[ticket]]` first, and have no
  free-text variables.
- How common such serials are on real hardware is UNVERIFIED. The mechanism is verified, and T7
  pins the placement.

## 4. The ticket package: `pkg/bootticket` (new, leaf)

It imports only the standard library.

```go
package bootticket

const (
    TTL      = time.Hour
    QueryKey = "ticket"
    version  = "v1"
    keyLen   = 32
)

var (
    ErrMissing = errors.New("bootticket: no ticket")
    ErrInvalid = errors.New("bootticket: invalid ticket")
    ErrExpired = errors.New("bootticket: expired ticket")
)

type Ticketer struct{ key []byte }

// New returns a Ticketer for a copy of key (bytes.Clone), or an error when key is not 32 bytes.
func New(key []byte) (*Ticketer, error)

// Issue returns a v1 ticket for mac and family, expiring TTL after time.Now().
// It returns "" when t is nil, t's key is not 32 bytes (a zero Ticketer), or mac or family is "".
func (t *Ticketer) Issue(mac, family string) string

// Check returns nil when ticket is a valid, unexpired v1 ticket for mac and family.
func (t *Ticketer) Check(ticket, mac, family string) error

// Prove is Check(r.URL.Query().Get(QueryKey), mac, family). It satisfies pkg/http's hostProof.
func (t *Ticketer) Prove(r *http.Request, mac, family string) error
```

`Check`, in this order:
1. `t == nil`, `len(t.key) != keyLen`, `mac == ""` or `family == ""` → `ErrInvalid`.
2. `ticket == ""` → `ErrMissing`.
3. `strings.Split(ticket, ".")` must give exactly 3 parts, and part 0 must be `"v1"`; otherwise
   `ErrInvalid`.
4. Part 1 must parse with `strconv.ParseInt(p, 10, 64)` and round-trip:
   `strconv.FormatInt(n, 10) == p`. That rejects `+`, leading zeros and whitespace. Otherwise
   `ErrInvalid`.
5. Recompute the signature string over part 1 exactly as written. Compare with
   `hmac.Equal([]byte(part2), []byte(want))`, as encoded strings, which also rejects base64
   trailing-bit variants. A mismatch is `ErrInvalid`.
6. `!time.Now().Before(time.Unix(n, 0))` → `ErrExpired`.

Rules:
- **MACs are passed as stored:** callers always pass `host.MAC` from the database record, which
  is normalised at write (`hardware.NormalizeMAC`), never the raw query value. Issuer and verifier
  therefore sign the same bytes.
- **Time:** `time.Now()` directly. Unit tests control it with `testing/synctest`. This is the
  repo's first `synctest` use; it is in a leaf package with no I/O. The synctest epoch is
  2000-01-01, so `exp` there has 9 digits, which step 4 accepts.
- **No logging:** `bootticket` never logs. The callers log (§8).

## 5. Wiring (the patchbay)

1. **Load the key.** `cmd/main.go` `run`, directly after the node-report key block (`:521-528`),
   always loads `keyfile.LoadOrCreate(filepath.Join(viper.GetString(config.DataDir), "boot-ticket-key"))`
   and builds `bootticket.New(key)`. Either error returns `fmt.Errorf("boot tickets: %w", err)`
   before any server starts.
2. **Hand it to TFTP.** `tftp.Config` (`pkg/tftp/tftp.go:124-130`) gains
   `Tickets bootscript.TicketIssuer`. `tftp.Start` returns an error when it is nil:
   `"tftp: no boot ticket issuer"`. `server` keeps it for the `menu/` branch.
3. **Hand it to HTTP.** `APIDeps` (`pkg/http/api.go:18`) gains `Tickets *bootticket.Ticketer`.
   - `StartHTTP` (`pkg/http/http.go:104`) checks it first, before the Auth check and
     unconditionally (including under `--noAuth`), and returns
     `errors.New("http: no boot ticket issuer; refusing to start")`.
   - `baseMux` stays callable with a nil `Tickets` for tests. The handlers then fail closed (step 5).
4. **New handler constructors** (the old wrappers `handleIgnitionRequest`,
   `handleMachineConfigRequest`, `handlePreseedRequest` and `preseedHandler` are deleted, and every
   caller moves to these):

   | Constructor | File | `baseMux` call |
   |---|---|---|
   | `ignitionHandler(store *db.Store, n *NodeReports, arp arpFunc, proof hostProof) http.HandlerFunc` | `ignition.go` | `ignitionHandler(deps.Store, deps.NodeReports, arpLookup, deps.Tickets)` |
   | `machineConfigHandler(store *db.Store, arp arpFunc, proof hostProof) http.HandlerFunc` | `machineconfig.go` | `machineConfigHandler(deps.Store, arpLookup, deps.Tickets)` |
   | `preseedHandler(store *db.Store, n *NodeReports, arp arpFunc, proof hostProof) http.HandlerFunc` (renamed from `preseedHandlerWith`) | `preseed.go` | `preseedHandler(deps.Store, deps.NodeReports, arpLookup, deps.Tickets)` |
   | `bootScriptHandler(store *db.Store, arp arpFunc, issuer bootscript.TicketIssuer) http.HandlerFunc` | `bootscript.go` | `bootScriptHandler(deps.Store, arpLookup, deps.Tickets)` |
   | `menuSelectionHandler(arp arpFunc, issuer bootscript.TicketIssuer) http.HandlerFunc` | `bootscript.go` | `menuSelectionHandler(arpLookup, deps.Tickets)` |

5. **Nil is closed at every seam.**
   - **A nil `*Ticketer`** inside an interface makes the interface non-nil. Its methods are
     nil-receiver-safe: `Issue` returns `""` and `Check`/`Prove` return `ErrInvalid`.
   - **A literal nil interface** is handled by the two helpers that wrap every call:
     - `ticketFor(issuer TicketIssuer, mac, cacheName string) string` in `pkg/bootscript/ticket.go`
       returns `""` when `issuer == nil`, `mac == ""`, or `configFamily(cacheName) == ""`, and
       otherwise `issuer.Issue(mac, configFamily(cacheName))`.
     - `proven(r *http.Request, p hostProof, host *hardware.Host, family string) bool` in
       `pkg/http/proof.go` returns false when `p == nil`, and logs the refusal (§8).
   - **No path panics on missing wiring,** so a TFTP `menu/` request cannot crash the process.
     pin/tftp v3.2.0 runs `readHandler` with no `recover`.

## 6. Config routes, rung by rung

**Identity, one helper.** All three routes call `resolveBootHost(r, arp)` (`pkg/http/preseed.go:210`,
unchanged). It reads `?mac=`, falls back to ARP on the source IP, normalises the MAC, and looks
the host up.
- `machineConfigMAC` (`machineconfig.go:84-98`), `machineConfigHost` (`:102-111`),
  `ignitionRequestMAC` (`ignition.go:58-77`) and `ignitionHost` (`:85-96`) are deleted.
- **Lookup-error logging, in machineconfig and ignition (as today):** when `err != nil` and not
  `errors.Is(err, hardware.ErrNotFound)`, log at Warn `"error looking up host"` with
  `"mac", loggableMAC(r.URL.Query().Get("mac"))` and `"err", err`. That replaces today's raw,
  client-controlled `"mac"` value.
- **Behaviour is unchanged:** `hardware.GetMacAddress` already normalises
  (`pkg/hardware/mac.go:244-247`), so a malformed `?mac=` still yields no host.
- `queryHostname`'s log uses the normalised MAC that `resolveBootHost` returns.

Below, **proof** means `proven(r, proof, host, <route family>)`.

### `GET /machineconfig` (family `ostype.FamilyTalos`)

| Order | Condition | Answer | Changed? |
|---|---|---|---|
| 1 | host is a member (`NodeConfigID != nil`) and not approved | `403` "cluster member config requires an approved host" | no (#158) |
| 2 | no approved host (unknown, unapproved, revoked, no MAC) | `404` "no approved host for this request" | no (#162) |
| 3 | **approved host, proof fails** | **`404` "no approved host for this request"** | **new** |
| 4 | member, source IP does not ARP-resolve to the member's MAC | `403` "cluster member config requires the request to come from the member" | no |
| 5 | member | frozen config, verbatim | no |
| 6 | bound `machineconfig` config (rungs 1–2) | rendered bound config | no |
| 7 | otherwise (rungs 3–4) | `--talosConfigFile` template | no |

### `GET /preseed` (family `ostype.FamilyDebian`)

| Order | Condition | Answer | Changed? |
|---|---|---|---|
| 1 | no approved host | `404` "no approved host for this request" | no (#162) |
| 2 | **approved host, proof fails** | **`404` "no approved host for this request"** | **new** |
| 3 | bound `debianconfig` (rungs 1–2) | rendered, with the DVD mirror and node-report `late_command` as today | no |
| 4 | otherwise (rung 4) | `--preseedFile` template, with the DVD mirror as today | no |

### `GET /ignition.json` (family `ostype.FamilyIgnition`)

| Order | Condition | Answer | Changed? |
|---|---|---|---|
| 1 | no approved host | reboot config via `serveRebootIgnition`, **no template read** | **changed** (R4) |
| 2 | **approved host, proof fails** | **reboot config via `serveRebootIgnition`** | **new** |
| 3 | bound `butane` config (rungs 1–2) | rendered, with the node-report unit as today | no |
| 4 | otherwise (rungs 3–4) | per-host `IgnitionFile` or `--ignitionFile` template | no |

`serveFileIgnition` loses its `host == nil` branch, because its callers never pass nil any more.

**Wrong family is covered by the proof rows** (order 3 on `/machineconfig`, order 2 on the other
two). A Flatcar boot's ticket is signed for `ignition`, so at `/machineconfig` it fails proof and
gets `404`. No route has a family gate of its own.

**Order in `/machineconfig`.** Proof (3) comes after the approval rules (1–2) so that #158's and
#162's answers stay byte-identical, and before the member ARP check (4) so a member also needs
both.

**What callers get, by topology** (an approved host booting through booty):

| Caller | Today | After |
|---|---|---|
| Same VLAN as booty | served | served (ticket) |
| Same VLAN, Talos, SMBIOS serial with a space | served (ARP fallback) | served (ticket precedes the space, R12) |
| Routed VLAN, no NAT | served (`?mac=`) | served (ticket) |
| Behind NAT | served (`?mac=`) | served (ticket) |
| QEMU user-mode lab (127.0.0.1) | served (`?mac=`) | served (ticket) |
| Any of the above, with a copied, hand-written or expired URL | served | unknown-host answer |
| Cluster member off booty's L2 | `403` (ARP) | `403` (unchanged, #71) |

## 7. Issuing tickets: the boot scripts

**Templates** (`pkg/bootscript/pxe_config.go`). `mac` and `ticket` come first in every config
URL. The ticket parameter is built as `"&" + bootticket.QueryKey + "=[[ticket]]"`.

| Template | Line | Config URL after |
|---|---|---|
| `talos.ipxe` | `:10` | `.../machineconfig?mac=${mac}&ticket=[[ticket]]&uuid=${uuid}&serial=${serial}&hostname=${hostname}` |
| `flatcar.ipxe` | `:17` | `.../ignition.json?mac=${mac}&ticket=[[ticket]]` |
| `coreos.ipxe` | `:24` | `.../ignition.json?mac=${mac}&ticket=[[ticket]]` (the `set CONFIGURL` line) |
| `debian.ipxe` | `:44` | `.../preseed?mac=${mac}&ticket=[[ticket]]` |

The holding and tool templates are unchanged and never carry a ticket.

**The token has one source: `bootTokensFor`.** It gains a final `ticket string` parameter and
always sets `tokens["[[ticket]]"] = ticket`, with `""` when there is none.
- `applyTokens` would otherwise ship a literal `[[ticket]]` (`tokens.go:71-76`).
- An empty ticket fails proof, so this fails closed either way, and the rendered script never
  contains `[[`.

Signature changes, `pkg/bootscript`:

| Function | Before | After |
|---|---|---|
| `bootTokensFor` (`tokens.go:95`) | `(osToLoad, segment, arch, version, urlHost string)` | `(osToLoad, segment, arch, version, urlHost, ticket string)` |
| `bootTokens` (`tokens.go:214`) | `(osToLoad, urlHost string, host *hardware.Host)` | `(osToLoad, urlHost string, host *hardware.Host, ticket string)`; passes `ticket` to each `bootTokensFor` call |
| `assignedScript` (`tokens.go:274`) | `(host, urlHost)` | `(host *hardware.Host, urlHost string, issuer TicketIssuer)`; ticket = `ticketFor(issuer, host.MAC, osToLoad)` |
| `RenderBootScript` (`render.go:43`) | `(host, urlHost)` | `(host *hardware.Host, urlHost string, issuer TicketIssuer)`; passes `issuer` to `assignedScript` only |
| `renderMenuSelection` (`menu.go:65`) | `(filename, urlHost string)` | `(filename, urlHost, mac string, issuer TicketIssuer)`; after it parses `cacheName`, ticket = `ticketFor(issuer, mac, cacheName)` |
| `MenuSelectionScript` (`menu.go:98`) | `(host, filename, urlHost)` | `(host *hardware.Host, filename, urlHost string, issuer TicketIssuer)`; passes `host.MAC` and `issuer` (`host` is non-nil past the `bootDispatch` gate) |

Callers:
- `bootScriptHandler` (`pkg/http/bootscript.go:33`) and `menuSelectionHandler` (`:45`) pass
  their `issuer`.
- The TFTP `menu/` branch (`pkg/tftp/tftp.go:60`) passes `s.tickets`.

**The holding script never carries a ticket.** That is what stops an unapproved device:
- `bootDispatch` (`tokens.go:52`) gives an unknown or unapproved host the holding script.
- `MenuSelectionScript` gives any host that is not in menu mode the holding script.

## 8. Logging

**Refusals.** `proven` logs one Warn record per refusal:
`slog.WarnContext(r.Context(), "config refused: no valid boot ticket", "family", family, "mac", host.MAC, "err", err)`.
- `err` is `ErrMissing`, `ErrInvalid` or `ErrExpired`, and its text never contains the ticket.
- When `p == nil`, the `err` is `errors.New("no boot ticket checker configured")`.
- An expired ticket can therefore be told apart from an unknown host.

**Request lines.** The three handlers log `r.RequestURI` today (`machineconfig.go:34`,
`preseed.go:41`, `ignition.go:42`), and that would include the ticket. They log
`"path", r.URL.Path, "mac", loggableMAC(r.URL.Query().Get("mac"))` instead. Elsewhere:
- `logRequest` (`pkg/http/http.go:259-263`) already logs only the path.
- `bootScriptHandler` logs the kind and the MAC, never the script.
- The TFTP handler logs the filename and byte count, never the script.

**booty never logs a ticket** (T11). Boot clients may: Ignition logs its fetch URL on every
attempt, and that log stays on the node.

## 9. Unchanged

- **`GET /booty.ipxe` and `GET /menu/`** keep identifying the host by `?mac=` with the ARP fallback
  (`resolveBootHost`, `identifyHostWith`). They are #166's surface.
- **TFTP `menu/`** identifies the host by ARP only (`pkg/tftp/tftp.go:36-48,60`).
- **TFTP `booty.ipxe`** is a stub that identifies no one (`bootscript.StubScript`).
- **Discovery recording** (`pkg/http/discovery.go`) is untouched: the per-IP limit, the 1024-row
  cap and the 20 s debounce.
- **Cluster members:** the ARP check, and the 403 for an unapproved member.
- **`POST /node/report`:** its own per-host HMAC token and ARP match.
- **The open route set** in `TestOnlyAllowlistedRoutesAnswerWithoutACredential`
  (`pkg/http/open_routes_test.go:176`) does not change. The test's deps gain `Tickets` (§10).
- **The API preview paths** (`machineConfigPreviewVars` and the rest) render without a request,
  and are gated.

## 10. Testing

**Method.** Every regression test is shown failing before the fix
([[prove-tests-fail-before-the-fix]]):
- **Regression tests (marked "main")** are written against `main`'s current constructors:
  `ignitionHandler(store, n)`, `machineConfigHandler(store, arp)` and
  `preseedHandlerWith(store, n, arp)`.
  - The builder runs them on the unfixed tree and shows them failing.
  - In the fix, only the constructor call in each test changes, to the §5 signature with a test
    `*bootticket.Ticketer`. The assertions do not change.
- **Tests that need the new code (marked "mutation")** name a mutation of the fix. The builder
  applies it, shows the test failing, and reverts it.
- **Each rejection test has an acceptance control,** so a gate that refuses everything cannot pass.

Fixtures:
- **Approved host:** `approveHost(t, mac)`, because `WriteMacAddress` drops `Approved` (#161).
  `approveHost` leaves `boot_mode='menu'`, the DB default (`pkg/db/migrations/0001_init.sql:40`).
  - An **assigned** host also needs `hardware.SetAssignment(mac, os, "", "")`
    (`pkg/hardware/mac.go:321`).
  - A **menu** host needs nothing more.
- **Off-L2 source:** `failingARP` (`pkg/http/machineconfig_member_test.go:22`), unless a test says
  otherwise.
- **Test key:** `tk` is a `*bootticket.Ticketer` from `bootticket.New(bytes.Repeat([]byte{7}, 32))`.
- **Ticket helper:** `withTicket(t, tk, path, mac, family string) string` appends
  `QueryKey + "=" + tk.Issue(mac, family)` with `&` when `path` contains `?`, else `?`.
- **Sentinel:** a unique string in the bound config or the default template, asserted absent on
  a refusal and present on a serve.

| # | Test | Asserts | Shown failing by |
|---|---|---|---|
| T1 | `TestConfigRefusesApprovedMACWithoutTicket` (table: 3 routes × {bound config, server default}) | `?mac=<approved>`, no ticket, off-L2 → `/machineconfig` and `/preseed` give `404` without the sentinel; `/ignition.json` gives the reboot config without the sentinel | main: 200 with the sentinel (#88) |
| T1b | `TestIgnitionRebootConfigNeedsNoTemplate` | `--ignitionFile` points at a missing file; an unknown MAC, and an approved MAC with no ticket → 200 with the `booty-reboot.service` JSON | main: 500 "ignition template unavailable" (B2) |
| T2 | `TestConfigServesWithValidTicketOffL2` (the T1 table) | Acceptance control: valid ticket, off-L2 → 200 with the sentinel | mutation: make `proven` also require `requestFromMAC` → fails. Pins routed-VLAN support (M2) |
| T3 | `TestConfigRefusesTicketForAnotherMAC` | A ticket for approved host A, request `?mac=B` (B also approved) → refused on all 3 routes | mutation: drop the MAC from the signed message |
| T4 | `TestConfigRefusesTicketForAnotherFamily` | A ticket for `ignition` at `/machineconfig` and `/preseed`; a ticket for `talos` at `/ignition.json` → refused | mutation: drop the family from the signed message (#88's rung-4 hole) |
| T5 | `TestCheckExpiry` (`pkg/bootticket`, `synctest.Test`) | Issue; `time.Sleep(TTL - time.Second)` → nil; `time.Sleep(2 * time.Second)` → `ErrExpired` | mutation: skip step 6 |
| T6 | `TestCheck` (`pkg/bootticket`, table) | Valid round trip → nil. `ErrInvalid` for: wrong MAC, wrong family, a tampered `sig` char, `exp` changed by 1, `v2.` prefix, 2 parts, 4 parts, `exp` of `+123` / `0123` / `12a`, empty MAC, empty family, nil receiver, `&Ticketer{}`. `""` → `ErrMissing`. `New` with 31 and 33 bytes → error. `Issue` on nil and zero `Ticketer` → `""` | mutation per row: skip the step that row tests (for example, skip step 5 → the tampered-`sig` row fails) |
| T7 | `TestBootScriptCarriesVerifiableTicket` (table: `talos`, `flatcar`, `coreos`, `debian`) | Assigned approved host. `GET /booty.ipxe?mac=` via `bootScriptHandler(store, failingARP, tk)` → script contains `ticket=v1.` and no `[[`. Expand `${mac}` → host MAC, `${uuid}` → `00000000-0000-0000-0000-000000000000`, `${serial}` → `Default string`, `${hostname}` → `""`. Take the config URL: the text after `talos.config=`, `ignition.config.url=` or `preseed/url=` up to the first space; for coreos, the value of the `set CONFIGURL ` line. `GET` it on the route's handler with `failingARP` and `tk` → 200 | mutation: move `&ticket=` after `${serial}` in `talos.ipxe` → the talos row fails (B1); issue with the wrong family → every row fails |
| T8 | `TestMenuSelectionCarriesTicket` (`pkg/http`) | A menu-mode approved host whose `host.OS` is `flatcar`. Seed `<dataDir>/cache/talos/schemA/amd64/v1.10.5/`. `GET /menu/talos/schemA/amd64/v1.10.5/boot.ipxe?mac=` via `menuSelectionHandler(failingARP, tk)`. Expand and extract as T7, then `GET /machineconfig` → 200 | mutation: issue with `host.OS`'s family instead of the selected OS's → fails (that is why the host is Flatcar) |
| T8b | `TestTFTPMenuSelectionCarriesTicket` (`pkg/tftp`, `server_test.go` pattern) | `Start` with `ARP` returning the host's MAC and `Tickets: tk`. Fetch `menu/talos/schemA/amd64/v1.10.5/boot.ipxe` over TFTP. Its `ticket=` value passes `tk.Check(v, mac, ostype.FamilyTalos)` | mutation: pass `nil` instead of `s.tickets` → fails |
| T9 | `TestHoldingScriptHasNoTicket` | Unknown, unapproved and revoked MACs → `/booty.ipxe` script contains no `ticket=` | mutation: set `[[ticket]]` in `holdingScript` from `ticketFor` → fails |
| T10 | `TestMemberNeedsTicketAndARP` | Member + matching ARP + **no ticket** → 404 | main: serves the frozen config |
| T10b | (same test, rows needing a ticket) | Member + ticket + matching ARP → frozen config. Member + ticket + ARP mismatch → 403 | acceptance control; mutation: drop the member ARP check → the 403 row fails |
| T11 | `TestConfigLogsNeverCarryTicket` | Capture `slog`; request each route with `ticket=SENTINEL-TICKET` for an approved host → no record contains `SENTINEL-TICKET` | main: the handler logs `r.RequestURI`, which contains it |
| T11b | `TestConfigRefusalIsLogged` | No ticket, and an expired ticket → one Warn record "config refused: no valid boot ticket" with `family`, `mac` and `err` | mutation: remove the log line |
| T12 | `TestIgnitionARPFallback` | No `?mac=`; injected ARP resolves the approved host; valid ticket → served | mutation: ignore the injected `arp` (call `arpLookup`) → fails |
| T13 | `TestStartHTTPRefusesNilTickets` | `StartHTTP(APIDeps{Store: s, Auth: store, Tickets: nil})` → error; also under `NoAuth: true` | mutation: remove the refusal |
| T13b | `TestTFTPStartRefusesNilTickets` | `tftp.Start(Config{Tickets: nil, …})` → error | mutation: remove the refusal |
| T14 | `TestLoadOrCreate`, `TestLoadOrCreateLeavesNoTemp` | Moved from `pkg/nodereport/token_test.go:50,93` to `pkg/keyfile`. The temp-prefix check uses `"." + filepath.Base(path) + "-"` | moved; still pass. Mutation: hard-code a different prefix → the no-temp test fails |
| T15 | `TestRunFailsOnBadBootTicketKey` (`cmd`) | A directory at `<dataDir>/boot-ticket-key` → `run` returns an error containing `boot tickets:`, and nothing listens | pattern: `cmd/main_test.go:350` |

**Existing tests that change** (the plan lists every call site):
- **Signature changes** reach about 70 call sites across about 21 test files in `pkg/http`,
  `pkg/bootscript` and `pkg/tftp`.
- **Config-fetch tests for an approved host that expect a serve** gain `withTicket`. The files
  come from `grep -rln '/machineconfig\|/preseed\|/ignition.json' pkg/http --include='*_test.go'`,
  19 files at `904223b`.
- **Tests building `APIDeps` for `StartHTTP` or `baseMux`** gain `Tickets: tk`:
  `pkg/http/auth_test.go:575,601,623,648` and `pkg/http/open_routes_test.go:190-196`.
- **`TestStartHTTPRefusesANilStoreWithoutNoAuth`** (`auth_test.go:503-518`) gains `Tickets: tk`
  so it keeps testing the Auth refusal, and does not trip the new one.
- **The `why` strings** for the three config routes in `openRouteTable`
  (`open_routes_test.go:52-54`) say "boot path, proven by a boot ticket".
- **Tests asserting a refusal** keep their assertions.

**Gates:**
- `go build ./...`, `go vet ./...` and `go test -race ./...`, with 0 FAIL against the baseline of
  about 1,150 runs.
- `TRICORDER_DIR=/Users/acaudill/Projects/github/go-tricorder task lint:new` reports 0 issues.
- Run each measuring command on its own (booty-repo-quirks).

**Lab check (end-to-end, after the unit gates).** Boot QEMU through booty's `debian.ipxe`, per
`docs/testing/debian-dvd-install-gate.md`'s setup, with booty built from the branch. Then:
- **Pass:** booty's log has a `"preseed request"` record for the host, no
  `"config refused: no valid boot ticket"` record, and QEMU's serial console (`-nographic`)
  contains no `retrieve_error` and no "Failed to retrieve the preconfiguration file".
- **Fail:** stop and report. That is a stop class, because M4 rules out a fallback. The `&`
  handling itself is already measured (R11), so a failure would point at the booty build.

## 11. Documentation (in this change)

**Repoint `#164` to #88:** `.claude/CLAUDE.md:155`, `docs/CONFIGURATION.md:135` and
`docs/releases/UNRELEASED.md:6`. Each sentence is rewritten, because the residual it states is
fixed here; the #166 residual replaces it.

| File | Change |
|---|---|
| `.claude/CLAUDE.md` | **Security invariants:** the three config routes serve only a request carrying a valid boot ticket for that host and family. "Known residual (#164)" becomes the #166 residual: deliberate `?mac=` spoofing at `/booty.ipxe` or `/menu/` mints a ticket. **`:158-160`:** firmware and installers carry no credential of their own; config URLs carry the ticket booty put in the boot script. **"How a boot works", step 3:** config URLs carry `ticket=`. **Repository map:** add `pkg/bootticket` and `pkg/keyfile`. |
| `docs/releases/UNRELEASED.md` | **`:5`:** "must now be approved to get their config" becomes "must be approved and boot through booty". **`:6`:** rewrite the #164 sentence. **New breaking entry:** config URLs need a booty-issued ticket (1 h). A copied, hand-written or stale config URL gets the unknown-host answer. A machine mid-boot during the upgrade fails once: Flatcar recovers by itself, and Talos and Debian need a reboot. New `<dataDir>/boot-ticket-key`. The `/ignition.json` reboot config no longer needs the template file. Closes #88. |
| `docs/CONFIGURATION.md` | **§Boot config precedence (`:129-136`):** replace the #164 sentence with the ticket rule, the 1 h lifetime, the per-route answer on refusal, the reboot remedy for an expired ticket, key rotation (delete `boot-ticket-key`, restart), and the #166 residual. **`:574-577`:** the boot-facing routes need no credential, and the config routes need a boot ticket. |
| `docs/schema/API.md` | **Rows `:16`, `:19`, `:20`, and `:290`, `:462`:** each config route requires the `ticket` query parameter booty puts in the boot script, and without a valid one gets the answer listed. **`:542`:** the boot contract changes in this release: config URLs carry `ticket`. |
| `deploy/README.md` | **§Security posture (`:76-119`):** add the residual. A LAN client that deliberately asks `/booty.ipxe?mac=<approved MAC>` can still fetch that host's config, join string included (#166). |
| `README.md` | **`:83-90`:** config URLs carry a short-lived ticket, so only a booty-rendered boot fetches them. **`:168`:** the same correction as `CONFIGURATION.md:574-577`. |
| `docs/testing/debian-dvd-install-gate.md` | **`:264` and `:310`:** a hand-written `preseed/url` is now refused. The runbook gets the URL from booty instead: run `curl -sS 'http://localhost:8088/booty.ipxe?mac=52:54:00:12:34:56'`, copy the `preseed/url=` value, replace the literal `${mac}` with `52:54:00:12:34:56`, and use it within 1 hour. |

## 12. Follow-ups

- **#166 (filed 2026-10-16):** deliberate spoofing at `/booty.ipxe` and `/menu/`. Candidates:
  - an ARP match before issuing when on-L2;
  - a credential the discovery OS enrolls (#130);
  - iPXE client certificates over HTTPS.

  The seams in R6 are where any of them plugs in.
- **Not filed:**
  - a `--bootTicketTTL` flag (M6 declined it);
  - `${serial:uristring}` / `${hostname:uristring}` in `talos.ipxe` (R12; the non-goal);
  - `Cache-Control: no-store` on ticketed scripts (iPXE does not cache; no present consumer).

## 13. Review record

- **2026-10-16, brainstorming** with the maintainer (§3, M1–M5).
- **Research:**
  - background research on the OS fetch behaviour (R3, R9, R10, R11);
  - a BusyBox `set` quoting check;
  - the Debian trixie QEMU boot that settles R11.
- **2026-10-16, Gate 1 (`sr-eng-review`):** 2 Blocking, 7 Significant, 8 Minor. All addressed in
  this revision:
  - **B1, the ticket after `${serial}`:** R12, §7 and T7.
  - **B2, the ignition 500 on refusal:** R4, §6 and T1b.
  - **S1, the zero-value `Ticketer`:** §4 step 1 and T6.
  - **S2, the nil interface and the TFTP panic:** §5 steps 2, 3 and 5, and T13b.
  - **S3, the TTL cost and the missing refusal log:** M6, R3, §8 and T11b.
  - **S4, the Debian lab gate:** R11, measured, and the §10 lab check with its pass and fail
    criteria.
  - **S5, the test method:** the §10 method, T5, T7, T8 and T8b.
  - **S6, the existing tests:** §10 "Existing tests that change", R5's temp prefix, `withTicket`.
  - **S7, the docs:** §11, and §9's TFTP correction.
  - **Minor items:** anchors corrected, R8 `FamilyTool` and `ConfigKind`, §4 parsing rules,
    §5.1 unconditional load, §8 "booty never logs", the R3 legacy-GRUB note, and the §12
    Cache-Control call.
- **Gate 1 could not verify, and this design still marks UNVERIFIED:**
  - Talos's COSI restart after 3 h;
  - the Flatcar 300 s reboot;
  - FCOS emergency behaviour;
  - Debian bookworm end-to-end;
  - how common SMBIOS serials with spaces are;
  - Talos's `ConfigSource`;
  - DUID leases.
