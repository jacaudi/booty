import type { FamilyKinds } from './catalog'

// The OS-family <-> kind compatibility rule is knowledge the SERVER owns
// (familyAllowsKind / authoringKindsForFamily) and now PUBLISHES via
// loadFamilyKinds() (/families + /os). This module no longer hand-maintains a
// copy of that rule — it derives from the `FamilyKinds` the loader returns and
// keeps only UI-owned presentation (labels, grouping, non-servable constants).

// A schematic is an IMAGE identity, not a boot config: it is POSTed to the Talos
// Image Factory, which returns a content-addressed derived ID (api_configs.go:331-336).
// No serving path can render one.
export const SCHEMATIC_KIND = 'schematic' as const

// A taloscluster is a CLUSTER SPEC, layered into every generated node config
// (api_clusters.go:365-372). It is owned by the Clusters page, not Boot Configs.
export const TALOSCLUSTER_KIND = 'taloscluster' as const

// Whether a kind is one renderConfig could ever serve to a machine. This is a
// UI-ownership fact, not server data: SCHEMATIC_KIND and TALOSCLUSTER_KIND are
// owned by other pages (OS Images, Clusters) and are never boot configs,
// regardless of what the server's authoring-kind vocabulary contains. Every
// other kind string (butane, machineconfig, debianconfig, and any the server
// adds later) is a boot-config kind.
export function isBootConfigKind(kind: string): boolean {
  return kind !== SCHEMATIC_KIND && kind !== TALOSCLUSTER_KIND
}

// The kinds renderConfig can actually serve, straight from the server's
// published authoring-kind vocabulary (loadFamilyKinds()/`/families`).
export function bootConfigKinds(data: FamilyKinds): string[] {
  return data.bootConfigKinds
}

// The Kind cell leads with the OS product name and shows the literal server kind
// beneath it. `butane` names TWO OSes because the server has one `ignition`
// family serving both (ostype/ignition.go:20-21) and nothing on the config says
// which — naming both is the honest reading, not a compromise.
const KIND_OS_NAMES: Record<string, string> = {
  butane: 'Flatcar / Fedora CoreOS',
  machineconfig: 'Talos Linux',
  debianconfig: 'Debian',
}

export function osNameForKind(kind: string): string {
  return KIND_OS_NAMES[kind] ?? kind
}

// The create picker offers ONE choice — the OS. The kind follows from it; there
// is no format choice and no content sniffing. These three cover every OS booty
// supports (pkg/ostype registers four OSes across three families).
export interface OSChoice {
  value: string
  label: string
}

export const OS_CHOICES: OSChoice[] = [
  { value: 'flatcar-fcos', label: 'Flatcar / Fedora CoreOS' },
  { value: 'talos', label: 'Talos Linux' },
  { value: 'debian', label: 'Debian' },
]

// Bridges an OS_CHOICES picker value to the OS name the loader's `osFamily` map
// is keyed by. `flatcar-fcos` groups two OS names under one label; both resolve
// to the same kind (the shared `ignition` family), so either name works as the
// lookup key.
const OS_CHOICE_OS_NAME: Record<string, string> = {
  'flatcar-fcos': 'flatcar',
  talos: 'talos',
  debian: 'debian',
}

// Booty's boot vocabulary calls fedora-coreos "coreos"; the server's /os speaks
// the canonical name only, so the frontend keeps this one declared alias (the
// same class of UI vocabulary as the flatcar/fedora-coreos label grouping).
const OS_ALIAS: Record<string, string> = { coreos: 'fedora-coreos' }

// ---------------------------------------------------------------------------
// BRIDGE for Task 10 (#61): BootConfigsView.tsx:72,262 and HostsView.tsx:89
// still call `kindForOS`/`kindsForHostOS` with one argument. Once those views
// are rewired to load `FamilyKinds` and pass `data`, delete the `data`-less
// branches below along with LEGACY_OS_CHOICE_KIND and LEGACY_HOST_OS_KINDS,
// and make `data` a required parameter.
// ---------------------------------------------------------------------------

const LEGACY_OS_CHOICE_KIND: Record<string, string> = {
  'flatcar-fcos': 'butane',
  talos: 'machineconfig',
  debian: 'debianconfig',
}

const LEGACY_HOST_OS_KINDS: Record<string, readonly string[]> = {
  talos: ['machineconfig'],
  debian: ['debianconfig'],
  flatcar: ['butane'],
  'fedora-coreos': ['butane'],
}

const LEGACY_BOOT_CONFIG_KINDS = ['butane', 'machineconfig', 'debianconfig'] as const

export function kindForOS(os: string, data?: FamilyKinds): string | undefined {
  if (!data) return LEGACY_OS_CHOICE_KIND[os] // BRIDGE — see note above
  return data.osFamily[OS_CHOICE_OS_NAME[os] ?? os]?.[0]
}

// Which kinds a given HOST may actually be bound. PERMISSIVE on an unknown or
// absent OS: a host that has not booted yet has no OS, and hiding every option
// would be a worse failure than offering one the server rejects loudly (422 on
// bind).
export function kindsForHostOS(os: string | undefined, data?: FamilyKinds): string[] {
  if (!data) {
    // BRIDGE — see note above.
    if (!os) return [...LEGACY_BOOT_CONFIG_KINDS]
    const canonical = OS_ALIAS[os] ?? os
    return [...(LEGACY_HOST_OS_KINDS[canonical] ?? LEGACY_BOOT_CONFIG_KINDS)]
  }
  if (!os) return data.bootConfigKinds
  const canonical = OS_ALIAS[os] ?? os
  return data.osFamily[canonical] ?? data.bootConfigKinds
}
