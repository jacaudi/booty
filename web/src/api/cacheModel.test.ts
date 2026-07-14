import { describe, expect, it } from 'vitest'
import type { CacheEntry } from './cache'
import { applyClientFilters, channelOf, groupEntries, humanSize, labelGroup, summarize } from './cacheModel'

const e = (o: Partial<CacheEntry>): CacheEntry => ({
  id: 1, os: 'talos', arch: 'amd64', version: 'v1.0.0', size: 1024,
  state: 'in-cycle', pinned: false, inWindow: true, fetchedAt: '', ...o,
})

describe('cacheModel', () => {
  it('humanSize renders B/KB/MB', () => {
    expect(humanSize(512)).toBe('512 B')
    expect(humanSize(1024)).toBe('1.0 KB')
    expect(humanSize(1024 * 1024)).toBe('1.0 MB')
  })

  it('channelOf prefers params.channel, falls back to os', () => {
    expect(channelOf(e({ os: 'flatcar', params: { channel: 'stable' } }))).toBe('stable')
    expect(channelOf(e({ os: 'talos', params: {} }))).toBe('talos')
  })

  it('groups by os/channel with size + version rollups, sorted by key', () => {
    const groups = groupEntries([
      e({ id: 1, os: 'talos', version: 'v1', size: 100 }),
      e({ id: 2, os: 'talos', version: 'v2', size: 200 }),
      e({ id: 3, os: 'flatcar', params: { channel: 'stable' }, version: '3', size: 50 }),
    ])
    expect(groups.map((g) => g.key)).toEqual(['flatcar/stable', 'talos/talos'])
    const talos = groups.find((g) => g.key === 'talos/talos')!
    expect(talos.versionCount).toBe(2)
    expect(talos.totalSize).toBe(300)
  })

  it('summarize counts states, used bytes, failed, and nothing-evictable', () => {
    const s = summarize([
      e({ id: 1, state: 'in-cycle', size: 10 }),
      e({ id: 2, state: 'archived', size: 20, verified: false }),
      e({ id: 3, state: 'archived-pinned', size: 30, pinned: true }),
    ])
    expect(s.usedBytes).toBe(60)
    expect(s.inCycle).toBe(1)
    expect(s.archived).toBe(2)
    expect(s.pinned).toBe(1)
    expect(s.failed).toBe(1)
    // entry 2 is archived && !pinned -> evictable exists -> not nothing-evictable
    expect(s.nothingEvictable).toBe(false)
  })

  it('nothingEvictable is true when every archived entry is pinned', () => {
    const s = summarize([e({ id: 1, state: 'archived-pinned', pinned: true })])
    expect(s.nothingEvictable).toBe(true)
  })

  it('applyClientFilters filters by version substring and failedOnly', () => {
    const entries = [
      e({ id: 1, version: 'v1.2.3', verified: true }),
      e({ id: 2, version: 'v1.3.0', verified: false }),
    ]
    expect(applyClientFilters(entries, { version: '1.2' }).map((x) => x.id)).toEqual([1])
    expect(applyClientFilters(entries, { failedOnly: true }).map((x) => x.id)).toEqual([2])
  })

  it('channelOf falls back to params.schematic before os (Talos carries no channel)', () => {
    expect(channelOf(e({ os: 'talos', params: { schematic: 'abc' } }))).toBe('abc')
    // A channel still wins where one exists (flatcar / fcos).
    expect(channelOf(e({ os: 'flatcar', params: { channel: 'stable' } }))).toBe('stable')
    // No params at all -> the OS, as before.
    expect(channelOf(e({ os: 'talos', params: {} }))).toBe('talos')
  })

  it('two schematics of the same os+arch land in SEPARATE groups', () => {
    // This is the live bug: today both collapse into one talos/talos group.
    const groups = groupEntries([
      e({ id: 1, os: 'talos', params: { schematic: 'aaa' }, version: 'v1', size: 100 }),
      e({ id: 2, os: 'talos', params: { schematic: 'bbb' }, version: 'v1', size: 200 }),
    ])
    expect(groups).toHaveLength(2)
    expect(groups.map((g) => g.schematic).sort()).toEqual(['aaa', 'bbb'])
  })

  it('labelGroup names a group after the live schematic whose derived id it matches', () => {
    const id = `43fac7${'0'.repeat(54)}1367`
    const [g] = groupEntries([e({ os: 'talos', params: { schematic: id } })])
    expect(labelGroup(g, [{ name: 'rpi4-tailscale', derivedSchematicId: id }])).toEqual({
      title: 'talos · rpi4-tailscale',
      subtitle: 'schematic 43fac7…1367',
    })
  })

  it('labelGroup names the seeded vanilla schematic like any other', () => {
    // SeedVanillaSchematic (pkg/http/schematic.go:93-130, wired at cmd/main.go:338)
    // creates a kind=schematic config named "vanilla" carrying the constant
    // DefaultTalosSchematic id. So the PREDEFINED default cache target
    // (pkg/cache/seed.go:53) names itself, with no special-casing here.
    const vanilla = '376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba'
    const [g] = groupEntries([e({ os: 'talos', params: { schematic: vanilla } })])
    expect(labelGroup(g, [{ name: 'vanilla', derivedSchematicId: vanilla }])).toEqual({
      title: 'talos · vanilla',
      subtitle: 'schematic 376567…b4ba',
    })
  })

  it('labelGroup makes NO claim about a schematic target it cannot match', () => {
    // A schematic-keyed target has FOUR sources and only one is a schematic
    // config (pkg/cache/seed.go:41-77): the predefined default, host-bound raw
    // IDs (this UI's own Import-by-ID creates these and deliberately creates no
    // config), cluster members, and configs. CacheEntryDTO carries no provenance
    // (api_cache.go:16-28), so an unmatched group may be a stranded target OR an
    // image a host is booting right now. Calling it "unreferenced" would invite
    // an operator to reap a running host's images. Show the id; claim nothing.
    const id = `9f21ab${'0'.repeat(54)}7c40`
    const [g] = groupEntries([e({ os: 'talos', params: { schematic: id } })])
    expect(labelGroup(g, [{ name: 'other', derivedSchematicId: 'deadbeef' }])).toEqual({
      title: 'talos · 9f21ab…7c40',
    })
    // Identical when the catalogue could not be loaded at all.
    expect(labelGroup(g, undefined)).toEqual({ title: 'talos · 9f21ab…7c40' })
  })

  it('labelGroup leaves a non-schematic group as its plain os/channel key', () => {
    const [g] = groupEntries([e({ os: 'flatcar', params: { channel: 'stable' } })])
    expect(labelGroup(g, [])).toEqual({ title: 'flatcar/stable' })
  })
})
