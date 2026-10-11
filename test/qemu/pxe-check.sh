#!/bin/bash
# Boots a UEFI (OVMF) QEMU guest from booty and reports how far the chain got.
# TOPO=proxy: a site DHCP server (netns "site", 10.0.2.2, no boot info) plus
#             booty's proxyDHCP on br0 (10.0.2.50). Tests ${proxydhcp/next-server}.
# TOPO=dhcp:  dnsmasq is the only DHCP server on tap0 (10.77.0.1) and points
#             firmware at booty's TFTP (dhcp-boot=ipxe.efi). No proxyDHCP.
# Usage (root): BOOTY=bin/booty TOPO=proxy|dhcp [OUT=dir] [QEMU_TIMEOUT=240] test/qemu/pxe-check.sh
# Based on the verified recipes in docs/plans/2026-10-11-upstream-import/research/.
set -u
BOOTY=${BOOTY:?set BOOTY to a built booty binary}
TOPO=${TOPO:-proxy}
OUT=${OUT:-$(mktemp -d)}
MAC=52:54:00:12:34:58
mkdir -p "$OUT/data"
cp /usr/share/OVMF/OVMF_VARS_4M.fd "$OUT/vars.fd"
cleanup() {
  [ -n "${QPID:-}" ] && kill $QPID 2>/dev/null
  [ -n "${BPID:-}" ] && kill $BPID 2>/dev/null
  for p in "$OUT"/*.pid; do [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null; done
  [ -n "${TPID:-}" ] && kill $TPID 2>/dev/null
  wait 2>/dev/null
  ip link del tap0 2>/dev/null; ip link del br0 2>/dev/null; ip link del site0 2>/dev/null
  ip netns del site 2>/dev/null
  [ -n "${NFCALL:-}" ] && echo "$NFCALL" > /proc/sys/net/bridge/bridge-nf-call-iptables
  true
}
trap cleanup EXIT
cleanup
# With net.bridge.bridge-nf-call-iptables=1 and an iptables FORWARD DROP policy,
# bridged frames between tap0 and the site veth are dropped; turn it off for the
# run and let cleanup restore the saved value.
if [ -f /proc/sys/net/bridge/bridge-nf-call-iptables ]; then
  NFCALL=$(cat /proc/sys/net/bridge/bridge-nf-call-iptables)
  echo 0 > /proc/sys/net/bridge/bridge-nf-call-iptables
fi
ip tuntap add tap0 mode tap
if [ "$TOPO" = proxy ]; then
  SRV=10.0.2.50
  ip link add br0 type bridge; ip link set br0 type bridge stp_state 0 forward_delay 0
  ip link set tap0 master br0; ip link set tap0 up
  ip link add site0 type veth peer name site1
  ip link set site0 master br0; ip link set site0 up
  ip netns add site; ip link set site1 netns site
  ip -n site addr add 10.0.2.2/24 dev site1; ip -n site link set site1 up; ip -n site link set lo up
  ip addr add $SRV/24 dev br0; ip link set br0 up
  # booty's proxyDHCP sends 255.255.255.255 from a 0.0.0.0 socket; without this
  # route Linux may send it out the default-route interface instead of br0.
  [ "${NOROUTE:-0}" = 1 ] || ip route add 255.255.255.255/32 dev br0 2>/dev/null || true
  IF=br0
  ip netns exec site dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file="$OUT/site.pid" \
    --user=root --port=0 --interface=site1 --bind-interfaces \
    --dhcp-range=10.0.2.15,10.0.2.99,255.255.255.0,1h --dhcp-leasefile="$OUT/site-leases" \
    --log-dhcp --log-facility="$OUT/site-dnsmasq.log" &
  PROXY=--proxyDHCPEnabled=true
else
  SRV=10.77.0.1
  ip addr add $SRV/24 dev tap0; ip link set tap0 up
  IF=tap0
  dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file="$OUT/dnsmasq.pid" \
    --user=root --port=0 --interface=tap0 --bind-interfaces \
    --dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m \
    --dhcp-boot=ipxe.efi,,$SRV \
    --dhcp-leasefile="$OUT/leases" --log-dhcp --log-facility="$OUT/dnsmasq.log" &
  PROXY=--proxyDHCPEnabled=false
fi
tcpdump -i $IF -n -U --immediate-mode -w "$OUT/host.pcap" 'udp or tcp port 8080 or arp' 2>/dev/null &
TPID=$!
"$BOOTY" --dataDir="$OUT/data" --serverIP=$SRV --httpPort=8080 $PROXY --debug=true >"$OUT/booty.log" 2>&1 &
BPID=$!
for i in $(seq 1 60); do grep -q 'Booty started' "$OUT/booty.log" && break; sleep 0.5; done
grep -q 'Booty started' "$OUT/booty.log" || { echo "result=BOOTY_DID_NOT_START"; tail -20 "$OUT/booty.log"; exit 1; }
timeout ${QEMU_TIMEOUT:-240} qemu-system-x86_64 -machine q35 -accel tcg -m 512 \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,unit=1,file="$OUT/vars.fd" \
  -display none -serial file:"$OUT/serial.log" -monitor none -no-reboot \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -device virtio-net-pci,netdev=n0,mac=$MAC,romfile= &
QPID=$!
RESULT=TIMEOUT
for i in $(seq 1 $(( ${QEMU_TIMEOUT:-240} * 2 ))); do
  if grep -q "path=/booty.ipxe" "$OUT/booty.log"; then RESULT=HTTP_BOOT_SCRIPT; break; fi
  if grep -q 'TFTP get.*booty.ipxe' "$OUT/booty.log" && [ "${UNTIL:-http}" = tftp ]; then RESULT=TFTP_BOOTY_IPXE; break; fi
  kill -0 $QPID 2>/dev/null || { RESULT=QEMU_EXITED; break; }
  sleep 0.5
done
echo "result=$RESULT topo=$TOPO out=$OUT"
grep -E 'TFTP get|proxyDHCP reply|path=/booty.ipxe|boot script' "$OUT/booty.log" | head -40
