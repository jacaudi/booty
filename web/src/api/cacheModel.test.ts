import { describe, expect, it } from 'vitest'
import type { CacheEntry } from './cache'
import { applyClientFilters, channelOf, groupEntries, humanSize, summarize } from './cacheModel'

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
})
