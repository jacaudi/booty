# UEFI boot-chain check against booty (QEMU/OVMF)

Date: 2026-10-11. booty commit: d61bc84 (docs: embedded iPXE).
Script: `test/qemu/pxe-check.sh`. iPXE `mode:` from `boot/VERSION`: native.
Packages (`dpkg-query -W qemu-system-x86 ovmf`):

```
ovmf	2024.02-2ubuntu0.10
qemu-system-x86	1:8.2.2+ds-0ubuntu1.18
```

QEMU 8.2.2, TCG (no KVM). Marker line numbers: "Booty started" is at cmd/main.go:538 (the brief said 535: pure shift).

## Results

| Run | Command | Result |
|---|---|---|
| dhcp | `TOPO=dhcp UNTIL=tftp` | `result=TFTP_BOOTY_IPXE` (pass) |
| proxy | `TOPO=proxy UNTIL=tftp` | `result=QEMU_EXITED` (the 240 s QEMU timeout expired; no TFTP get at all) |
| proxy, 480 s | `TOPO=proxy UNTIL=tftp QEMU_TIMEOUT=480` | `result=QEMU_EXITED` (same) |
| proxy, no route | `TOPO=proxy UNTIL=tftp NOROUTE=1` | `result=QEMU_EXITED` |

The proxy result differs from the brief's expectation (`TFTP_BOOTY_IPXE`). No booty code was changed.

### dhcp topology (pass)

```
result=TFTP_BOOTY_IPXE topo=dhcp out=/tmp/pxe-dhcp
TFTP get file=ipxe.efi (x2), autoexec.ipxe (x2), booty.ipxe
```

tcpdump (`tcpdump -nr /tmp/pxe-dhcp/host.pcap 'udp port 69'`):

```
13:41:18.883499 IP 10.77.0.113.1036 > 10.77.0.1.69: TFTP, length 51, RRQ "ipxe.efi" octet tsize 0 blksize 1468 windowsize 4
13:41:18.930323 IP 10.77.0.113.1037 > 10.77.0.1.69: TFTP, length 43, RRQ "ipxe.efi" octet blksize 1468 windowsize 4
13:41:19.991004 IP 10.77.0.113.28101 > 10.77.0.1.69: TFTP, length 43, RRQ "autoexec.ipxe" octet blksize 1432 tsize 0
13:41:20.014665 IP 10.77.0.113.41525 > 10.77.0.1.69: TFTP, length 43, RRQ "autoexec.ipxe" octet blksize 1432 tsize 0
13:41:28.154804 IP 10.77.0.113.39979 > 10.77.0.1.69: TFTP, length 40, RRQ "booty.ipxe" octet blksize 1432 tsize 0
```

The firmware loaded ipxe.efi, the embedded iPXE ran, and it requested `booty.ipxe` from booty's TFTP (10.77.0.1).

### proxy topology (fail)

Two causes were found.

1. Sandbox, not booty: this host has `net.bridge.bridge-nf-call-iptables=1` and an iptables `FORWARD` policy of DROP (Docker). Bridged frames between tap0 and the site veth were dropped, so the site dnsmasq never saw the DISCOVER (`site-dnsmasq.log` has no DHCP lines; firmware reported `PXE-E16: No valid offer received`). Runs 1 and 2 above are this failure. Out dirs: /tmp/pxe-proxy, /tmp/pxe-proxy480.
2. After `sysctl -w net.bridge.bridge-nf-call-iptables=0` (reverted to 1 afterwards; this is a host tweak, not a script change) the site DHCP offer arrived (firmware got station IP 10.0.2.45) but the chain still stopped (out: /tmp/pxe-proxy-nfoff). booty logged:

```
msg="ProxyDHCP OFFER" xid=0x3aee05c5 mac=52:54:00:12:34:58 siaddr=10.0.2.50
msg="proxyDHCP reply" client=52:54:00:12:34:58 type=OFFER to=255.255.255.255:68 file=""
msg="ProxyDHCP ignoring packet addressed to another server" xid=0x3aee05c5 ... serverIdentifier=10.0.2.2
msg="ProxyDHCP ignoring non-PXE packet" ... classIdentifier=HTTPClient:Arch:00016:UNDI:003000   (x4, later HTTP boot attempts)
```

   Firmware: `Station IP address is 10.0.2.45` then `PXE-E21: Remote boot cancelled.` There was no proxyDHCP ACK and no `file=ipxe.efi` reply. The OVMF PXE REQUEST is a broadcast carrying the site server's identifier (10.0.2.2), and booty drops it ("addressed to another server"). The brief expected an OFFER then an ACK with `file=ipxe.efi`. UNVERIFIED: whether the OFFER with `file=""` is by design and whether the REQUEST should be answered; this needs a read of `pkg/proxydhcp/proxydhcp.go` (not done: out of scope).

No TFTP RRQ occurred in the proxy runs, so there is no `RRQ "booty.ipxe"` line to compare with 10.0.2.50.

### Step 3b: broadcast route (NOROUTE=1)

`ip route show default`: `default via 192.0.2.1 dev eth0`.

With no `255.255.255.255/32 dev br0` route, booty's proxyDHCP OFFER was logged (`proxyDHCP reply ... to=255.255.255.255:68`) but did NOT appear on br0 in `host.pcap` (the only reply seen was the site's `10.0.2.2.67 > 255.255.255.255.68`). With the route (runs above) the `10.0.2.50.67 > 255.255.255.255.68` reply is on br0 immediately after each request. Evidence for #71: with the default route on another interface, the broadcast proxyDHCP reply does not reach the bridge. (Out: /tmp/pxe-noroute, nf-call-iptables=0.)

## Cleanup check

After all runs: `ip link show br0` / `tap0`: "Device does not exist"; `ip netns list`: empty.

## 2026-10-11: proxyDHCP fix and HTTP boot script

Booty commit: `843af11` (`fix(proxydhcp): boot UEFI firmware beside a site DHCP server (boot item type 0x8000, ACK to source port)`). Built with `go build -o bin/booty ./cmd`; `test/qemu/pxe-check.sh` now also saves, clears and restores `net.bridge.bridge-nf-call-iptables` and runs `tcpdump --immediate-mode`. Both runs used `QEMU_TIMEOUT=300`.

Result lines:

```
result=HTTP_BOOT_SCRIPT topo=proxy out=/tmp/pxe-p6-proxy
result=HTTP_BOOT_SCRIPT topo=dhcp out=/tmp/pxe-p6-dhcp
```

`boot script` log lines (`booty.log` of each run):

```
# proxy
time=2026-10-11T15:41:55.746Z level=INFO msg="boot script" kind=holding mac=52:54:00:12:34:58
# dhcp
time=2026-10-11T15:41:35.036Z level=INFO msg="boot script" kind=holding mac=52:54:00:12:34:58
```

Summary of F1: with a site DHCP server beside booty's proxyDHCP, OVMF aborted with `PXE-E21: Remote boot cancelled`. Booty advertised boot-server and menu type 0, which EDK2 treats as local boot (`NetworkPkg/UefiPxeBcDxe/PxeBcBoot.c:937-941`). Once the type is non-zero, EDK2 sends its boot-server REQUEST from port 4011 and listens only there (`PxeBcDhcp4.c:1402,1496`), while booty answered to ciaddr:68, which gave `PXE-E18: Server response timeout`. The fix uses vendor type 0x8000 (as dnsmasq does), answers on the client's source port when it equals ciaddr, and echoes the requested PXE_BOOT_ITEM. Evidence: `.superpowers/sdd/README/f1-investigation.md`; decision: design section 8, item 12.

The earlier proxy failure recorded above (`PXE-E21` in the proxy topology) is superseded by this section.
