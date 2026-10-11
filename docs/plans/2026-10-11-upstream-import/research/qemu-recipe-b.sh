#!/bin/bash
# Recipe (b): guest NIC on a QEMU hub joined to slirp (restrict=on) AND tap0;
# dnsmasq runs proxy-DHCP on tap0. Question: does the guest see offers from both?
set -u
D=$(cd "$(dirname "$0")" && pwd)
TAPNET=${TAPNET:-10.0.2}          # tap0 address lives in slirp's subnet by default
TAPIP=$TAPNET.50
[ -f $D/dnsmasq.pid ] && kill $(cat $D/dnsmasq.pid) 2>/dev/null
ip link show tap0 >/dev/null 2>&1 || ip tuntap add tap0 mode tap
ip addr flush dev tap0; ip addr add $TAPIP/24 dev tap0; ip link set tap0 up
rm -f $D/dnsmasq.log $D/tcpdump.txt $D/guest.pcap
tcpdump -i tap0 -n -l -e 'udp port 67 or udp port 68 or udp port 69 or udp port 4011 or arp or icmp' > $D/tcpdump.txt 2>&1 &
TPID=$!
dnsmasq --conf-file=/dev/null --keep-in-foreground --pid-file=$D/dnsmasq.pid \
  --user=root --port=0 --interface=tap0 --bind-interfaces \
  --dhcp-range=$TAPNET.0,proxy \
  --dhcp-boot=boot.ipxe,,$TAPIP \
  ${PXESVC:+--pxe-service=x86PC,"booty",boot.ipxe,$TAPIP} \
  --enable-tftp --tftp-root=$D/tftp \
  --log-dhcp --log-facility=$D/dnsmasq.log &
DPID=$!
sleep 1
START=$(date +%s.%N)
timeout ${QEMU_TIMEOUT:-90} qemu-system-x86_64 -machine pc -accel tcg -m 256 \
  -display none -serial none -monitor none -no-reboot -boot n \
  -netdev user,id=u0,restrict=on,net=10.0.2.0/24 \
  -netdev hubport,id=hp_user,hubid=0,netdev=u0 \
  -netdev tap,id=t0,ifname=tap0,script=no,downscript=no \
  -netdev hubport,id=hp_tap,hubid=0,netdev=t0 \
  -netdev hubport,id=hp_nic,hubid=0 \
  -device e1000,netdev=hp_nic,mac=52:54:00:12:34:57 \
  -object filter-dump,id=fd0,netdev=hp_nic,file=$D/guest.pcap &
QPID=$!
for i in $(seq 1 ${WAIT_HALF_SECS:-60}); do
  grep -q 'sent .*boot.ipxe' $D/dnsmasq.log 2>/dev/null && break
  kill -0 $QPID 2>/dev/null || break
  sleep 0.5
done
END=$(date +%s.%N)
echo "elapsed=$(echo "$END - $START" | bc) s"
sleep 3
kill $QPID 2>/dev/null; wait $QPID 2>/dev/null
kill $DPID $TPID 2>/dev/null; wait 2>/dev/null
