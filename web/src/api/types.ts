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
  health?: HostHealthSummary
}

// Mirrors nodereport.Summary (pkg/nodereport/health.go).
export interface HostHealthSummary {
  running: string
  kernel: string
  firmware: string
  receivedAt: string
  failedUnitCount: number
  vendor: string
  product: string
}

// Mirrors nodereport.Health (pkg/nodereport/health.go).
export interface HostHealth {
  receivedAt: string
  bootID: string
  running: string
  failedUnits: string[]
  journalErrors: string[]
  dmi: { vendor: string; product: string; biosVersion: string }
  firmware: string
  kernel: string
}

// A host is pending iff it is not approved. hardware.Host serializes `approved`
// with omitzero, so an unapproved host OMITS the field (undefined) — testing
// `=== false` misses every real pending host. This is the single source for
// that rule; use it everywhere pending hosts are counted or listed.
export function isPending(host: Host): boolean {
  return !host.approved
}

// Mirrors a row of GET /api/v1/hosts `discovered`: a machine that booted but has
// no hosts row yet. Times are RFC 3339.
export interface DiscoveredHost {
  mac: string
  arch: string
  ip: string
  firstSeen: string
  lastSeen: string
}
