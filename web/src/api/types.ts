// Mirrors hardware.Host (pkg/hardware/mac.go). omitempty/omitzero Go fields are
// optional here. bootMode is 'assigned' | 'menu'.
export interface Host {
  mac: string
  hostname: string
  ip: string
  booted: string
  ignitionFile?: string
  os?: string
  doInstall?: boolean
  schematic?: string
  approved?: boolean
  bootMode?: 'assigned' | 'menu'
  assignedOS?: string
  assignedArch?: string
  assignedParams?: string
  uuid?: string
  serial?: string
}

// A host is pending iff it is not approved. hardware.Host serializes `approved`
// with omitzero, so an unapproved host OMITS the field (undefined) — testing
// `=== false` misses every real pending host. This is the single source for
// that rule; use it everywhere pending hosts are counted or listed.
export function isPending(host: Host): boolean {
  return !host.approved
}
