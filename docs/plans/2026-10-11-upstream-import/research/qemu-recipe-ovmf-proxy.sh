#!/bin/bash
# OVMF (UEFI) firmware PXE -> proxyDHCP (dnsmasq stands in for booty) -> TFTP ipxe.efi
# (iPXE v2.0.0 with embedded booty script) -> iPXE `dhcp` -> TFTP booty.ipxe from the
# proxy's next-server -> HTTP GET /marker?mac=...
#
# The NIC is virtio-net-pci with romfile= (no iPXE option ROM), so EDK2's own PXE client
# (OVMF VirtioNetDxe + PxeBcDxe) does the first stage.
#
# TOPO=slirp  (recipe b): guest NIC on a QEMU hub joined to slirp
#     (-netdev user,restrict=on,net=10.0.2.0/24; the "site" DHCP server is slirp at 10.0.2.2)
#     and tap0 (host 10.0.2.50/24). Proven: DHCP/proxy/4011/TFTP all work.
#     KNOWN LIMIT: every guest TCP SYN is flooded to slirp too, and slirp (restrict=on)
#     answers it with RST before the host's SYN-ACK, so HTTP to 10.0.2.50 fails with
#     iPXE "Connection reset (0f0a6095)". Use TOPO=bridge when HTTP is needed.
# TOPO=bridge (default): host bridge br0 (10.0.2.50/24) holding tap0 and a veth to netns
#     "site", where a second dnsmasq is the plain site DHCP server (10.0.2.2, no boot info).
#     Full chain incl. HTTP works.
# MENU=1 adds --pxe-prompt (NOTE: verified that dnsmasq 2.91 still sends NO option 43 to arch-7 clients).
# PROXY43=1: dnsmasq ignores iPXE's DHCP and proxy43.py sends iPXE a proxy OFFER with 43.9.
# EXTRA_DNSMASQ="..." appends raw flags to the proxy dnsmasq (e.g. a forced option 43).
#
# Usage: IPXE_EFI=/path/ipxe.efi [TOPO=bridge|slirp] [MENU=0|1] [OUT=dir] [QEMU_TIMEOUT=240] $0
# Needs root, /dev/net/tun, bridge+veth+netns support; packages: qemu-system-x86 ovmf
# dnsmasq-base iproute2 tcpdump python3.
set -u
OUT=${OUT:-$(cd "$(dirname "$0")" && pwd)/run-ovmf-proxy}
IPXE_EFI=${IPXE_EFI:?set IPXE_EFI to an ipxe.efi built with the embedded booty script}
TOPO=${TOPO:-bridge}
TAPIP=10.0.2.50
CSA=${CSA:-x86-64_EFI}            # dnsmasq CSA name; verified to match EDK2's PXEClient:Arch:00007
mkdir -p $OUT/tftp $OUT/www
cp $IPXE_EFI $OUT/tftp/ipxe.efi
printf '#!ipxe\necho BOOTY-OK\nchain http://%s:8080/marker?mac=${mac} || shell\n' $TAPIP > $OUT/tftp/booty.ipxe
# /marker is itself an iPXE script so the chain succeeds and the guest powers off cleanly
printf '#!ipxe\necho MARKER-OK mac=${mac} ip=${ip}\npoweroff\n' > $OUT/www/marker
cp /usr/share/OVMF/OVMF_VARS_4M.fd $OUT/vars.fd          # writable copy of the vars store
cleanup() {
  [ -n "${QPID:-}" ] && kill $QPID 2>/dev/null
  for p in $OUT/dnsmasq.pid $OUT/site-dnsmasq.pid; do [ -f $p ] && kill $(cat $p) 2>/dev/null; done
  [ -n "${HPID:-}" ] && kill $HPID 2>/dev/null
  [ -n "${P43PID:-}" ] && kill $P43PID 2>/dev/null
  [ -n "${TPID:-}" ] && kill $TPID 2>/dev/null
  wait 2>/dev/null
  ip link del tap0 2>/dev/null; ip link del br0 2>/dev/null; ip link del site0 2>/dev/null
  ip netns del site 2>/dev/null
  true
}
trap cleanup EXIT
cleanup
rm -f $OUT/*.log $OUT/*.pcap $OUT/*.pid $OUT/start.ts
ip tuntap add tap0 mode tap
if [ "$TOPO" = bridge ]; then
  ip link add br0 type bridge; ip link set br0 type bridge stp_state 0 forward_delay 0
  ip link set tap0 master br0; ip link set tap0 up
  ip link add site0 type veth peer name site1
  ip link set site0 master br0; ip link set site0 up
  ip netns add site; ip link set site1 netns site
  ip -n site addr add 10.0.2.2/24 dev site1; ip -n site link set site1 up; ip -n site link set lo up
  ip addr add $TAPIP/24 dev br0; ip link set br0 up
  IF=br0
  # plain site DHCP server (like a home router): addresses only, no PXE/boot info
  ip netns exec site dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$OUT/site-dnsmasq.pid \
    --user=root --port=0 --interface=site1 --bind-interfaces \
    --dhcp-range=10.0.2.15,10.0.2.99,255.255.255.0,1h --dhcp-leasefile=$OUT/site-leases \
    --log-dhcp --log-facility=$OUT/site-dnsmasq.log &
  NETARGS="-netdev tap,id=n0,ifname=tap0,script=no,downscript=no"
  NICNET=n0
else
  ip addr add $TAPIP/24 dev tap0; ip link set tap0 up
  IF=tap0
  NETARGS="-netdev user,id=u0,restrict=on,net=10.0.2.0/24 -netdev hubport,id=hp_user,hubid=0,netdev=u0
    -netdev tap,id=t0,ifname=tap0,script=no,downscript=no -netdev hubport,id=hp_tap,hubid=0,netdev=t0
    -netdev hubport,id=hp_nic,hubid=0"
  NICNET=hp_nic
fi
tcpdump -i $IF -n -U -w $OUT/host.pcap 'udp or tcp port 8080 or arp or icmp' 2>/dev/null &
TPID=$!
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$OUT/dnsmasq.pid \
  --user=root --port=0 --interface=$IF --bind-interfaces \
  --dhcp-range=10.0.2.0,proxy \
  --pxe-service=$CSA,"booty",ipxe.efi,$TAPIP \
  $([ "${MENU:-0}" = 1 ] && echo --pxe-prompt=booty,0) \
  ${EXTRA_DNSMASQ:-} \
  $([ "${PROXY43:-0}" = 1 ] && echo --dhcp-userclass=set:ipxe,iPXE --dhcp-ignore=tag:ipxe) \
  --enable-tftp --tftp-root=$OUT/tftp \
  --log-dhcp --log-facility=$OUT/dnsmasq.log &
if [ "${PROXY43:-0}" = 1 ]; then
  # experiment: dnsmasq ignores iPXE (user class iPXE) and proxy43.py answers iPXE's DISCOVER
  # with a proxy OFFER that carries option 43.9 (PXE menu); see proxy43.py
  python3 -u $(cd "$(dirname "$0")" && pwd)/proxy43.py $IF $TAPIP $OUT/proxy43.log &
  P43PID=$!
fi
python3 -u -m http.server 8080 --bind $TAPIP --directory $OUT/www > $OUT/http.log 2>&1 &
HPID=$!
sleep 1.5
START=$(date +%s.%N); echo $START > $OUT/start.ts
timeout ${QEMU_TIMEOUT:-240} qemu-system-x86_64 -machine q35 -accel tcg -m 512 \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,unit=1,file=$OUT/vars.fd \
  -display none -serial file:$OUT/serial.log -monitor none -no-reboot \
  $NETARGS \
  -device virtio-net-pci,netdev=$NICNET,mac=52:54:00:12:34:58,romfile= \
  -object filter-dump,id=fd0,netdev=$NICNET,file=$OUT/guest.pcap &
QPID=$!
RESULT=TIMEOUT
for i in $(seq 1 $(( ${QEMU_TIMEOUT:-240} * 2 ))); do
  grep -q 'GET /marker' $OUT/http.log 2>/dev/null && { RESULT=MARKER; break; }
  kill -0 $QPID 2>/dev/null || { RESULT=QEMU_EXITED; break; }
  sleep 0.5
done
END=$(date +%s.%N)
echo "result=$RESULT topo=$TOPO menu=${MENU:-0} elapsed_to_marker=$(echo "$END - $START" | bc) s"
for i in $(seq 1 20); do kill -0 $QPID 2>/dev/null || break; sleep 0.5; done   # wait for guest poweroff
if [ $RESULT = MARKER ]; then kill -0 $QPID 2>/dev/null && echo "qemu still running after marker" || echo "qemu exited after marker (guest poweroff)"; fi
