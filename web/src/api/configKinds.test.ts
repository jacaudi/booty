import { describe, expect, it } from 'vitest'
import type { FamilyKinds } from './catalog'
import {
  OS_CHOICES,
  SCHEMATIC_KIND,
  TALOSCLUSTER_KIND,
  bootConfigKinds,
  isBootConfigKind,
  kindForOS,
  kindsForHostOS,
  osNameForKind,
} from './configKinds'

const data: FamilyKinds = {
  bootConfigKinds: ['butane', 'machineconfig', 'debianconfig'],
  osFamily: { flatcar: ['butane'], 'fedora-coreos': ['butane'], talos: ['machineconfig'], debian: ['debianconfig'] },
}

describe('configKinds', () => {
  it('boot kinds come from the server and exclude preseed', () => {
    expect([...bootConfigKinds(data)].sort()).toEqual(['butane', 'debianconfig', 'machineconfig'])
  })

  it('kindsForHostOS maps the boot vocabulary, including the coreos alias', () => {
    expect(kindsForHostOS('talos', data)).toEqual(['machineconfig'])
    expect(kindsForHostOS('debian', data)).toEqual(['debianconfig'])
    expect(kindsForHostOS('flatcar', data)).toEqual(['butane'])
    expect(kindsForHostOS('fedora-coreos', data)).toEqual(['butane'])
    expect(kindsForHostOS('coreos', data)).toEqual(['butane']) // alias -> fedora-coreos
  })

  it('is permissive (full union) for unknown/absent OS', () => {
    expect(kindsForHostOS(undefined, data)).toEqual(data.bootConfigKinds)
    expect(kindsForHostOS('plan9', data)).toEqual(data.bootConfigKinds)
  })

  it('non-servable kinds stay non-servable', () => {
    expect(bootConfigKinds(data)).not.toContain(SCHEMATIC_KIND)
    expect(bootConfigKinds(data)).not.toContain(TALOSCLUSTER_KIND)
  })

  it('osNameForKind keeps UI labels and no longer maps preseed', () => {
    expect(osNameForKind('butane')).toBe('Flatcar / Fedora CoreOS')
    expect(osNameForKind('debianconfig')).toBe('Debian')
  })

  it('osNameForKind falls back to the raw kind for anything unmapped', () => {
    expect(osNameForKind('taloscluster')).toBe('taloscluster')
  })

  it('isBootConfigKind excludes only the page-owned non-servable constants', () => {
    // Data-free: SCHEMATIC_KIND and TALOSCLUSTER_KIND are UI-ownership facts
    // (owned by OS Images / Clusters), not server data.
    expect(isBootConfigKind(SCHEMATIC_KIND)).toBe(false)
    expect(isBootConfigKind(TALOSCLUSTER_KIND)).toBe(false)
    expect(isBootConfigKind('butane')).toBe(true)
    expect(isBootConfigKind('machineconfig')).toBe(true)
    expect(isBootConfigKind('debianconfig')).toBe(true)
  })

  it('offers exactly three OS choices, covering every OS booty supports', () => {
    expect(OS_CHOICES.map((o) => o.label)).toEqual([
      'Flatcar / Fedora CoreOS',
      'Talos Linux',
      'Debian',
    ])
  })

  it('each OS choice resolves its server kind from the loader data', () => {
    expect(kindForOS('flatcar-fcos', data)).toBe('butane')
    expect(kindForOS('talos', data)).toBe('machineconfig')
    expect(kindForOS('debian', data)).toBe('debianconfig')
    expect(kindForOS('nope', data)).toBeUndefined()
  })
})
