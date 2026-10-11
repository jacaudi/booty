#!/bin/bash
# OVMF (UEFI) firmware PXE with dnsmasq as the FULL DHCP server on tap0 (recipe a topology,
# no proxyDHCP): EDK2 PXE -> TFTP ipxe.efi (iPXE v2.0.0 + embedded booty script) from
# 10.77.0.1 -> iPXE `dhcp` (no proxy => `isset ${proxydhcp/next-server}` fails) ->
# chain tftp://${next-server}/booty.ipxe -> HTTP GET /marker?mac=...
# The NIC is virtio-net-pci with romfile= so EDK2's own PXE client runs (no iPXE option ROM).
#
# Usage: IPXE_EFI=/path/ipxe.efi [OUT=dir] [QEMU_TIMEOUT=240] [EXTRA_DNSMASQ=--no-ping] $0
# Needs root, /dev/net/tun; packages: qemu-system-x86 ovmf dnsmasq-base iproute2 tcpdump python3.
set -u
OUT=${OUT:-$(cd "$(dirname "$0")" && pwd)/run-ovmf-dhcp}
IPXE_EFI=${IPXE_EFI:?set IPXE_EFI to an ipxe.efi built with the embedded booty script}
SRV=10.77.0.1
mkdir -p $OUT/tftp $OUT/www
cp $IPXE_EFI $OUT/tftp/ipxe.efi
printf '#!ipxe\necho BOOTY-OK\nchain http://%s:8080/marker?mac=${mac} || shell\n' $SRV > $OUT/tftp/booty.ipxe
printf '#!ipxe\necho MARKER-OK mac=${mac} ip=${ip}\npoweroff\n' > $OUT/www/marker
cp /usr/share/OVMF/OVMF_VARS_4M.fd $OUT/vars.fd
cleanup() {
  [ -n "${QPID:-}" ] && kill $QPID 2>/dev/null
  [ -f $OUT/dnsmasq.pid ] && kill $(cat $OUT/dnsmasq.pid) 2>/dev/null
  [ -n "${HPID:-}" ] && kill $HPID 2>/dev/null
  [ -n "${TPID:-}" ] && kill $TPID 2>/dev/null
  wait 2>/dev/null
  ip link del tap0 2>/dev/null
  true
}
trap cleanup EXIT
cleanup
rm -f $OUT/*.log $OUT/*.pcap $OUT/*.pid $OUT/leases $OUT/start.ts
ip tuntap add tap0 mode tap
ip addr add $SRV/24 dev tap0; ip link set tap0 up
tcpdump -i tap0 -n -U -w $OUT/host.pcap 'udp or tcp port 8080 or arp or icmp' 2>/dev/null &
TPID=$!
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$OUT/dnsmasq.pid \
  --user=root --port=0 --interface=tap0 --bind-interfaces \
  --dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m \
  --dhcp-boot=ipxe.efi,,$SRV \
  --enable-tftp --tftp-root=$OUT/tftp \
  ${EXTRA_DNSMASQ:-} \
  --dhcp-leasefile=$OUT/leases --log-dhcp --log-facility=$OUT/dnsmasq.log &
python3 -u -m http.server 8080 --bind $SRV --directory $OUT/www > $OUT/http.log 2>&1 &
HPID=$!
sleep 1.5
START=$(date +%s.%N); echo $START > $OUT/start.ts
timeout ${QEMU_TIMEOUT:-240} qemu-system-x86_64 -machine q35 -accel tcg -m 512 \
  -drive if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
  -drive if=pflash,format=raw,unit=1,file=$OUT/vars.fd \
  -display none -serial file:$OUT/serial.log -monitor none -no-reboot \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -device virtio-net-pci,netdev=n0,mac=52:54:00:12:34:59,romfile= \
  -object filter-dump,id=fd0,netdev=n0,file=$OUT/guest.pcap &
QPID=$!
RESULT=TIMEOUT
for i in $(seq 1 $(( ${QEMU_TIMEOUT:-240} * 2 ))); do
  grep -q 'GET /marker' $OUT/http.log 2>/dev/null && { RESULT=MARKER; break; }
  kill -0 $QPID 2>/dev/null || { RESULT=QEMU_EXITED; break; }
  sleep 0.5
done
END=$(date +%s.%N)
echo "result=$RESULT elapsed_to_marker=$(echo "$END - $START" | bc) s"
if [ $RESULT = MARKER ]; then
  for i in $(seq 1 20); do kill -0 $QPID 2>/dev/null || break; sleep 0.5; done
  kill -0 $QPID 2>/dev/null && echo "qemu still running after marker" || echo "qemu exited after marker (guest poweroff)"
fi
