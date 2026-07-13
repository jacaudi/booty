import type { CacheEntry } from './cache'

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

export function channelOf(e: CacheEntry): string {
  return e.params?.channel ?? e.os
}

export function groupKey(e: CacheEntry): string {
  return `${e.os}/${channelOf(e)}`
}

export interface CacheGroup {
  key: string
  os: string
  channel: string
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

export function summarize(entries: CacheEntry[]): CacheSummary {
  const evictable = entries.filter((e) => e.state.startsWith('archived') && !e.pinned)
  return {
    usedBytes: entries.reduce((sum, e) => sum + e.size, 0),
    inCycle: entries.filter((e) => e.state.startsWith('in-cycle')).length,
    archived: entries.filter((e) => e.state.startsWith('archived')).length,
    pinned: entries.filter((e) => e.pinned).length,
    failed: entries.filter((e) => e.verified === false).length,
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
