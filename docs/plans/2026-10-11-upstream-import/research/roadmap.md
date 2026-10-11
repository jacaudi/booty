# booty refocus: research for the Talos-first roadmap

Date: 2026-10-11. Repo: /home/user/booty (fork jacaudi/booty, HEAD f36482c). Upstream: jeefy/booty (upstream/main 8a5a227, 2026-10-08).

How this was checked: I read the fork's source and the Talos machinery source in GOMODCACHE. I pulled Go modules from proxy.golang.org into a scratch module cache and read them, and I compiled a test binary. I shallow-cloned the public GitHub repos into `scratchpad/repos/` and read them.

Network limits: the session proxy returned 403 for most documentation sites (developer.1password.com, developer.hashicorp.com, openbao.org, talos.dev, docs.siderolabs.com, pve.proxmox.com, wiki.archlinux.org, archlinux.org, truenas.com, extensions.flatcar.org, tails.net and others). Where a site's docs live in a Git repo, I read that repo instead: hashicorp/web-unified-docs, openbao/openbao `website/`, siderolabs/docs, proxmox/pve-installer and pve-docs, truenas/documentation, archlinux/archiso. Every claim below that I could not confirm is marked **UNVERIFIED**.

---

## 1. How the fork stores Talos cluster secrets today

Files read: `pkg/config/secretskey.go`, `pkg/http/clustersecrets.go`, `pkg/http/clustergen.go`, `pkg/db/clusters.go`, `pkg/db/migrations/0005_clusters.sql`, `pkg/http/api_clusters.go`, `pkg/http/machineconfig.go`, `pkg/http/http.go`.

**What is stored**

- `clusters.secrets_enc` (BLOB NOT NULL) holds the whole Talos `secrets.Bundle`, marshalled with `go.yaml.in/yaml/v4`. That is the same YAML as `talosctl gen secrets`, so an export is talosctl-compatible (`marshalBundle`/`unmarshalBundle` in clustergen.go).
  - Greenfield clusters get the bundle from `secrets.NewBundle(secrets.NewFixedClock(2020-01-01T00:00:00Z), contract)` (`mintBundle`). The fixed clock exists so regeneration is reproducible.
  - Imported clusters get it from `secrets.NewBundleFromConfig(...)`, using the first imported config (api_clusters.go:~760).
- `cluster_node_configs.config_enc` holds each member's fully rendered, frozen machine config. It is a separate ciphertext, and a control-plane config contains every CA private key (see section 2).
  - The same row stores `sha256` of the **plaintext** config in clear. That reveals only whether two configs are equal, but the hash is computed over secret material, so note it.

**How it is encrypted**

- The cipher is `filippo.io/age` v1.3.1 X25519. One operator identity is set by `--secretsKey` (a path to `age-keygen` output). It encrypts to its own recipient, so a single key both encrypts and decrypts (`encryptSecrets`/`decryptSecrets` in `pkg/http/clustersecrets.go`).
- It fails closed:
  - If `--secretsKey` is unset, every cluster operation returns `config.ErrNoSecretsKey`, which the API surfaces as a 422.
  - If the key is set but malformed, startup is refused (`ValidateSecretsKey`).
- `LoadSecretsIdentity()` re-reads and re-parses the key file on every call. There is no caching, so the plaintext identity sits on disk next to the SQLite database.

**Where it is decrypted** (all in-process, in the booty server)

| Call site | Purpose |
|---|---|
| `api_clusters.go:259` (export endpoint) | Returns the plaintext `secretsYaml` to an authenticated API caller |
| `api_clusters.go:398` `addMember` | Decrypts the bundle, then `generateNodeConfig` → re-encrypts the result (`freezeAndBind` → `encryptSecrets`) |
| `machineconfig.go:199` `serveFrozenNodeConfig` | Decrypts the frozen config on **every** `GET /machineconfig` from a member |

**Key security finding: the serving path matters more than at-rest storage.**

`/machineconfig` is mounted on the plain, unauthenticated base mux (`pkg/http/http.go:38`). The host is identified by the client-supplied `?mac=` query parameter, falling back to ARP. The iPXE script passes `mac=${mac}` (`pkg/tftp/pxe_config.go:22`).

So anyone on the boot network who knows or guesses a control-plane member's MAC receives the decrypted control-plane config. That config contains the OS CA key, Kubernetes CA key, etcd CA key, aggregator CA key, service-account key and secretbox key. That is total cluster compromise.

Moving the at-rest key into 1Password or Vault does not change this. The serving path needs its own controls, for example:

- one-shot or short-TTL per-boot tokens in the `talos.config=` URL;
- only serving a CP config to an IP/MAC pair that netbooted moments ago;
- Talos's OAuth2 device flow for `talos.config` (siderolabs/docs `public/talos/v1.13/security/machine-config-oauth.mdx`).

  Doc inconsistency: that page names the kernel args `talos.config.oauth.*`, while `reference/kernel.mdx` heads the section `talos.config.auth.*`. Check against the Talos source before implementing.

---

## 2. The Talos secrets bundle (machinery v1.13.0)

Source: `/root/go/pkg/mod/github.com/siderolabs/talos/pkg/machinery@v1.13.0/config/generate/secrets/secrets.go`, `bundle.go`, and `../generate/{init,controlplane,worker}.go`, `../types/v1alpha1/v1alpha1_validation.go`.

The latest machinery on the Go proxy is **v1.14.2** (2026-09-29). v1.15.0-alpha.0 also exists. The fork pins v1.13.0.

```go
type Bundle struct {
    Clock      Clock       // not serialized
    Cluster    *Cluster    // ID, Secret            (discovery service identity/encryption)
    Secrets    *Secrets    // BootstrapToken, AESCBCEncryptionSecret, SecretboxEncryptionSecret
    TrustdInfo *TrustdInfo // Token                 (trustd join token)
    Certs      *Certs      // Etcd, K8s, K8sAggregator (cert+key), K8sServiceAccount (key only), OS (cert+key)
}
```

`populate()` generates any missing CA: etcd CA, Kubernetes CA, aggregator CA, and the service-account key (RSA or ECDSA depending on the version contract). `CAValidityTime` is 87600h (10 years).

**What lands in each role's config** (`init.go` is used for both init and controlplane, because `controlPlane()` calls `in.init()` and only changes the type; `worker.go` is used for workers):

| Bundle field | Control plane (init.go) | Worker (worker.go) |
|---|---|---|
| Certs.OS (Talos API CA) | `machine.ca` **crt+key** | `machine.ca` **crt only** |
| TrustdInfo.Token | `machine.token` | `machine.token` |
| Cluster.ID / Secret | `cluster.id` / `cluster.secret` | same |
| Certs.K8s (cluster CA) | `cluster.ca` **crt+key** | `cluster.ca` **crt only** |
| Certs.K8sAggregator | `cluster.aggregatorCA` crt+key | absent |
| Certs.K8sServiceAccount | `cluster.serviceAccount` key | absent |
| Certs.Etcd | `cluster.etcd.ca` crt+key | absent |
| Secrets.BootstrapToken | `cluster.token` | `cluster.token` |
| Secrets.SecretboxEncryptionSecret / AESCBC | `cluster.secretboxEncryptionSecret` (or aescbc) | absent |

Talos validation enforces this split (`v1alpha1_validation.go` lines 149–178, 441):

- On controlplane: "issuing CA key is required for controlplane nodes (.machine.ca.key)".
- On workers: "issuing Talos API CA key is not allowed on non-controlplane nodes" and "cluster CA key is not allowed on non-controlplane nodes (.cluster.ca)".

**Implication for Vault PKI.** Talos control-plane nodes must hold the CA **private keys** in their config, because trustd, apiserver and etcd issue certificates locally. So a Vault/OpenBao PKI engine cannot keep these keys non-exportable. At most it could:

- act as an offline root that signs a Talos intermediate whose key is exported once (OpenBao PKI docs say the private key "can only be exported at generation time": `website/content/docs/secrets/pki/considerations.mdx:57`); or
- issue admin `talosconfig` client certificates.

Either way the key still ends up in the served CP config. The useful pattern is to **protect the bundle**, not to replace it: use KV for storage or Transit for envelope encryption (section 4). Worker configs carry no CA keys, but they do carry the bootstrap token, the trustd token and the cluster secret, which together are enough to join a node.

---

## 3. 1Password Go SDKs

### onepassword-sdk-go (github.com/1password/onepassword-sdk-go)

- **Version:** v0.4.1, published 2026-07-30. This is `@latest` on proxy.golang.org; the previous release was v0.4.1-beta.1. The module is pre-1.0.
- **License:** MIT (the module's `LICENSE`).
- **Auth:**
  - `onepassword.NewClient(ctx, WithServiceAccountToken(tok), WithIntegrationInfo(name, ver))` (`client_builder.go:18,72,80`).
  - `WithDesktopAppIntegration(account)` also exists, but it needs cgo (see below).
  - 1Password Connect is not supported by this SDK; its README points to connect-sdk-go instead.
- **Operations** (`secrets.go`, `items.go`):
  - `Secrets().Resolve(ctx, "op://vault/item/field")`, `ResolveAll`, `ValidateSecretReference`, `GeneratePassword`.
  - `Items().Create / CreateAll / Get / GetAll / Put (update) / Delete / DeleteAll / Archive / List`, plus `Items().Files()` (attachments) and `Items().Shares()`.
  - Vault and group management are also available. Environments read is beta (README, per pkg.go.dev).
- **WASM core, and whether it needs cgo:**
  - The module embeds `internal/wasm/core.wasm` (9,509,477 bytes) via `//go:embed`. It runs that core with `github.com/extism/go-sdk` v1.7.1 on `github.com/tetratelabs/wazero` v1.11.0, which is a pure-Go runtime (`internal/extism_core.go`).
  - cgo is only needed for the desktop-app path. `client_builder_no_cgo.go` and `internal/shared_lib_core_no_cgo.go`, built under `//go:build !cgo && (darwin || linux)`, return an error for it.
  - **Verified:** a test program using `WithServiceAccountToken` and the openbao api built with `CGO_ENABLED=0`. `file` reports "statically linked", and at runtime the client rejected a bogus token in the expected way ("service account token had invalid format").
  - So it is **compatible with static builds**.
  - Costs: about +9.5 MB of binary, and `NewClient` took about 3.7 s in this container (WASM compile). Create the client once and reuse it.
  - Requires Go ≥1.24 (`go.mod`). booty uses Go 1.26.1, so that is fine.
  - The FFI message limit is about 50 MB (`extism_core.go` comment), far above a roughly 10 KB bundle.
- **Fit:** the natural shape is one 1Password item per cluster, holding the `secrets.yaml` bundle as a concealed field or file attachment, with `op://` references stored in booty's database instead of ciphertext.
  - Item field size limits and service-account rate limits: **UNVERIFIED**, because developer.1password.com was blocked.

### connect-sdk-go (github.com/1Password/connect-sdk-go)

- **Version:** v1.5.3, tagged 2023-07-28. It is the latest on the proxy; the repo's last commit is 2026-09-07, so it is maintained but has had no release since 2023.
- **License:** MIT.
- **Code:** pure Go REST client (no WASM).
- **Auth:** `OP_CONNECT_HOST` and `OP_CONNECT_TOKEN` (`connect/client.go:66-87`).
- **API:** `GetVault*`, `GetItem*`, `CreateItem`, `UpdateItem`, `DeleteItem*`, files, `LoadStruct*`.
- **Requirement:** a self-hosted 1Password Connect server (README).
- **Fit:** smaller and simpler, but it adds a server to run. Choose it when the operator already runs Connect, for example alongside the 1Password k8s operator.

---

## 4. OpenBao and HashiCorp Vault Go clients

| Module | Latest (proxy) | License (module LICENSE file) | Go |
|---|---|---|---|
| github.com/openbao/openbao/api/v2 | v2.7.1 (2026-10-01) | MPL-2.0 | 1.25 |
| github.com/hashicorp/vault/api | v1.23.0 (2026-03-23) | **MPL-2.0** ("Copyright IBM Corp. 2016, 2025 / Mozilla Public License, version 2.0") | 1.24 |
| github.com/hashicorp/vault-client-go | v0.4.3 (2023-12-18) | MPL-2.0 | — (beta, no release since 2023) |

**BSL question.** The Vault repo root `LICENSE` is BUSL 1.1 (Licensed Work: "Vault Version 1.15.0 or later", licensor IBM). However, `api/LICENSE` and `sdk/LICENSE` in the same repo are MPL-2.0 (read with `git show HEAD:api/LICENSE` in a clone of hashicorp/vault). The BSL therefore does not apply to importing `github.com/hashicorp/vault/api`. It applies to the server, which booty does not embed. HashiCorp's own FAQ answer could not be read (the page rendered questions only), so the FAQ wording is **UNVERIFIED**; the license files themselves are authoritative.

**Can one client talk to both OpenBao and Vault?** Yes for the core engines:

- The OpenBao api still sends `X-Vault-Token` (`client.go:66 AuthHeaderName`).
- It reads `BAO_*` environment variables and falls back to `VAULT_*` (`env.go ReadBaoVariable` → `UpstreamVariableName`).
- KV v2 and Transit use the same HTTP paths in both products' API docs.

Recommendation: use `openbao/api/v2` (truly open, MPL, actively released) as the single client, and test it against Vault. Vault Enterprise-only features (namespaces semantics, managed keys) may diverge; that is **UNVERIFIED** in practice.

**APIs to use:**

- **KV v2.** `client.KVv2(mount).Put/Get/GetVersion/Patch/Delete/GetMetadata` (`kv_v2.go:116-377`). Store `secrets.yaml` per cluster, with versioning for rotation history.
- **Transit (envelope encryption).**
  - Endpoints: `POST /transit/encrypt/:name`, `/transit/decrypt/:name`, `/transit/datakey/:type/:name`, `/transit/datakeys/:type/:name` (Vault v2.x API docs `content/vault/v2.x/content/api-docs/secret/transit.mdx:802-1271`). An envelope-encryption guide exists at `docs/secrets/transit/envelope-encryption.mdx`, and OpenBao's transit docs describe datakey generation (`website/content/docs/secrets/transit/index.mdx:26`).
  - No typed helper exists; call `client.Logical().Write("transit/datakey/wrapped/<key>", ...)`.
  - Pattern: request a data key, age/AES-GCM-encrypt the bundle locally, store `{ciphertext, wrapped_dek}` in SQLite, and unwrap via `transit/decrypt` when needed.
  - This drop-in replaces the age `--secretsKey` behind the same `encryptSecrets`/`decryptSecrets` seam, and the key never leaves Vault.
- **PKI.** See section 2: it is not a substitute for the Talos CA keys. It could optionally sign an intermediate or issue admin client certs.

**Design note.** The existing seam (`encryptSecrets`/`decryptSecrets` + `ErrNoSecretsKey`) is a good place for a `SecretsBackend` interface with age, transit, kv and 1Password implementations. Frozen node configs (`config_enc`) need the same backend, and decrypt runs on every `/machineconfig` GET. For a remote KMS that means a network round-trip per boot, so cache the plaintext in memory with a TTL, or decrypt once per boot token.

---

## 5. KubeSolo (Portainer) on Flatcar via systemd-sysext

- **What it is:** a single-node Kubernetes distribution: "Ultra-lightweight, OCI-compliant, single-node Kubernetes, under 200 MB RAM" (github.com/portainer/kubesolo README).
  - It removes etcd, leader election and overlay networking.
  - It bundles containerd, crun, CoreDNS and Kine (SQLite).
  - It schedules through a `NodeSetter` admission webhook.
  - `go.mod` builds on `k8s.io/kubernetes v1.35.7` with `replace` directives to the **k3s-io** Kubernetes and containerd forks (`v1.35.7-k3s1`, `k3s-io/containerd/v2 v2.2.5-k3s2`) and `k3s-io/kine v0.16.3`. So it is k3s-derived at the dependency level, not a k3s binary.
  - License: MIT.
- **Current release:** v1.2.1 (released 29 Sep 2026; tag confirmed via `git ls-remote`). v1.2.0 (24 Aug 2026) tracks Kubernetes v1.35.7.
- **Install:**
  - `install.sh` downloads `kubesolo-<ver>-linux-<arch>[-musl][-offline].tar.gz` from GitHub releases, verified against `SHA256SUMS`.
  - It installs to `/usr/local/bin/kubesolo` and writes `/etc/systemd/system/kubesolo.service` (`KillMode=process`, `Delegate=yes`, `Restart=always`).
  - Configuration lives in `/etc/kubesolo/config.yaml`; the admin kubeconfig is at `/var/lib/kubesolo/pki/admin/admin.kubeconfig`.
  - **It refuses to install if Docker is present or running** (`install.sh:44-60`). Flatcar ships Docker, and the Docker sysext is enabled by default on recent Flatcar. A KubeSolo node must disable or mask docker (Flatcar's `docker.socket`, and possibly removing the docker sysext link); the exact Flatcar steps are **UNVERIFIED**. The check may be an install-script check only, not a runtime conflict; KubeSolo's runtime behavior is **UNVERIFIED**.
- **Flatcar sysext:** **none exists.**
  - flatcar/sysext-bakery (commit 445ccc9, 2026-09-08) has 35 extensions: bird, btop, chrony, cilium, cloud-hypervisor, coder, consul, containerd, crio, dataplaneapi, docker-buildx, docker-compose, docker, falco, haproxy, ig, **k3s**, kata-containers, keepalived, **kubernetes**, llamaedge, nebula, nerdctl, nomad, nvidia-runtime, ollama, opkssh, **rke2**, scx, tailscale, tilde, vault, wasmcloud, wasmedge, wasmtime.
  - It has no kubesolo and no k0s; `grep -ri kubesolo` returns nothing.
  - Building one is straightforward: copy `k3s.sysext/create.sh`. It downloads one static binary into `${sysextroot}/usr/local/bin/` and symlinks kubectl/ctr/crictl. A kubesolo.sysext would also ship `usr/local/lib/systemd/system/kubesolo.service`.
  - booty could build the sysext itself, or point at a bakery fork's release.
- **Ignition delivery** (bakery `docs/k3s.md`):
  - Butane `storage.files` downloads `/opt/extensions/<name>/<name>-<ver>-x86-64.raw` and a sysupdate conf `/etc/sysupdate.<name>-<minor>.d/<name>-<minor>.conf`.
  - `storage.links` points `/etc/extensions/<name>.raw` at it, and a second link enables the unit (`/etc/systemd/system/multi-user.target.wants/k3s.service → /usr/local/lib/systemd/system/k3s.service`).
  - Optional updates use a `systemd-sysupdate.timer` with a `.service` drop-in that runs `systemd-sysupdate -C <name>-<minor> update` and touches `/run/reboot-required`, which kured picks up.
  - Bakery releases ship `SHA256SUMS` plus `<ext>.conf` per extension. URLs are `https://extensions.flatcar.org/extensions/...`; that host was blocked here, so its liveness is **UNVERIFIED**.
  - booty already renders Butane, so it can mirror the `.raw` into `/data/` and serve it with `verification.hash`, as upstream does for Bluefin sysexts.
- **As a backup to single-node Talos:** this works on PXE-booted Flatcar only with a persistent disk for `/var/lib/kubesolo`. Flatcar netboot runs from a RAM tmpfs root (upstream README, "Flatcar and Fedora CoreOS run from RAM when PXE-booted"), so state is otherwise lost every boot. Reuse upstream's `--controlPlaneDisk` pattern (format once, label, bind-mount).

---

## 6. Bluefin Server

- **What it is:** projectbluefin/server (Apache-2.0, **Alpha**, README "Release status: Alpha").
  - "An image-based Linux server OS built like a container, composed from freedesktop-sdk" (FSDK 26.08, systemd v261, built with BuildStream 2).
  - It is DDI-first and diskless-first. The signed netboot UKI pulls a dm-verity erofs `/usr` DDI into RAM with `rd.systemd.pull`, and the DDI is verified against a GPG-signed `SHA256SUMS`.
  - Install to disk is optional (`systemd-sysinstall`, A/B updates through systemd-sysupdate).
  - It is Secure Boot first. Ignition runs per boot from `bluefin-node.ign`.
  - Opt-in sysexts: k0s, kubeadm, zfs, NVIDIA, homelab/kubestellar.
  - The README names booty as the network-boot server.
  - Releases are tagged `v<YY.MM.run>`; the latest seen was v26.10.1094. They are mirrored as an ORAS artifact at `ghcr.io/projectbluefin/bluefin-server`.
- **How upstream jeefy/booty boots it** (`git show upstream/main:README.md` §Bluefin Server, lines ~709–800; code in `pkg/server/bluefin*.go`, `pkg/server/join.go`, `pkg/profile/kubeadm_bluefin.go`, `pkg/hardware/bluefin.go`):
  - ProxyDHCP answers x86-64 UEFI HTTP Boot clients (option 60 `HTTPClient`, arch 0x0010) with `http://<server>/bluefin/<mac>/bluefin-server-netboot.efi`.
  - The UKI's initrd pulls the DDI, `SHA256SUMS` and `SHA256SUMS.gpg` from the same directory, then HEADs and runs `bluefin-node.ign` (Ignition spec 3.6.0).
  - Fallbacks:
    - iPXE chainload with Secure Boot off: booty rewrites `rd.systemd.pull=…bootorigin:` to an explicit URL.
    - Legacy BIOS: iPXE boots the UKI's `.linux`/`.initrd`/`.ucode` PE sections; diskless only.
  - Release sync: GitHub releases or OCI, with optional `--bluefinKeyring` signature checking.
  - Install-to-disk: `booty-install.service` → `systemd-sysinstall`.
  - Relevant commit history (`git log upstream/main -- pkg/server/bluefin* pkg/versions/bluefin*`): 852b265 BIOS diskless, 74b0ba3/97a014f/a7eecf0 kubeadm sysext sync and worker join, 963d0ff NVIDIA sysexts, 875d952 core user (root locked since 26.10), 1544200 OCI cache removed.
- **Does upstream's kubeadm join work on it?**
  - **Worker of an external kubeadm cluster: yes, as documented and tested** (README §"Bluefin hosts joining a kubeadm cluster"; tests `pkg/server/bluefin_kubeadm_test.go` and the golden `testdata/bluefin-node-kubeadm-profile-*.json`).
    - It requires `--profile=kubeadm-worker`, `--controlPlane=external`, and a release that ships `kubeadm_<ver>.raw.zst` (from server 26.09.1).
    - The node config adds `/etc/extensions/kubeadm_<ver>.raw` and `booty-kubeadm-join.service`, which runs `systemctl enable --now containerd kubelet`, then `kubeadm reset -f`, then `kubeadm join … --cri-socket unix:///run/containerd/containerd.sock`. Retries back off up to 30 attempts.
    - If the sysext is missing, it fails closed: the node boots without joining.
  - **Managed kubeadm control plane on Bluefin: not supported.** The matrix says "not supported (HTTP 400 under `managed`)". Bluefin is supported as a control plane only under `--clusterDistribution=k0s`.
    - Bluefin's own `docs/skills/kubeadm-sysext.md` (updated 2026-10-04) now ships an opt-in single-node control plane (`kubeadm-init.service`) that upstream booty does not yet use.
  - **Version skew risk:**
    - The Bluefin kubeadm sysext pins Kubernetes **1.35.9** (`include/kubeadm.yml`) and is locked to the image version.
    - Upstream booty's `--k8sVersion` default for Flatcar/CoreOS is **v1.34.3**.
    - Kubernetes forbids a kubelet newer than the API server (the bluefin doc says the same). A Bluefin worker therefore cannot join a 1.34 control plane that booty built.
    - `pkg/server/bluefin_kubeadm.go` performs no version check (grep for version/skew/minor found only the release version).
    - Whether joins actually fail today is **UNVERIFIED**, but by Kubernetes policy they should.

---

## 7. Netboot feasibility (one line each)

netboot.xyz evidence comes from netbootxyz/netboot.xyz@main (2026-10-09): `roles/netbootxyz/templates/menu/*.ipxe.j2` and `endpoints.yml`.

- **Arch (archiso):** official. The releng profile's initramfs includes `archiso_pxe_common archiso_pxe_nbd archiso_pxe_http archiso_pxe_nfs` (`configs/releng/airootfs/etc/mkinitcpio.conf.d/archiso.conf`), plus `syslinux/archiso_pxe-linux.cfg` with `archiso_http_srv=http://${pxeserver}/`. Kernel and initrd come from mirror `iso/<ver>/arch/boot/x86_64/`. Arch's own ipxe.archlinux.org signed netboot images: **UNVERIFIED** (archlinux.org blocked). netboot.xyz: **yes**, `archlinux.ipxe.j2` boots mirror kernel+initrd with `archiso_http_srv=`.
- **CachyOS:** no official PXE docs found (wiki.cachyos.org blocked: **UNVERIFIED**). netboot.xyz: **yes (live)**. `live-cachyos.ipxe.j2` uses netboot.xyz-repackaged `vmlinuz/initrd/airootfs.sfs` (version 260809) and **injects an `archiso_pxe_http` hook as an extra initrd**, which implies the stock CachyOS initramfs lacks the HTTP PXE hook (an inference).
- **EndeavourOS:** no official PXE docs found (**UNVERIFIED**). netboot.xyz: **yes**, `endeavouros.ipxe.j2` and `live-endeavouros.ipxe.j2` boot repackaged archiso files (Titan-Nova-2026.08.15) with `archiso_http_srv=`.
- **SteamOS:** **no official netboot/PXE path found**. A web search turned up only Valve's image download and a netboot.xyz KB on PXE-booting *other* OSes on a Steam Deck. netboot.xyz has no SteamOS entry (`grep -i steamos` finds nothing). Treat it as not feasible without repackaging.
- **Tails:** netboot.xyz **yes**. `live-tails.ipxe.j2` loads `vmlinuz` + `initrd.img` + a helper script + the whole `tails-amd64.iso` as `/tails.iso` (`fromiso=`), version 7.14. Official Tails PXE: **UNVERIFIED**, since tails.net was blocked; I believe Tails doesn't support it.
- **Debian/Debian-based:** the fork already has a `debian` family with preseed (`pkg/ostype/ostype.go`). netboot.xyz has `debian.ipxe.j2` and `live-debian.ipxe.j2` (9 endpoints). Debian's official `netboot.tar.gz` installer: well known but **UNVERIFIED** this session.
- **Proxmox VE:** **official PXE as of proxmox-installer 9.1.7** (debian/changelog: "assistant: add support for splitting ISO into (i)PXE-compatible files, extracting vmlinuz and initrd … A configuration file for iPXE can optionally be generated via --pxe-loader").
  - Command: `proxmox-auto-install-assistant prepare-iso <iso> --fetch-from {iso|http|partition} --pxe [--pxe-loader ipxe]`.
  - It emits `vmlinuz`, a gzip-recompressed `initrd.img` and `boot.ipxe`, which runs `initrd initrd.img; initrd <iso> proxmox.iso` (`proxmox-auto-install-assistant/src/main.rs:226-280, 1034-1431`).
  - With `--fetch-from http`, the answer URL comes from the ISO, DHCP option 250 (`proxmox-auto-installer-manifest-url`) with option 251 for the cert fingerprint, or DNS TXT (`proxmox-fetch-answer/src/fetch_plugins/http.rs:20-46,133`). An optional Bearer token is supported.
  - The pve-docs `.adoc` files do not mention PXE yet (grep found only `qm.adoc`). The Automated_Installation wiki page was blocked.
  - netboot.xyz: **yes**, `proxmox.ipxe.j2` with PVE 9.2-1 (amd64 and arm64), using the same vmlinuz + initrd + `proxmox.iso` initrd trick.
- **Proxmox Backup Server:** the same shared installer (`proxmox-post-hook` handles `ProxmoxProduct::Pbs`), so `prepare-iso --pxe` should apply to an auto-install-capable PBS ISO. The first PBS version with that capability is **UNVERIFIED**. netboot.xyz: **yes**, PBS 4.2-1 via the same method (it also has PMG 9.1-1 and PDM 1.1-1).
- **TrueNAS (SCALE / Community Edition):** **no official PXE support.** truenas/documentation (465 .md files, 2026-10-09) contains no "pxe", "netboot" or "network boot". Community threads (forums.truenas.com/t/boot-into-truenas-installer-via-ipxe/30138; forum.ipxe.org tid=32091) report the installer reaching its menu over iPXE and then failing to find installation media. netboot.xyz: **no** entry. Not feasible without custom work; plan it as an ISO-over-HTTP or sanboot experiment at best.

---

## Suggested roadmap implications

1. **Talos first: fix the serving path before swapping the KMS.**
   - Gate `/machineconfig` for cluster members: per-boot one-time token embedded in the iPXE `talos.config=` URL, binding source IP to the MAC, rate-limiting, and optional Talos OAuth2.
   - Then put a `SecretsBackend` interface behind `encryptSecrets`/`decryptSecrets` with these backends: `age` (today), `transit` (OpenBao/Vault via openbao/api/v2), `kv` (store the bundle in KV v2; render configs on demand instead of freezing ciphertext), and `onepassword` (onepassword-sdk-go service account; CGO_ENABLED=0 is fine).
2. **Bump machinery to v1.14.x** after the backend work. The fork pins v1.13.0, and the docs already cover v1.14/v1.15.
3. **kubeadm on Flatcar/FCOS:** port upstream's `--profile=kubeadm-worker` and managed control plane (`pkg/cluster`, `pkg/kubeadm`, `pkg/profile`). Treat Bluefin as worker-only (Alpha) and add a kubelet/API minor-skew check that upstream lacks.
4. **KubeSolo backup:**
   - Build a `kubesolo.sysext` modelled on `k3s.sysext`, or contribute it upstream to sysext-bakery.
   - Serve it from `/data/` with an Ignition hash.
   - Give it a persistent disk.
   - Handle Docker on Flatcar, since KubeSolo's installer refuses to run with Docker present.
5. **Generic Linux:** Arch, EndeavourOS and CachyOS via the archiso `archiso_http_srv` pattern (CachyOS needs the extra hook initrd). Tails via `fromiso`. Debian already exists. SteamOS: no path.
6. **Server OSes:** Proxmox VE and PBS are first-class through `prepare-iso --pxe` plus booty serving the answer file at the DHCP option-250 URL, which maps cleanly onto booty's per-host config model. TrueNAS: defer, no supported path.
