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
- `nopasswd` → ensure `sudo` in packages; append to the composed `late_command`:
  `in-target usermod -aG sudo <user>` **and** an `in-target sh -c` that writes a
  `440` `/etc/sudoers.d/<user>` containing `<user> ALL=(ALL) NOPASSWD:ALL` (via
  POSIX `printf`, not a bashism).
- `password` → ensure `sudo` in packages; append `in-target usermod -aG sudo <user>`
  only (interactive sudo prompts for the user's password). **Coherence:** requires
  a `password_hash` — `sudo: password` on a locked (password-omitted) account is
  rejected (422), since the user could never authenticate sudo.
- omitted / `false` → no sudo.

**Explicitly a separate field**, not implicitly coupled to an empty password:
authentication (key vs password) and authorization (sudo) are independent, and
auto-granting passwordless root from an *absent* password field is a footgun.

`sudo` is parsed by a small custom unmarshaler that accepts either a YAML string
(`nopasswd`|`password`) or a YAML bool (`false`→none; `true`→`nopasswd` as a
friendly alias); any other value is a validation error.

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

## Validation

- user present + no `password_hash` + no `ssh_authorized_keys` → 422.
- `sudo: password` + no `password_hash` → 422 (can't authenticate sudo on a locked account).
- `sudo` not in {`nopasswd`, `password`, `false`, `true`, omitted} → 422.
- Existing `validateUsername` / ssh-key validation unchanged and now also guard
  the sudo interpolation.

## Testing & lab-validation

- **Unit (byte-exact goldens):** password-omitted+key+`sudo:nopasswd` (full service
  account); `sudo:password`; `late_command` as a list == same block-string output;
  openssh-server auto-add + dedup; and the two validation errors. Assert existing
  goldens unchanged except the D4 openssh-server cases.
- **Lab (end-to-end, per the mirror-fix precedent):** install a single Debian
  server from a `{user: svc, ssh key, sudo: nopasswd, no password}` config booty
  serves; boot it; SSH in with the key; confirm `sudo -n true` succeeds
  (passwordless) and password login is refused.

## Files touched

- `pkg/http/debiangen.go` — `debianUser` (`PasswordHash` optional, add `Sudo`);
  `debianConfigSpec.LateCommand` → `stringOrList`; two custom unmarshalers;
  `buildPreseedView` (locked-password + require-key, sudo composition, openssh-server
  auto-add, ordering).
- `pkg/http/debiangen_test.go` — new goldens + unchanged-existing assertions.
- `docs/CONFIGURATION.md`, `docs/schema/API.md` — document the new fields/behavior.
