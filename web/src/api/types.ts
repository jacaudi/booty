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
