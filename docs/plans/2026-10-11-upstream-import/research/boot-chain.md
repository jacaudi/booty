# Boot-chain research: iPXE v2.0.0 settings scopes, build, local env, QEMU topologies

Date: 2026-10-11. Sources read are listed with each claim.
- `IPXE` = `/tmp/claude-0/-home-user-booty/d59ac83a-7590-5a41-ad64-142ee85fc598/scratchpad/dl/ipxe/src` (shallow clone of https://github.com/ipxe/ipxe at tag v2.0.0, commit `12798ec` "[release] Release version 2.0.0").
- `SLIRP` = `.../scratchpad/dl/libslirp/src` (GitHub mirror github.com/qemu/libslirp, HEAD 5ac17660, **dated 2020-03-31**. The canonical gitlab.freedesktop.org repo was blocked by the proxy, so current libslirp may differ.)
- `QDOC` = `.../scratchpad/dl/qemudocs/` (`qemu-options.hx` and `docs/system/devices/net.rst` from raw.githubusercontent.com/qemu/qemu/master, at master 615ece3c. www.qemu.org itself was blocked: proxy 403 / ENOTFOUND. These files are the source the qemu.org pages are built from.)
- Nothing was built or executed from the downloads.

---

## 1. iPXE DHCP / ProxyDHCP settings

### 1(a) Names, placement, priority

- Block names: `dhcp`, `proxydhcp`, `pxebs` (`IPXE/include/ipxe/dhcp.h:711,714,717`).
- The DHCPACK is registered as a child of the netdev block, so it is `net0.dhcp` (`IPXE/net/udp/dhcp.c:596-599`: `parent = netdev_settings(...)`, `register_settings(settings, parent, DHCP_SETTINGS_NAME)`).
- ProxyDHCP is registered with `parent = NULL`, so it sits at the root as `proxydhcp` (`dhcp.c:620`, `dhcp.c:730`). `NULL` means the root block (`IPXE/core/settings.c:550-555`). PXEBS is registered the same way (`dhcp.c:874`).
- Lookup order. Unscoped `fetch_setting(NULL, …)` tries the root's own store, then walks the children depth-first in list order (`settings.c:667-718`).
- Order among children: `reprioritise_settings` places a block after every sibling with a higher-or-equal `${priority}`. With equal priority (0 by default), a newly registered block goes **last** (`settings.c:438-466`, called from `register_settings` at `settings.c:502`). A block with the same name is replaced (`settings.c:489-491`).
- When `dhcp` gets its ACK it **unregisters any existing `proxydhcp` and `pxebs`** (`dhcp.c:606-610`), including copies cached from the PXE ROM by cachedhcp. It then registers the new proxydhcp block (`dhcp.c:615-627` or `730`). That puts `proxydhcp` after `net0` among the root's children.
- `next-server` is the BOOTP `siaddr` field (`settings.c:2424-2428`, tag `DHCP_EB_SIADDR`; `IPXE/net/dhcppkt.c:106`). A siaddr of 0 counts as absent, so the fetch falls through to the options (`dhcppkt.c:52-58` `used_len_ipv4`, `dhcppkt.c:202-217`).

**Answer.** Take a site DHCP ACK with siaddr = S and a proxy reply from booty B:
- `${next-server}` resolves to **S**: `net0` → `net0.dhcp` is searched before the root-level `proxydhcp`. It falls through to B only when the site ACK has siaddr 0.
- `${proxydhcp/next-server}` resolves to the proxydhcp block's siaddr. The search is scoped to that block and its children only (`settings.c:683-718` with the block returned by `parse_setting_name` at `settings.c:1554-1561`).

**What booty's reply contains (verified).** Upstream booty's OFFER sets siaddr = ServerIP (`upstream:pkg/dhcp/pxe.go:188-196` `WithServerIP`). I decoded `upstream:pkg/dhcp/testdata/golden/discover-bios.hex`:
- siaddr = 192.168.1.10, which is `testServerIP` (`upstream:pkg/dhcp/pxe_test.go:16`)
- empty `file`
- opt 54 = 192.168.1.10
- opt 60 = "PXEClient"
- opt 43 = `06 01 07 | 08 07 0000 01 c0a8010a | 09 08 0000 05 "Booty" | 0a 06 00 "Booty" | ff`. This includes sub-option 9, the boot menu.

So `${proxydhcp/next-server}` = booty's ServerIP.

**Caveat (verified in code).** A reply is taken as the ProxyDHCP candidate if it has a non-zero pseudo-id and option 60 "PXEClient", at priority ≥ the current candidate's. The last one received wins (`dhcp.c:417-424`; pseudo-id at `dhcp.c:1244-1248`). If the site DHCP server also sends option 60 "PXEClient", as some PXE-enabled site servers do, it can displace booty as `proxydhcp`.

### 1(b) No ProxyDHCP reply

- **`${proxydhcp/next-server}` expands to the empty string, not an error.**
  - `expand_settings` treats an unknown block as empty: `parse_setting_name` returns `-ENODEV` and `value = NULL` → `""` (`settings.c:2374-2390`, `1554-1561`).
  - It also treats a missing setting as empty ("treat non-existent settings as empty", `settings.c:2380-2384`).
  - After `dhcp`, a stale cached `proxydhcp` cannot survive, because `dhcp.c:606-608` unregisters it.
- `chain tftp:///booty.ipxe` then parses with host = `""`, which is non-NULL (`IPXE/core/uri.c:383-428`).
  - The TFTP open check is only `if (!uri->host)` (`IPXE/net/udp/tftp.c:1095`), so it passes.
  - `xfer_open_named_socket` resolves `""` (`tftp.c:236-238` → `IPXE/core/resolv.c:403-427`). The NUMERIC resolver fails, so the mux tries the next one, DNS (`resolv.c:202-221`). DNS is compiled in by default (`IPXE/config/general.h:252`).
  - **No DNS server known:** fails at once with `-ENXIO_NO_NAMESERVER` (`IPXE/net/udp/dns.c:986-990`). This is "fail fast".
  - **DNS server known** (the usual case in topology (b), where the site DHCP gives option 6): `dns_encode("")` gives the root name. Because `""` has no '.', the search list is appended (`dns.c:993`, `dns.c:101-150`, `dns.c:536-580`). iPXE sends a real DNS query for `<search-domain>` (or ".").
    - It usually fails after one DNS round trip (`-ENXIO_NO_RECORD`, `dns.c:876-882`).
    - If the search-domain apex has an A record, iPXE sends a TFTP RRQ to that host. The retry timer starts at 1/4 s, doubles, and fails once the timeout exceeds 10 s (`IPXE/include/ipxe/retry.h:16,19`, `IPXE/net/retry.c:166-186`). That is about 16 s by my reading of the code; I did not measure it.
    - An unresponsive DNS server costs a similar timeout.
  - **So the design doc's "the first leg is empty and fails at once" is only true when no DNS server is configured.** It never hangs forever.
- Suggested guard, verified in code: `isset ${proxydhcp/next-server} && chain tftp://${proxydhcp/next-server}/booty.ipxe || chain tftp://${next-server}/booty.ipxe || …`.
  - `isset` returns `-ENOENT` on an empty argument (`IPXE/core/exec.c:491-501`).
  - `&&`/`||` are evaluated left to right on the last executed rc (`exec.c:153-203`, `308-342`). A failed `isset` skips the `&&` command, and the next `||` runs.

### 1(c) Does iPXE itself send DHCPREQUEST to UDP 4011?

- **Only when the proxy offer lacks "PXE options".** `dhcp_has_pxeopts` is true if (siaddr ≠ 0 **and** a filename is present) **or** the offer has a PXE boot menu (43.9) (`dhcp.c:307-320`; `DHCP_PXE_BOOT_MENU` = 43.9 at `dhcp.h:127`).
  - If true, the offer is registered directly as `proxydhcp` and **no** ProxyDHCPREQUEST is sent (`dhcp.c:612-627`).
  - Otherwise iPXE enters the ProxyDHCP state and unicasts a DHCPREQUEST to `proxy_server:4011` (`dhcp.c:628-631`, `dhcp.c:670-688`, `PXE_PORT 4011` at `dhcp.h:33`). The reply must come from port 4011 and is registered as `proxydhcp` (`dhcp.c:719-736`).
  - If that reply does not arrive within `DHCP_REQ_PROXY_TIMEOUT_SEC` = 2 s, `dhcp` finishes successfully without proxydhcp settings (`dhcp.c:746-753`, `config/dhcp.h:70`).
- **Booty's upstream-style OFFER carries 43.9** (decoded above), so **iPXE's `dhcp` does NOT send a 4011 REQUEST to booty**. `proxydhcp` = booty's OFFER: next-server = booty, and **no filename** (the OFFER's `file` is empty).
  - Booty's 4011 ACK (filename `booty.ipxe` for user-class iPXE, or the arch binary) is used by the PXE firmware ROM, not by iPXE's `dhcp` command.
  - iPXE only does PXE boot-server discovery (state PXEBS, settings block `pxebs`) via `pxebs`/autoboot's `pxe_menu_boot` (`dhcp.c:1448-1531`, `IPXE/usr/autoboot.c:365-417`). The embedded script calls neither.
- `${filename}` unscoped therefore comes from `net0.dhcp` (the site ACK) if present. The embedded script does not use it.

---

## 2. iPXE build for `bin/undionly.kpxe`, `bin-x86_64-efi/ipxe.efi`, `bin-x86_64-efi/snponly.efi` with EMBED

**Host tools actually used:**
- gcc (`HOST_CC := gcc`, `IPXE/Makefile:19`, plus the target CC)
- binutils: as, ld, objcopy, objdump, ar/ranlib (`Makefile.housekeeping:1218,1263-1269,1368-1376`)
- make
- perl (`Makefile:26`; sortobjdump at `Makefile.housekeeping:1269`; FIXROM etc.)
- `cksum` + `awk` for the build ID (`Makefile.housekeeping:1229`). Upstream passes `BUILD_ID=` explicitly anyway.
- git: optional, only for `GITVERSION` when `../.git` exists (`Makefile:220-222`).
- openssl/csplit: only when CERT/TRUST are set (`Makefile.housekeeping:642-729`).

**liblzma:** needed for **undionly.kpxe**.
- `.kpxe` is an auto medium (`IPXE/arch/x86/Makefile.pcbios:30`).
- The rule is built from `%.kpxe.zbin` (`Makefile.housekeeping:1425-1431`), which needs `$(ZBIN)` (`1380-1382`).
- zbin links `-llzma` and includes `<lzma.h>` (`Makefile.housekeeping:1491-1500`, `IPXE/util/zbin.c:7`).
- EFI targets need only elf2efi, which includes no lzma or compression (`IPXE/Makefile.efi:41-44`, `util/elf2efi.c:22-40`; grep finds no compress/lzma).

**mtools:** not needed. It is used only by `util/genfsimg` (`mformat`/`mcopy` at genfsimg:329-330), which is invoked only for `.iso`/`.sdsk`/`.usb` (`Makefile.pcbios:66-74`, `Makefile.efi:64-68`). Upstream's `mtools` and `xz-utils` apt installs are unnecessary for these three targets.

**The .tmp file:**
- `$(BIN)/%.tmp` is the raw ld output and is kept (`.PRECIOUS : $(BIN)/%.tmp`, `Makefile.housekeeping:1263-1272`). For `undionly.kpxe` it is `bin/undionly.kpxe.tmp`.
- Compression happens only later (`.bin` → `.zbin`).
- The EMBED file is `.incbin`'d raw into `.data` (`IPXE/image/embedded.c:17-29`; `EMBED`/`EMBEDDED_IMAGE` handling at `Makefile.housekeeping:595-618`).
- So the script text is present byte-for-byte in `bin/undionly.kpxe.tmp` and in both `.efi` files. It is not in the compressed `undionly.kpxe`.

**Upstream `hack/build-ipxe.sh`** (read via `git show upstream/main:hack/build-ipxe.sh`):
- Line 77 greps for the fixed string `chain tftp://\${next-server}/booty.ipxe` in `bin/undionly.kpxe.tmp` and the two `.efi` files.
- I tested the same quoting with GNU grep 3.11 on local text files (`.../scratchpad/greptest/`):
  - The **old pattern still matches the new embed.ipxe**, because the new line's second leg `|| chain tftp://${next-server}/booty.ipxe` contains it. So the check passes but **does not verify the new first leg**.
  - On a script that has only the new first leg, the old pattern does NOT match.
  - The pattern `chain tftp://\${proxydhcp/next-server}/booty.ipxe` matches correctly (`/` is literal; `$` mid-pattern and `{` are literal in BRE).
- **The port must change the grep to the `${proxydhcp/next-server}` string**, which is what the design doc intends.
- Otherwise the script would work with the new line: targets, EMBED and file paths are all correct.

---

## 3. Local environment (commands run on this machine)

- `which qemu-system-x86_64 qemu-system-i386 swtpm` → nothing found (rc=1). **QEMU and swtpm are not installed.**
- `/usr/share/OVMF`, `/usr/share/ovmf`, `/usr/share/qemu`, `/usr/share/seabios` → all "No such file or directory". **No OVMF or SeaBIOS firmware.**
- `apt-cache policy qemu-system-x86 ovmf` → empty output, rc 0, because `/var/lib/apt/lists/` is empty (no package index). **UNVERIFIED** whether these packages are installable here. Run `apt-get update` and then the same policy command to check (not done, per the instructions).
- `sudo -n true` → `sudo-ok`. We are uid 0 (root).
- `sudo -n dockerd --version` → `Docker version 29.8.2, build 8af9fe3`. `/usr/bin/docker` is present. dockerd is **not running** (`pgrep` found nothing) and was not started.
- Extras:
  - **Present:** `/dev/net/tun`. Native-build toolchain: gcc 13.3.0, GNU make 4.3, perl, as/ld/objcopy, liblzma-dev 5.4.5 with `/usr/include/lzma.h`.
  - **Absent:** `/dev/kvm` (so QEMU would run TCG only, slowly), `ip`/iproute2, tcpdump, dnsmasq, mtools.
  - OS: Ubuntu 24.04.5 LTS.

---

## 4. Testing PXE in QEMU: what works and what needs root

**Verified from the docs / slirp source:**
- User-mode networking (slirp) needs no root and has a built-in DHCP server (QDOC net.rst:49-69). `tftp=dir` and `bootfile=file` turn on PXE boot from a host directory (QDOC qemu-options.hx:3437-3461). Example: `-netdev user,id=n1,tftp=/path/to/tftp/files,bootfile=/pxelinux.0`.
- The slirp DHCP **always sets siaddr = its own host address** (10.0.2.2) (SLIRP bootp.c:222,235). `tftp-server-name` sets option 66 only (qemu-options.hx:3445-3449; bootp.c:315-323), and iPXE's `next-server` is siaddr, not option 66. **slirp alone therefore cannot emulate "site DHCP pointing next-server at booty"**, and booty cannot sit on slirp's L2 segment as a host process.
- `restrict=on` isolates the guest from the host (qemu-options.hx:3394-3398).
  - Slirp still answers DHCP and its own TFTP, because those are handled before the restricted check (SLIRP udp.c:137-159).
  - It drops other UDP (udp.c:157) and TCP (tcp_input.c:390-401).
  - It omits the router and DNS options from DHCP (bootp.c:271-280).
- Slirp answers ARP only for 10.0.2.2, 10.0.2.3 and guestfwd addresses (SLIRP slirp.c:809-820).
- Slirp does **not** filter by destination MAC (slirp.c:852-895). On a shared hub it would also ingest IP frames meant for booty. Without `restrict=on` it would NAT any 10.0.2.x alias to host loopback (socket.c:827-838) and duplicate booty's traffic. **Use `restrict=on` when hubbing slirp with booty.**
- Connecting several VMs without root uses a UDP multicast "bus": `-netdev socket,id=n1,mcast=230.0.0.1:1234` or `-netdev dgram,…,remote.host=224.0.0.1,remote.port=1234` (qemu-options.hx:3654-3675, 3795-3820; net.rst "Connecting emulated networks between QEMU instances").
- Hubs connect a NIC and several backends within one QEMU: `-netdev hubport,id=id,hubid=hubid[,netdev=nd]` (qemu-options.hx:4059-4065).
- TAP needs `/dev/net/tun` and root, or a configured `qemu-bridge-helper`/sudo scripts for unprivileged users (net.rst:14-37; qemu-options.hx:3556-3577). **We are root and `/dev/net/tun` exists.**

**Conclusion.**
- A host-process booty can only be tested on the same L2 as the VM through a **tap device (root / CAP_NET_ADMIN)**.
- A fully rootless test is possible only by running booty and the "site DHCP" **inside guest VMs** joined to the client by `-netdev socket,mcast=`/`dgram` multicast. That needs a Linux guest image, which is heavier.

**Recipe (a): booty proxyDHCP beside a DHCP server that hands out a different next-server.** Root, tap + slirp hub. Single QEMU; slirp is the "site DHCP" with siaddr 10.0.2.2 ≠ booty.

```
# host (root): tap0 for booty, outside slirp's .15-.31 pool
ip tuntap add dev tap0 mode tap && ip addr add 10.0.2.10/24 dev tap0 && ip link set tap0 up   # iproute2 is NOT installed here
booty … --serverIP 10.0.2.10 (proxyDHCP on)             # booty listens on tap0
qemu-system-x86_64 -m 512 -boot n \
  [-bios /usr/share/seabios/bios.bin | -drive if=pflash,format=raw,readonly=on,file=OVMF_CODE.fd …] \
  -netdev hubport,id=hp0,hubid=0 -device e1000,netdev=hp0 \
  -netdev user,id=u0,restrict=on -netdev hubport,id=hp1,hubid=0,netdev=u0 \
  -netdev tap,id=t0,ifname=tap0,script=no,downscript=no -netdev hubport,id=hp2,hubid=0,netdev=t0
```

- Expected: `${next-server}` = 10.0.2.2 (slirp), `${proxydhcp/next-server}` = 10.0.2.10 (booty).
- With `restrict=on` there is no DNS option, so this cannot exercise the DNS slow path from 1(b).
- Options are from the docs. **The exact combined command line is UNVERIFIED:** QEMU is not installed, so it was not run. Firmware paths depend on the distro package.

**Recipe (b): site DHCP pointed at booty, no proxyDHCP.** Root, tap only.
- tap0 as above; booty runs with proxyDHCP off.
- A DHCP server on tap0 must set siaddr = 10.0.2.10 and a bootfile:
  - dnsmasq (not installed; something like `--dhcp-boot=undionly.kpxe,booty,10.0.2.10`, flag syntax **UNVERIFIED**), or
  - a small Go helper using `insomniacslk/dhcp` server4 with `dhcpv4.WithServerIP(10.0.2.10)`. The golden hex above confirms `WithServerIP` sets siaddr.
- QEMU: `-netdev tap,id=t0,ifname=tap0,script=no,downscript=no -device e1000,netdev=t0`.
- Add DNS (option 6, plus option 15 for a search domain) to reproduce the 1(b) DNS slow path.
- Do not run both a site DHCP server and booty's proxyDHCP on the same host interface without checking that both can bind UDP/67 (they would conflict). That is why recipe (a) uses slirp as the site server.

---

## UNVERIFIED items

1. Whether `qemu-system-x86`, `ovmf`, `seabios` and `swtpm` are installable here: the apt index is empty. Verify with `apt-get update && apt-cache policy …`.
2. Both QEMU command lines and their end-to-end boot behaviour under OVMF/SeaBIOS: not run (no QEMU, no KVM). Verify by installing and running them.
3. The ~16 s TFTP timeout and the DNS behaviour for `tftp:///` are read from code, not measured. Verify in QEMU with a DNS-providing DHCP.
4. Slirp facts come from a 2020 GitHub mirror of libslirp; current gitlab libslirp was unreachable. Check bootp.c, udp.c and slirp.c in the libslirp version that ships with the installed QEMU.
5. dnsmasq `--dhcp-boot` syntax for setting siaddr: the man page was not fetched.
6. Whether Debian `stable-slim` includes awk/cksum (needed for the build-ID command if `BUILD_ID` is not overridden). Upstream overrides `BUILD_ID`.
7. No iPXE build was run, so the presence of the script text in `.tmp`/`.efi` is shown from the Makefile and source, not by grepping a real binary.
