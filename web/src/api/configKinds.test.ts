import { describe, expect, it } from 'vitest'
import {
  BOOT_CONFIG_KINDS,
  OS_CHOICES,
  SCHEMATIC_KIND,
  TALOSCLUSTER_KIND,
  isBootConfigKind,
  kindForOS,
  kindsForHostOS,
  osNameForKind,
} from './configKinds'

describe('configKinds', () => {
  it('the boot-config kinds are exactly the four kinds renderConfig can serve', () => {
    expect([...BOOT_CONFIG_KINDS].sort()).toEqual(['butane', 'debianconfig', 'machineconfig', 'preseed'])
  })

  it('schematic and taloscluster are NOT boot-config kinds', () => {
    // familyAllowsKind (render.go:34-43) allows neither, so resolveConfig falls
    // through to the default file with only a slog.Warn — a bound config and an
    // unbound boot. They must never be offered as a host/role config.
    expect(isBootConfigKind(SCHEMATIC_KIND)).toBe(false)
    expect(isBootConfigKind(TALOSCLUSTER_KIND)).toBe(false)
    expect(isBootConfigKind('butane')).toBe(true)
    expect(isBootConfigKind('machineconfig')).toBe(true)
    expect(isBootConfigKind('debianconfig')).toBe(true)
    expect(isBootConfigKind('preseed')).toBe(true)
  })

  it('offers exactly three OS choices, covering every OS booty supports', () => {
    expect(OS_CHOICES.map((o) => o.label)).toEqual([
      'Flatcar / Fedora CoreOS',
      'Talos Linux',
      'Debian',
    ])
  })

  it('each OS choice derives its server kind', () => {
    expect(kindForOS('flatcar-fcos')).toBe('butane')
    expect(kindForOS('talos')).toBe('machineconfig')
    expect(kindForOS('debian')).toBe('debianconfig')
    expect(kindForOS('nope')).toBeUndefined()
  })

  it('raw preseed is offered as no OS choice (structured debianconfig only)', () => {
    expect(OS_CHOICES.some((o) => o.kind === 'preseed')).toBe(false)
  })

  it('osNameForKind leads with the OS product name, including the legacy preseed fallback', () => {
    expect(osNameForKind('machineconfig')).toBe('Talos Linux')
    expect(osNameForKind('butane')).toBe('Flatcar / Fedora CoreOS')
    expect(osNameForKind('debianconfig')).toBe('Debian')
    expect(osNameForKind('preseed')).toBe('Debian')
  })

  it('osNameForKind falls back to the raw kind for anything unmapped', () => {
    expect(osNameForKind('taloscluster')).toBe('taloscluster')
  })

  it('kindsForHostOS admits only what the host OS family allows', () => {
    // familyAllowsKind is PER-FAMILY (render.go:34-43). Binding a butane config
    // to a Talos host fails on the same code path, with the same silent
    // fall-through to the default file, as binding a taloscluster.
    expect(kindsForHostOS('talos')).toEqual(['machineconfig'])
    expect([...kindsForHostOS('debian')].sort()).toEqual(['debianconfig', 'preseed'])
    expect(kindsForHostOS('flatcar')).toEqual(['butane'])
    expect(kindsForHostOS('fedora-coreos')).toEqual(['butane'])
    // The cache vocabulary calls it "coreos"; resolve.go bridges the two names
    // via CacheNameToCanonical, so both must resolve here.
    expect(kindsForHostOS('coreos')).toEqual(['butane'])
  })

  it('kindsForHostOS is PERMISSIVE for a host whose OS is unknown or absent', () => {
    // A host that has not booted yet has no OS. Hiding every option would be a
    // worse failure than offering one the server will reject loudly.
    expect(kindsForHostOS(undefined)).toEqual(BOOT_CONFIG_KINDS)
    expect(kindsForHostOS('plan9')).toEqual(BOOT_CONFIG_KINDS)
  })
})
