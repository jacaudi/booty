// A small static starter set of common Talos system extensions. Fetching the
// live catalogue from the Factory is a documented follow-up (see issue #32).
export interface CatalogueEntry {
  name: string
  description: string
}

export const EXTENSION_CATALOGUE: CatalogueEntry[] = [
  { name: 'siderolabs/iscsi-tools', description: 'iSCSI initiator tools (open-iscsi)' },
  { name: 'siderolabs/util-linux-tools', description: 'util-linux userspace utilities' },
  { name: 'siderolabs/gvisor', description: 'gVisor container runtime sandbox' },
  { name: 'siderolabs/intel-ucode', description: 'Intel CPU microcode updates' },
  { name: 'siderolabs/amd-ucode', description: 'AMD CPU microcode updates' },
  { name: 'siderolabs/nvidia-container-toolkit', description: 'NVIDIA container toolkit' },
  { name: 'siderolabs/tailscale', description: 'Tailscale mesh VPN' },
]
