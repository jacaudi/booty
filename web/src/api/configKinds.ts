// The OS-family <-> kind mapping is knowledge the SERVER owns (familyAllowsKind,
// pkg/http/render.go:34-43) but does not expose over the API. The frontend must
// keep a copy; this module is the one place that copy lives. Follow-up issue:
// expose the mapping via the API and delete the duplication.
//
// Drift fails safe: a new server kind is simply not offered by the UI, and a
// tightened familyAllowsKind yields a loud 422 — never silent corruption.

// A schematic is an IMAGE identity, not a boot config: it is POSTed to the Talos
// Image Factory, which returns a content-addressed derived ID (api_configs.go:331-336).
// No serving path can render one.
export const SCHEMATIC_KIND = 'schematic' as const

// A taloscluster is a CLUSTER SPEC, layered into every generated node config
// (api_clusters.go:365-372). It is owned by the Clusters page, not Boot Configs.
export const TALOSCLUSTER_KIND = 'taloscluster' as const

// The kinds renderConfig can actually serve to a machine (render.go:59-84).
//
// This single set is BOTH what the Configs list shows AND what a host's or a
// role's config Select may offer — because a config is bindable exactly when it
// is renderable: resolveConfig gates every binding rung through the very same
// familyAllowsKind that gates rendering (resolve.go:30,65). Binding a non-member
// (a schematic, a taloscluster) silently resolves to the server-default file with
// only a slog.Warn: the user sees a bound config and gets an unbound boot.
export const BOOT_CONFIG_KINDS = ['butane', 'machineconfig', 'debianconfig', 'preseed'] as const

export type BootConfigKind = (typeof BOOT_CONFIG_KINDS)[number]

export function isBootConfigKind(kind: string): kind is BootConfigKind {
  return (BOOT_CONFIG_KINDS as readonly string[]).includes(kind)
}

// The create picker offers ONE choice — the OS. The kind follows from it; there
// is no format choice and no content sniffing. These three cover every OS booty
// supports (pkg/ostype registers four OSes across three families).
//
// Raw `preseed` is deliberately absent: debianconfig and preseed serve
// byte-identical text/plain at /preseed (render.go:71-81), differing only in
// authoring format, and debianconfig is the structured one. Bringing an existing
// preseed is a CLI concern. An existing preseed row still lists, edits and
// validates — it simply cannot be newly authored here.
export interface OSChoice {
  value: string
  label: string
  kind: BootConfigKind
}

export const OS_CHOICES: OSChoice[] = [
  { value: 'flatcar-fcos', label: 'Flatcar / Fedora CoreOS', kind: 'butane' },
  { value: 'talos', label: 'Talos Linux', kind: 'machineconfig' },
  { value: 'debian', label: 'Debian', kind: 'debianconfig' },
]

export function kindForOS(os: string): BootConfigKind | undefined {
  return OS_CHOICES.find((o) => o.value === os)?.kind
}

// The Kind cell leads with the OS product name and shows the literal server kind
// beneath it. `butane` names TWO OSes because the server has one `ignition`
// family serving both (ostype/ignition.go:20-21) and nothing on the config says
// which — naming both is the honest reading, not a compromise.
const KIND_OS_NAMES: Record<BootConfigKind, string> = {
  butane: 'Flatcar / Fedora CoreOS',
  machineconfig: 'Talos Linux',
  debianconfig: 'Debian',
  preseed: 'Debian',
}

export function osNameForKind(kind: string): string {
  return isBootConfigKind(kind) ? KIND_OS_NAMES[kind] : kind
}

// Which kinds a given HOST may actually be bound. BOOT_CONFIG_KINDS above is the
// union across families, but familyAllowsKind is PER-FAMILY (render.go:34-43) —
// so a butane config bound to a Talos host is rejected by exactly the same code
// path, with exactly the same silent fall-through to the server-default file, as
// a taloscluster. This is `familyAllowsKind ∘ osFamily` (resolve.go:89-95),
// mirrored:
//
//   talos                    -> talos family    -> machineconfig
//   debian                   -> debian family   -> preseed | debianconfig
//   flatcar / fedora-coreos  -> ignition family -> butane
//
// "coreos" is the cache vocabulary's name for fedora-coreos; resolve.go bridges
// the two via CacheNameToCanonical, so both spellings resolve here.
const HOST_OS_KINDS: Record<string, readonly BootConfigKind[]> = {
  talos: ['machineconfig'],
  debian: ['preseed', 'debianconfig'],
  flatcar: ['butane'],
  'fedora-coreos': ['butane'],
  coreos: ['butane'],
}

// PERMISSIVE on an unknown or absent OS: a host that has not booted yet has no
// OS, and hiding every option would be a worse failure than offering one the
// server rejects loudly. Unknown -> the full union.
export function kindsForHostOS(os?: string): readonly BootConfigKind[] {
  return (os && HOST_OS_KINDS[os]) || BOOT_CONFIG_KINDS
}
