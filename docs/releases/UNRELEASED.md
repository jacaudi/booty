## Security

- TFTP now serves only cache/, public/ and the iPXE bootfile names from --dataDir. Previously any file there, including api-token and booty.db, was readable by any host that could reach UDP/69. If untrusted hosts could reach it, rotate the API token: booty token rotate --yes. Move any other top-level files you served over TFTP under public/.
- A Talos cluster member's machine config is now served only when the request's source IP ARP-resolves to that member's MAC. Previously anyone who knew a member's MAC could fetch it, including the cluster CA private keys. Members booted through a DHCP relay cannot be ARP-resolved and are refused until authenticated config fetch is designed.

## Features

- booty embeds iPXE v2.0.0 (undionly.kpxe, ipxe.efi, snponly.efi) and serves it over TFTP and /boot/; staging iPXE in --dataDir is no longer needed (except ARM64).

## Changes

- booty.ipxe is now rendered over HTTP (/booty.ipxe?mac=) and identifies hosts by the MAC iPXE reports; TFTP serves a one-line stub. DoInstall is no longer cleared on boot; it never changed what booted.
- TFTP refuses write requests with an error instead of an invalid reply.
- TFTP no longer follows absolute symlinks, or symlinks leading out of --dataDir. booty itself creates none.
- New flag --tftpPort (default 69).
- proxyDHCP no longer answers the client's port-67 REQUEST for its real lease (UEFI firmware declined the lease and the boot failed).
- proxyDHCP ignores PXE packets without client architecture (option 93) and INFORM; very old BIOS boot ROMs that omit option 93 need the site DHCP server pointed at booty.
- New --proxyDHCPRelay.
- --serverIP is detected from the default route when unset. Hosts or containers with no default route (for example --network none) now refuse to start until --serverIP is set; previously they started on 127.0.0.1, which no client can reach.
- Butane 0.29 / Ignition 2.27: the fcos 1.6.0-experimental variant no longer translates (use fcos 1.6.0); flatcar 1.2.0-experimental now emits Ignition 3.7.0-experimental. Stable variants are unchanged.
- proxyDHCP now boots UEFI firmware that sits beside another DHCP server: the PXE boot item uses vendor type 0x8000 (UEFI treated the previous type 0 as "boot locally" and cancelled with PXE-E21), and the boot-server ACK goes to the port the firmware sent from (4011).
- Hostnames are validated as RFC 1123: create-host rejects an invalid hostname with 400; /machineconfig ignores (and logs) an invalid ?hostname= from DHCP; a debianconfig whose templated hostname or domain contains a control character is refused; stored hostnames have control characters stripped when rendered.
- The legacy PXELINUX path (pxelinux.cfg/default) is removed; it could no longer boot under the TFTP allowlist. Legacy BIOS PXE firmware chains booty's embedded undionly.kpxe instead.
- Flatcar, CoreOS and Debian config URLs carry ?mac=${mac}, so hosts are identified without ARP.
- The reboot config served to unknown hosts was invalid Ignition (its unit had no .service suffix), so Flatcar/CoreOS could stop at an Ignition error instead of rebooting; the unit is now booty-reboot.service.
