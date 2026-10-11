# QEMU + OVMF UEFI PXE recipes for booty's boot chain (verified 2026-10-11)

`SP=/tmp/claude-0/-home-user-booty/d59ac83a-7590-5a41-ad64-142ee85fc598/scratchpad`. Container: Ubuntu 24.04, root, **TCG only** (no `/dev/kvm`). `/home/user/booty` was not modified.

Packages, versions as in `tooling-qemu.md`: QEMU 8.2.2, ovmf 2024.02-2ubuntu0.10, dnsmasq 2.91, plus python3 for the HTTP server.

Upstream docs: `git ls-tree -r upstream/main docs | grep -i -E 'qemu|evidence'` returns nothing.
- `docs/plans/2026-09-26-secure-boot-http-boot.md` has only evidence prose: `OVMF_CODE.secboot.fd`, `-machine q35,smm=on`, a bridged lab. It has no PXE command lines to reuse.
- `README.md:930` has a `virt-fw-vars` + `-drive if=pflash,unit=1,...` hint, for Secure Boot only.
- The recipes below were written from scratch.

## Artifacts

| File | What |
|---|---|
| `$SP/research/qemu-recipe-ovmf-proxy.sh` | OVMF PXE → dnsmasq proxyDHCP → ipxe.efi → booty.ipxe → HTTP marker. `TOPO=bridge` (default, full chain works) or `TOPO=slirp` (recipe b; HTTP is broken, see below). Optional `MENU=1`, `PROXY43=1`, `EXTRA_DNSMASQ=`. |
| `$SP/research/qemu-recipe-ovmf-dhcp.sh` | OVMF PXE with dnsmasq as the full DHCP server on tap0 (recipe a topology). |
| `$SP/research/proxy43.py` | Raw-socket responder that sends iPXE a proxy OFFER containing option 43.9. Experiment only. |
| `$SP/ovmf/ipxe-booty-v2.0.0.efi` | iPXE v2.0.0 `bin-x86_64-efi/ipxe.efi` with the embedded booty script. sha256 `99ef2151…a9aeaa`, 1158656 bytes. |
| `$SP/ovmf/embed.ipxe` | The embedded script, verbatim from the task. |
| `$SP/ovmf/<run>/` | Per-run evidence: `guest.pcap` (QEMU `filter-dump` on the NIC = exactly what the guest saw), `host.pcap`, `dnsmasq.log`, `site-dnsmasq.log`, `http.log`, `serial.log`, `start.ts`. Runs: `proxy1` (slirp), `slirp-menu`, `bridge0`, `bridge-menu`, `bridge-forced43`, `bridge-p43`, `dhcp1`, `dhcp-noping`. |

Run with: `IPXE_EFI=$SP/ovmf/ipxe-booty-v2.0.0.efi OUT=<dir> [TOPO=bridge|slirp] $SP/research/qemu-recipe-ovmf-proxy.sh`. The scripts clean up tap0/br0/site0, the `site` netns, and the dnsmasq/python/tcpdump/qemu processes on exit (trap). After all runs, `pgrep` and `ip -br link` showed nothing left over.

Common QEMU firmware/NIC arguments, with option ROM disabled so EDK2's own PXE runs:
```
qemu-system-x86_64 -machine q35 -accel tcg -m 512 \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,unit=1,file=$OUT/vars.fd \        # cp of /usr/share/OVMF/OVMF_VARS_4M.fd
  -display none -serial file:$OUT/serial.log -monitor none -no-reboot \
  <netdev args> -device virtio-net-pci,netdev=<id>,mac=52:54:00:12:34:58,romfile= \
  -object filter-dump,id=fd0,netdev=<id>,file=$OUT/guest.pcap
```
- No `-boot n` was needed. With no disks, OVMF's first boot option was `Boot0001 "UEFI PXEv4 (MAC:525400123458)"` (serial.log).
- OVMF's VirtioNetDxe drives the NIC and the serial log carries both the OVMF and iPXE consoles, so `serial.log` is human-readable evidence.

## 1. UEFI firmware PXE (EDK2) with proxyDHCP. **PROVEN**

Run `proxy1` used TOPO=slirp, i.e. recipe b: hub of `-netdev user,restrict=on,net=10.0.2.0/24` + tap0 10.0.2.50. Proxy dnsmasq:
```
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=… --user=root --port=0 \
  --interface=tap0 --bind-interfaces --dhcp-range=10.0.2.0,proxy \
  --pxe-service=x86-64_EFI,"booty",ipxe.efi,10.0.2.50 --enable-tftp --tftp-root=… --log-dhcp --log-facility=…
```
- **CSA name:** the man page lists `x86-64_EFI`. dnsmasq 2.91 matched it to EDK2's arch 7: its log shows `vendor class: PXEClient:Arch:00007:UNDI:003000`, then `PXE(tap0) … proxy`, then `bootfile name: ipxe.efi`.

Guest-side timeline (`guest.pcap`, seconds after QEMU start):
- `+2.28` DHCPDISCOVER: opt 60 `"PXEClient:Arch:00007:UNDI:003000"`, opt 93 ARCH=7, opt 94 NDI 1.3.0, opt 97 GUID.
- `+2.28` slirp OFFER (Your-IP 10.0.2.15, **Server-IP 10.0.2.2**, no router or bootfile).
- `+2.28` dnsmasq proxy OFFER (Server-IP 10.0.2.50, opt 60 `PXEClient`, opt 97, **no option 43, no file**).
- `+5.79` broadcast REQUEST (Server-ID 10.0.2.2), answered by slirp's ACK. dnsmasq did not answer the port-67 REQUEST, so there was no upstream "Lease confirmed isn't the same" problem.
- `+5.80` **`10.0.2.15.4011 > 10.0.2.50.4011`** request. dnsmasq replied ACK with `bootfile name: ipxe.efi`, `next server: 10.0.2.50`.
- `+6.79` TFTP `RRQ "ipxe.efi" octet tsize 0 blksize 1468 windowsize 4` (size probe, aborted: dnsmasq logs `error 8 User aborted`), then `+6.81` the real RRQ. dnsmasq logged `sent …/ipxe.efi to 10.0.2.15`. About 0.3 s for 1.1 MB.
- serial.log shows: `>>Start PXE over IPv4.` → `Station IP address is 10.0.2.15` → `Server IP address is 10.0.2.50` → `NBP filename is ipxe.efi` → `NBP filesize is 1158656 Bytes` → `NBP file downloaded successfully.` → `BdsDxe: starting Boot0001 "UEFI PXEv4 …"` → `iPXE initialising devices...`. **The EFI file executed.**

Side observations:
- EDK2 sends `windowsize 4`. dnsmasq ignores it, and the transfer is still lock-step ACK per block.
- The bridge topology (below) gives the same sequence. There the site dnsmasq's OFFER arrives 3 s after the proxy's (`+2.57` vs `+5.58`) because of dnsmasq's ping-before-offer.

## 2. Embedded iPXE: TFTP goes to the proxy's next-server, and the HTTP marker arrives. **PROVEN** (HTTP only on TOPO=bridge)

The iPXE startup itself tries `autoexec.ipxe` twice by TFTP from the NBP's server (`RRQ "autoexec.ipxe"` at +7.08/+7.09, dnsmasq `file … not found`, serial `autoexec.ipxe... Not found`) before running the embedded script. Booty could serve `autoexec.ipxe` (cf. upstream S1 evidence); here that file is absent and harmless.

**TOPO=slirp (`proxy1`):**
- `+11.27` iPXE DISCOVER: opt 60 `PXEClient`, user class `iPXE`, arch 7. Both OFFERs arrive: slirp (siaddr 10.0.2.2) and dnsmasq (siaddr 10.0.2.50).
- REQUEST/ACK with slirp.
- `+11.28` **`10.0.2.15.68 > 10.0.2.50.4011` ProxyDHCP REQUEST**, answered with an ACK (sname "10.0.2.50", file "ipxe.efi").
- `+11.28` **`RRQ "booty.ipxe"` to 10.0.2.50:69, not 10.0.2.2**. dnsmasq logged `sent …/booty.ipxe to 10.0.2.15`. serial: `tftp://10.0.2.50/booty.ipxe... ok`, `BOOTY-OK`. The first leg (`isset ${proxydhcp/next-server}`) was taken, as the URL shows.
- **HTTP FAILS on this topology:** serial shows `http://10.0.2.50:8080/marker... Connection reset (https://ipxe.org/0f0a6095)`. guest.pcap explains it:
  ```
  10.0.2.15.50518 > 10.0.2.50.8080: [S]
  52:55:0a:00:02:02 > …: 10.0.2.50.8080 > 10.0.2.15.50518: [R.]   <- slirp's MAC, forged RST
  d6:f2:…(tap0)     > …: 10.0.2.50.8080 > 10.0.2.15.50518: [S.]   <- real host SYN-ACK, too late
  ```
  The hub floods the SYN to slirp too, and slirp with `restrict=on` RSTs it, using 10.0.2.50 as source, before the host answers. **Recipe b cannot carry TCP to the tap host.** UDP (DHCP/4011/TFTP) is unaffected.

**TOPO=bridge (`bridge0`) fixes this.** Host `br0` = 10.0.2.50/24 holds `tap0` and a veth `site0`. Its peer `site1` is in netns `site` = 10.0.2.2/24, which runs a plain dnsmasq "site DHCP" (`--dhcp-range=10.0.2.15,10.0.2.99,255.255.255.0,1h`, no boot options). The site OFFER still carries **siaddr 10.0.2.2**, so the next-server trap is reproduced. The guest NIC is a plain `-netdev tap,ifname=tap0`. Result `result=MARKER elapsed_to_marker=16.13 s`, guest powered off:
- `+11.60` iPXE DISCOVER → proxy OFFER (.50) + site OFFER (.2) → REQUEST/ACK (.2).
- `+11.61` ProxyDHCP REQUEST → 4011 ACK.
- `+15.51` `RRQ "booty.ipxe"` → **10.0.2.50**.
- `+15.52` TCP SYN/SYN-ACK to 10.0.2.50:8080.
- http.log: `10.0.2.45 - - [...] "GET /marker?mac=52:54:00:12:34:58 HTTP/1.1" 200 -`
- serial: `BOOTY-OK`, `http://10.0.2.50:8080/marker... ok`, `MARKER-OK mac=52:54:00:12:34:58 ip=10.0.2.45`.
- The 3.9 s gap between 11.61 and 15.51 comes from iPXE's IPv6 autoconf. RS at 11.60/11.73/11.98/12.48/13.48 go unanswered, which matches `IPV6CONF_MAX_TIMEOUT` (3 s) backoff in `net/ndp.c`. With slirp, which answers RS with an RA immediately, there is no gap. The cause is inferred from pcap plus source and was not separately ablated.

**Does iPXE send a 4011 request when the proxy offer carries a PXE menu (43.9)?**
- **dnsmasq 2.91 never sent option 43 to these arch-7 clients.**
  - Default: no 43.
  - `MENU=1` (`--pxe-prompt=booty,0`; runs `bridge-menu`, `slirp-menu`): no 43.
  - Forcing it (`--dhcp-userclass=set:ipxe,iPXE --dhcp-option-force=tag:ipxe,43,06:01:08:09:08:80:00:05:62:6f:6f:74:79:ff`; run `bridge-forced43`): still no 43, because proxy replies ignore it. The tags matched (`tags: ipxe, br0`).
- With every dnsmasq variant, iPXE **did** send the 4011 ProxyDHCP REQUEST. This matches `net/udp/dhcp.c` `dhcp_has_pxeopts()`: the offer has neither siaddr+file nor 43.9, so iPXE enters `dhcp_state_proxy` → `dhcp_proxy_tx` to PXE_PORT 4011.
- With option 43.9 actually present (`PROXY43=1`, run `bridge-p43`): dnsmasq ignores iPXE (`--dhcp-ignore=tag:ipxe`) and `proxy43.py` sends an OFFER with siaddr 10.0.2.50, opt 60 PXEClient and `Vendor-Option (43) 6.1.8.9.8.128.0.5.98.111.111.116.121.255`, as tcpdump decodes it. **iPXE sent NO 4011 request.** guest.pcap goes straight from the DHCP ACK at +11.72 to `RRQ "booty.ipxe"` → 10.0.2.50 at +15.63. The proxy offer itself populated `proxydhcp/next-server`, and the marker arrived.
- **Implication for booty's proxydhcp:** if it includes 43.9 in the OFFER to iPXE, it must also carry siaddr, because no 4011 follow-up will come. If it omits 43 and file, it must answer 4011.

## 3. Full-DHCP topology (no proxy). **PROVEN**

Run `dhcp1`. tap0 = 10.77.0.1/24. dnsmasq full DHCP: `--dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m --dhcp-boot=ipxe.efi,,10.77.0.1 --enable-tftp`. Result `elapsed_to_marker=15.13 s`.
- `+2.71` EDK2 DISCOVER (`vendor class: PXEClient:Arch:00007:UNDI:003000`).
- `+5.72` OFFER 10.77.0.114 (`next server: 10.77.0.1`, `option: 67 bootfile-name ipxe.efi`).
- `+6.21` REQUEST/ACK.
- `+6.22` `RRQ "ipxe.efi"` → 10.77.0.1. No 4011 traffic at all.
- `+10.73` iPXE DISCOVER (user class iPXE, `UNDI:003010`), re-sent `+11.71`. iPXE waits `DHCP_DISC_PROXY_TIMEOUT_SEC`=2 for proxy offers because the OFFER has no `PXEClient`. REQUEST/ACK follows at `+13.71`.
- `+14.62` `RRQ "booty.ipxe"` → **10.77.0.1**. The `isset` leg failed and the `${next-server}` leg ran; serial shows `tftp://10.77.0.1/booty.ipxe... ok`.
- `+14.63` HTTP. http.log: `"GET /marker?mac=52:54:00:12:34:59 HTTP/1.1" 200`. serial: `MARKER-OK mac=52:54:00:12:34:59 ip=10.77.0.114`.
- `EXTRA_DNSMASQ=--no-ping` (run `dhcp-noping`) made the OFFER immediate (+2.12), but EDK2 still sent its REQUEST at +5.64. The total stayed at 15.12 s, so there is no gain.

## 4. iPXE build and licence target

```
git clone --depth 1 --branch v2.0.0 https://github.com/ipxe/ipxe     # 12798ec29aa8 "[release] Release version 2.0.0" (2026-03-06)
make -C src -j4 bin-x86_64-efi/ipxe.efi EMBED=$SP/ovmf/embed.ipxe   # rc=0, 38.9 s (4 vCPU)
rm -rf src/bin-x86_64-efi; make -C src -j1 …                        # rc=0, 133.5 s clean single-job
```
- Toolchain: gcc 13.3.0, GNU make 4.3, perl 5.38.2, liblzma-dev present.
- The output is 1158656 bytes, and the embedded script is visible via `strings`.
- **Rebuilds are not byte-identical.** The -j4 build gave sha256 `99ef2151…`, the -j1 rebuild `3eb3bd75…`; probably a build timestamp, UNVERIFIED which field.
- Booty should pin by source tag, not by binary hash, unless it adds reproducible-build flags (UNVERIFIED).

Licence targets (`src/Makefile.housekeeping:1333-1342`): `$(BIN)/%.licence` and `$(BIN)/%.licence_list`, both from `%.tmp`.
- `make -C src bin-x86_64-efi/ipxe.efi.licence EMBED=…` **FAILS** with rc=2 (make error 1 at `Makefile.housekeeping:1337`). It prints `The following files are missing a FILE_LICENCE() declaration:`, followed by the mlx_utils_flexboot headers/sources, `drivers/net/ath/ath5k/*`, `ath9k/*`, `bnx2_fw.h`, `bnxt/*.h` and `tg3/*`. The all-drivers `ipxe.efi` cannot get a verdict.
- `make … bin-x86_64-efi/ipxe.efi.licence_list` works and prints `bsd2 bsd2_patent bsd3 gpl2_only gpl2_or_later gpl2_or_later_or_ubdl gpl_any mit public_domain`.
- `perl util/licence.pl <that list>` prints the subsumption chain and ends with `The overall licence for this file is: GPL version 2 only`, rc=0.
- `make bin-x86_64-efi/snponly.efi.licence` **WORKS** (rc=0) and reports `The overall licence for this file is: GPL version 2 (or, at your option, any later version)`.

## Gotchas for the implementer
1. Recipe b (slirp hub) is fine for DHCP/proxy/4011/TFTP but **breaks TCP** to the tap host because slirp forges RSTs. Use the bridge+netns topology for anything that needs HTTP.
2. dnsmasq proxy mode sends EFI clients no option 43, ignores `--dhcp-option-force` for 43, and needs `--pxe-service` to reply at all (prior b1 result). Both EDK2 and iPXE therefore do a 4011 round-trip against it.
3. iPXE 2.0.0 EFI fetches `autoexec.ipxe` (TFTP, from the NBP's server) before the embedded script.
4. TCG timings: about 2.3–2.9 s to the first EDK2 DISCOVER and about 11–12 s to iPXE's DHCP. The marker arrives at **15–16 s**. Use timeouts ≥ 60 s.
5. Guest IPs: slirp gives 10.0.2.15; the site dnsmasq gave 10.0.2.45; full DHCP gave 10.77.0.114.

## UNVERIFIED
- Booty's own proxydhcp replacing dnsmasq. Not run.
- Which field makes iPXE rebuilds non-reproducible.
- That the 3.9 s bridge-topology delay is solely IPv6 autoconf. Inferred from pcap + source.
- Secure Boot (`OVMF_CODE_4M.secboot.fd`) paths. Not exercised.
