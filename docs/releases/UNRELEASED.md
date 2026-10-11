## Security

- TFTP now serves only cache/, public/ and the iPXE bootfile names from --dataDir. Previously any file there, including api-token and booty.db, was readable by any host that could reach UDP/69. If untrusted hosts could reach it, rotate the API token: booty token rotate --yes. Move any other top-level files you served over TFTP under public/.
- A Talos cluster member's machine config is now served only when the request's source IP ARP-resolves to that member's MAC. Previously anyone who knew a member's MAC could fetch it, including the cluster CA private keys. Members booted through a DHCP relay cannot be ARP-resolved and are refused until authenticated config fetch is designed.

## Changes

- Hostnames are validated as RFC 1123: create-host rejects an invalid hostname with 400; /machineconfig ignores (and logs) an invalid ?hostname= from DHCP; a debianconfig whose templated hostname or domain contains a control character is refused; stored hostnames have control characters stripped when rendered.
