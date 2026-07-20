import { afterEach, describe, expect, it, vi } from 'vitest'
import { loadFamilyKinds, __resetCatalogCache } from './catalog'

afterEach(() => {
  __resetCatalogCache()
  vi.restoreAllMocks()
})

describe('loadFamilyKinds', () => {
  it('derives boot kinds and per-OS kinds from /os + /families', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
      const path = String(url)
      if (path.endsWith('/families')) {
        return new Response(JSON.stringify({ families: [
          { name: 'ignition', configKind: 'ignition', authoringKinds: ['butane'] },
          { name: 'machineconfig', configKind: 'machineconfig', authoringKinds: ['machineconfig'] },
          { name: 'debian', configKind: 'preseed', authoringKinds: ['debianconfig'] },
        ] }), { status: 200 })
      }
      return new Response(JSON.stringify({ os: [
        { name: 'flatcar', family: 'ignition', requiredParams: [] },
        { name: 'fedora-coreos', family: 'ignition', requiredParams: [] },
        { name: 'talos', family: 'machineconfig', requiredParams: [] },
        { name: 'debian', family: 'debian', requiredParams: [] },
      ] }), { status: 200 })
    })
    const data = await loadFamilyKinds()
    expect([...data.bootConfigKinds].sort()).toEqual(['butane', 'debianconfig', 'machineconfig'])
    expect(data.osFamily['talos']).toEqual(['machineconfig'])
    expect(data.osFamily['fedora-coreos']).toEqual(['butane'])
  })
})
