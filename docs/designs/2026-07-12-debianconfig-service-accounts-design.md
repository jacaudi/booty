# Design — `debianconfig` service-account ergonomics

**Date:** 2026-07-12
**Status:** approved (brainstorming)
**Branch:** `worktree-debianconfig-svc`, stacked on `worktree-debianconfig` (PR #56)
**Kind:** additive schema + generator change to the `debianconfig` config kind.

## Problem

Authoring a **key-only service account that can sudo** — the common "provision a
box, log in by key, run privileged automation" pattern — is clumsy today:

- `accounts.user.password_hash` is **required**, so a key-only account still
  needs a hand-generated password hash it will never use.
- Granting sudo means hand-writing a `late_command` (`usermod -aG sudo` + a
  `/etc/sudoers.d` drop-in), and the passwordless variant is a POSIX-`sh`
  quoting trap (the obvious `<<<` here-string is a bashism that fails under d-i's
  `sh` — found in lab testing).
- `late_command` is a block scalar only; a YAML list reads better and removes
  the "each line must be independently sequenceable" newline-flatten ambiguity.

## Approved decisions

### D1 — `password_hash` optional → locked password + key required
`accounts.user.password_hash` becomes optional (was required-when-a-user-is-present).
- **Omitted** → booty emits `d-i passwd/user-password-crypted password *`
  (a locked crypt sentinel: no password login, key-only). Chosen over a *random*
  password because `renderConfig` runs on every preview/serve — a fresh random
  hash each render would be **non-deterministic** (breaks byte-exact goldens and
  reproducible installs). `*` is deterministic and semantically clear.
- **Fail-closed:** omitting `password_hash` **requires** a non-empty
  `ssh_authorized_keys` for that user (otherwise the account is unreachable) →
  validation error (422).
- **Provided** → unchanged (existing behavior/goldens).

A *retrievable* random break-glass password (generate-once-at-create, store,
surface) was considered and **deferred** — it needs revision storage + API
surfacing and is a separate feature.

### D2 — new per-user `sudo:` field (tri-state, default none)
`accounts.user.sudo` accepts:
- `nopasswd` → ensure `sudo` in packages; append to the composed `late_command`
  two commands (POSIX `sh`, not a bashism):
  `in-target usermod -aG sudo <user>` **and**
  `in-target sh -c 'printf "%s\n" "<user> ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/<user>'`,
  followed by `in-target chmod 440 /etc/sudoers.d/<user>`. The single-quoted
  `sh -c` body keeps the `(ALL)` parens and spaces literal, `>` is a real
  redirect, and `printf` avoids the lab-failed `<<<` here-string. `chmod 440` is
  a separate command (files are root-owned by default — `in-target` runs as
  root). `validateUsername`'s charset (`^[a-z_][a-z0-9_-]*$`) excludes `.` and
  `~`, so `/etc/sudoers.d/<user>` is never a filename `sudo` silently ignores.
  Keeping `usermod -aG sudo` for `nopasswd` (redundant given the explicit
  drop-in) is a conscious path-uniformity choice across both modes (F7).
- `password` → ensure `sudo` in packages; append `in-target usermod -aG sudo <user>`
  only (interactive sudo prompts for the user's password). **Coherence:** requires
  a `password_hash` — `sudo: password` on a locked (password-omitted) account is
  rejected (422), since the user could never authenticate sudo.
- omitted / `false` → no sudo.

**Explicitly a separate field**, not implicitly coupled to an empty password:
authentication (key vs password) and authorization (sudo) are independent, and
auto-granting passwordless root from an *absent* password field is a footgun.

`sudo` is parsed by a small custom unmarshaler with a **fully-closed input
matrix** (F3):
- **absent** (field never present) → none — the `Sudo` zero value encodes none;
  `UnmarshalYAML` is never called for an absent field.
- **null** (`sudo:` / `sudo: ~`) → none (ergonomic sibling of "absent").
- YAML bool `false` → none; `true` → `nopasswd` (friendly alias).
- YAML string `nopasswd` / `password` → as named.
- anything else — empty string `""`, a number, a sequence, a mapping → 422.

(DRY: `sudo`'s string-or-bool unmarshaler and `late_command`'s string-or-list
unmarshaler share *shape*, not *knowledge* — different value spaces and rules —
so they stay two small separate types; unifying them would be premature.)

**Injection safety:** the `<user>` interpolated into the `usermod` command and the
sudoers drop-in is the *already-validated* username (existing `validateUsername`,
`^[a-z_][a-z0-9_-]*$`, length-bounded) — no new shell-injection surface.

### D3 — `late_command` accepts a list OR a block string
`late_command` becomes a `stringOrList` type via a custom unmarshaler: a YAML
sequence (`[]string`) or the existing block scalar. Both normalize to the same
`;`-joined flatten used today. Existing block-string configs are **byte-identical**
(back-compat).

### D4 — `openssh-server` auto-added when a user has ssh keys
If any user declares `ssh_authorized_keys`, booty ensures `openssh-server` is in
the emitted `pkgsel/include` (deduped if the operator also listed it) — ssh keys
are useless without sshd. **Behavior-change note:** this changes emitted output
for any *existing* config that had ssh keys but did not list `openssh-server`;
that is intended, and the affected goldens update.

**Package composition (D4 + D2 together, F1).** Two packages can be auto-added:
`openssh-server` (when a user has ssh keys) and `sudo` (when `sudo != false`).
The exact rule — pinned so `pkgsel/include` bytes are deterministic:
1. The operator's `packages` list is emitted **verbatim** — never reordered,
   never generally deduped (protects back-compat byte-identity).
2. Each auto-package is appended **only if absent** from the operator list, in a
   **fixed order**: `openssh-server` then `sudo`. (Dedup is scoped to this
   append; a config that already lists a package sees no change.)
3. If the spec has no `packages` at all, the auto-adds create the
   `pkgsel/include` line (which would otherwise not be emitted).

Order within `pkgsel/include` is install-irrelevant (d-i treats it as a set);
the fixed order exists only to keep goldens deterministic.

### D5 — composed `late_command` ordering
`ssh-keys → sudo-setup → ESP-sync (mirror only) → operator's own`. The sudo
fragment is booty-generated account setup, grouped with the ssh-keys fragment and
placed before disk (ESP-sync) and operator commands, so an operator `late_command`
can assume the account + sudo are already configured.

## Schema (after)

```yaml
accounts:
  root_password_hash: "$6$…"      # optional; omit → root login disabled
  user:
    fullname: Service Account     # optional; defaults to username
    username: svc                 # required
    password_hash: "$6$…"         # OPTIONAL now; omit → locked (*), key required
    ssh_authorized_keys: ["ssh-ed25519 …"]   # required if password_hash omitted
    sudo: nopasswd                # optional: nopasswd | password | false(default)
packages: [qemu-guest-agent]      # openssh-server auto-added when a user has keys;
                                  # sudo auto-added when sudo != false
late_command:                     # block scalar OR a YAML list
  - in-target systemctl enable ssh
```

## Non-goals

- Retrievable/break-glass random password (D1 deferred).
- Per-user secondary groups beyond `sudo`, `NOPASSWD` scoping to specific commands,
  password-aging/lock policy, or multiple users (schema is single-user today).
- Any change to disk/network/mirror behavior.

## Backward compatibility

Existing `debianconfig` goldens stay byte-identical **except** configs that have
`ssh_authorized_keys` but no `openssh-server` in packages (D4 adds it). New
behaviors (locked password, `sudo`, list `late_command`) fire only on the new
inputs. No migration; no API/DB shape change (the source is opaque YAML).

**Exact D4 golden impact** (verified against the current test file — the plan
must update precisely these, and no others):
- `TestTranslateDebianConfigSSHKeysLateCommand` — full `got == want`; ssh keys,
  no `openssh-server` → **changes** (gains a `d-i pkgsel/include string
  openssh-server` line, emitted by the template *before* the `late_command`
  line). Update the golden.
- `TestTranslateDebianConfigEscapeHatchOrdering` — full `got == want`; same
  shape → **changes** the same way (new `pkgsel/include` line lands between the
  user block and the composed `late_command` line). Update the golden.
- `TestTranslateDebianConfigSSHThenESPSyncOrder` — its output *also* changes, but
  it asserts `strings.Contains(got, <late_command line>)`, so it **still passes
  unchanged** — do **not** rewrite it (leave the Contains assertion as-is).
- `TestTranslateDebianConfigCombinedEndToEnd` — already lists `openssh-server`,
  so D4's dedup skips the auto-add → **unchanged** (it exercises the dedup path).

## Validation

Checks run in this **fixed order** (F5) so the surfaced 422 message is
deterministic when an input is invalid in more than one way:

1. **username** — `validateUsername` (empty or malformed) → 422. The message
   drops the now-false "and password_hash" (F2): the empty-username arm becomes
   `accounts.user requires a username`; the malformed case keeps its existing
   `invalid accounts.user.username …` message.
2. **reachability (D1)** — user present + no `password_hash` + no
   `ssh_authorized_keys` → 422, message
   `accounts.user requires password_hash or ssh_authorized_keys`. This
   **replaces** the old unconditional `PasswordHash == ""` → error at
   `buildPreseedView` (whose string also read "requires username and
   password_hash", F2).
3. **sudo coherence (D2)** — `sudo: password` + no `password_hash` → 422
   (`sudo: password requires accounts.user.password_hash` — can't authenticate
   sudo on a locked account).
4. **sudo value** — outside {`nopasswd`, `password`, `false`, `true`, null,
   omitted} → 422 (surfaced at unmarshal time, before the above).

Existing `validateUsername` / ssh-key validation are otherwise unchanged and now
also guard the sudo interpolation. The test
`TestTranslateDebianConfigUserRequiresUsernameAndHash` is renamed/re-commented
to match the reworded messages (its assertions still pass, for the new reasons).

## Testing & lab-validation

- **Unit (byte-exact goldens):** password-omitted+key+`sudo:nopasswd` (full service
  account); `sudo:password`; `late_command` as a list == same block-string output;
  openssh-server auto-add + dedup; and the two validation errors. Assert existing
  goldens unchanged except the D4 openssh-server cases.
- **Lab (end-to-end, per the mirror-fix precedent):** install a single Debian
  server from a `{user: svc, ssh key, sudo: nopasswd, no password}` config booty
  serves; the install must complete **fully unattended with the `*` locked
  password — no interactive password prompt** (F6); boot it; SSH in with the
  key; confirm `sudo -n true` succeeds (passwordless) and password login is
  refused.

## Files touched

- `pkg/http/debiangen.go` — `debianUser` (`PasswordHash` optional, add `Sudo`);
  `debianConfigSpec.LateCommand` → `stringOrList`; two custom unmarshalers;
  `buildPreseedView` (locked-password + require-key, sudo composition, openssh-server
  auto-add, ordering).
- `pkg/http/debiangen_test.go` — new goldens + unchanged-existing assertions.
- `docs/CONFIGURATION.md`, `docs/schema/API.md` — document the new fields/behavior.

## Design review (sr-go-engineer)

Verdict **AMEND-BEFORE-PLANNING** — 0 approved decisions defective; D1–D5 stand.
All 5 load-bearing constraints (POSIX-`sh`, injection-safety, back-compat
byte-identity, deterministic output, generator-only scope) verified clean, and
the D1×D2 coherence matrix (12 cells) re-walked complete. Findings folded:
**F1** package composition rule (see *Package composition*); **F2** reworded the
two now-false "requires … password_hash" error strings + ordered Validation;
**F3** fully-closed `sudo:` input matrix; **F4** pinned the concrete POSIX-`sh`
sudoers form + `chmod 440` + the `.`/`~`-excluded filename invariant; **F5**
fixed validation order; **F6** unattended-`*`-install lab assertion; **F7**
`usermod` kept for `nopasswd` as a documented conscious choice. The exact D4
golden impact was verified against the current test file and corrected (the
`SSHThenESPSyncOrder` `Contains` assertion survives unchanged) — see *Backward
compatibility*.
