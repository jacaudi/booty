#!/bin/bash
# Recipe (a): iPXE NIC ROM gets DHCP from dnsmasq on tap0 and TFTPs from next-server.
set -u
D=$(cd "$(dirname "$0")" && pwd)
[ -f $D/dnsmasq.pid ] && kill $(cat $D/dnsmasq.pid) 2>/dev/null
ip link show tap0 >/dev/null 2>&1 || ip tuntap add tap0 mode tap
ip addr flush dev tap0; ip addr add 10.77.0.1/24 dev tap0; ip addr add 10.77.0.2/24 dev tap0; ip link set tap0 up
rm -f $D/dnsmasq.log $D/tcpdump.txt $D/leases $D/serial.log
tcpdump -i tap0 -n -l -e 'udp port 67 or udp port 68 or udp port 69 or arp or icmp' > $D/tcpdump.txt 2>&1 &
TPID=$!
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$D/dnsmasq.pid \
  --user=root --port=0 --interface=tap0 --bind-interfaces \
  --dhcp-range=10.77.0.100,10.77.0.150,255.255.255.0,10m \
  --dhcp-boot=boot.ipxe,booty-smoke,${NEXT_SERVER:-10.77.0.1} \
  $([ "${TFTP:-1}" = 1 ] && echo --enable-tftp --tftp-root=$D/tftp) \
  --dhcp-leasefile=$D/leases --log-dhcp --log-facility=$D/dnsmasq.log &
DPID=$!
sleep 1
START=$(date +%s.%N)
timeout ${QEMU_TIMEOUT:-120} qemu-system-x86_64 -machine pc -accel tcg -m 256 \
  -display none -serial file:$D/serial.log -monitor none -no-reboot -boot n \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -device e1000,netdev=n0,mac=52:54:00:12:34:56 &
QPID=$!
# wait until TFTP transfer completes or timeout
for i in $(seq 1 240); do
  grep -q 'sent .*boot.ipxe' $D/dnsmasq.log 2>/dev/null && break; grep -q 'RRQ' $D/tcpdump.txt 2>/dev/null && break
  kill -0 $QPID 2>/dev/null || break
  sleep 0.5
done
END=$(date +%s.%N)
echo "elapsed_to_tftp_sent=$(echo "$END - $START" | bc) s"
sleep 3
kill $QPID 2>/dev/null; wait $QPID 2>/dev/null
kill $DPID $TPID 2>/dev/null; wait 2>/dev/null
