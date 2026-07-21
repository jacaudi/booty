import type { CacheEntry } from './cache'
import { shortSchematicId } from './schematicId'

export function humanSize(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

// The group discriminator within an OS. Talos cache targets are keyed by their
// Factory-derived SCHEMATIC id (pkg/cache/schematic.go: params = {"schematic": …})
// and carry no channel; flatcar / fcos carry {"channel": …}. Never both.
//
// Falling straight through to `os` — the old behavior — collapsed every distinct
// schematic into a single talos/talos group, silently merging unrelated images.
export function channelOf(e: CacheEntry): string {
  return e.params?.channel ?? e.params?.schematic ?? e.os
}

// Module-private: groupEntries is its only consumer. Do not export it — nothing
// outside this file needs it, and an unused export is a maintenance liability.
function schematicOf(e: CacheEntry): string | undefined {
  return e.params?.schematic
}

export function groupKey(e: CacheEntry): string {
  return `${e.os}/${channelOf(e)}`
}

export interface CacheGroup {
  key: string
  os: string
  channel: string
  schematic?: string
  entries: CacheEntry[]
  totalSize: number
  versionCount: number
}

export function groupEntries(entries: CacheEntry[]): CacheGroup[] {
  const byKey = new Map<string, CacheEntry[]>()
  for (const e of entries) {
    const k = groupKey(e)
    const list = byKey.get(k)
    if (list) list.push(e)
    else byKey.set(k, [e])
  }
  return [...byKey.entries()]
    .map(([key, es]) => ({
      key,
      os: es[0].os,
      channel: channelOf(es[0]),
      schematic: schematicOf(es[0]),
      entries: es,
      totalSize: es.reduce((sum, e) => sum + e.size, 0),
      versionCount: es.length,
    }))
    .sort((a, b) => a.key.localeCompare(b.key))
}

export interface CacheSummary {
  usedBytes: number
  inCycle: number
  archived: number
  pinned: number
  failed: number
  nothingEvictable: boolean
}

// Single source of truth for "how many cached images failed verification".
// A cache entry is failed iff verify ran and reported false; `undefined`/`null`
// (never verified) is NOT a failure. Reads only `verified`, so it is safe on
// partial entries (unlike summarize(), which also dereferences `state`).
export function failedCount(entries: CacheEntry[]): number {
  return entries.filter((e) => e.verified === false).length
}

export function summarize(entries: CacheEntry[]): CacheSummary {
  const evictable = entries.filter((e) => e.state.startsWith('archived') && !e.pinned)
  return {
    usedBytes: entries.reduce((sum, e) => sum + e.size, 0),
    inCycle: entries.filter((e) => e.state.startsWith('in-cycle')).length,
    archived: entries.filter((e) => e.state.startsWith('archived')).length,
    pinned: entries.filter((e) => e.pinned).length,
    failed: failedCount(entries),
    nothingEvictable: evictable.length === 0,
  }
}

export function applyClientFilters(
  entries: CacheEntry[],
  opts: { version?: string; failedOnly?: boolean },
): CacheEntry[] {
  return entries.filter((e) => {
    if (opts.version && !e.version.includes(opts.version)) return false
    if (opts.failedOnly && e.verified !== false) return false
    return true
  })
}

// The subset of a schematic Config the cache needs in order to name a group.
export interface SchematicRef {
  name: string
  derivedSchematicId?: string
}

export interface GroupLabel {
  title: string
  subtitle?: string
}

// Name a cache group. A schematic-keyed group is named after the live schematic
// config whose derived id it matches — this is what makes the two OS Images tabs
// agree with each other, and it covers the catalog default Talos target for free
// (SeedVanillaSchematic seeds a "vanilla" config carrying the constant id).
//
// An UNMATCHED group gets its short id and NOTHING ELSE — deliberately. It is
// tempting to call it an orphan (editing a schematic really does strand its old
// target forever, pkg/cache/schematic.go:20-22), but a schematic-keyed target has
// four sources and only one is a config: host-bound raw IDs — which this very
// UI's Import-by-ID creates, with no config by design — and cluster-member
// schematics are configless AND IN ACTIVE USE. CacheEntryDTO exposes no
// provenance (api_cache.go:16-28), so we cannot tell a stranded target
// from an image a host is booting right now. Claiming "not referenced" would
// invite an operator to reap a running host's images. Show the id; claim nothing.
// Surfacing true orphans needs target provenance on the API first (issue, Task 13).
export function labelGroup(g: CacheGroup, schematics?: SchematicRef[]): GroupLabel {
  if (!g.schematic) return { title: g.key }

  const short = shortSchematicId(g.schematic)
  const match = schematics?.find((s) => s.derivedSchematicId === g.schematic)
  if (match) return { title: `${g.os} · ${match.name}`, subtitle: `schematic ${short}` }
  return { title: `${g.os} · ${short}` }
}
