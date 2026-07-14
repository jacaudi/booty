import { request } from './client'

export interface CacheEntry {
  id: number
  os: string
  arch: string
  params?: Record<string, string>
  version: string
  size: number
  state: 'in-cycle' | 'in-cycle-pinned' | 'archived' | 'archived-pinned'
  pinned: boolean
  inWindow: boolean
  fetchedAt: string
  verified?: boolean | null
  verifyErr?: string
}

export interface ScanResult {
  scanned: number
  updated: number
  orphans: number
}

export interface CacheFilter {
  os?: string
  state?: 'in-cycle' | 'archived'
  pinned?: boolean
}

export async function listCache(filter?: CacheFilter): Promise<CacheEntry[]> {
  const q = new URLSearchParams()
  if (filter?.os) q.set('os', filter.os)
  if (filter?.state) q.set('state', filter.state)
  if (filter?.pinned !== undefined) q.set('pinned', String(filter.pinned))
  const qs = q.toString()
  const body = await request<{ entries: CacheEntry[] }>(qs ? `/cache?${qs}` : '/cache')
  return body?.entries ?? []
}

export function pinCache(id: number): Promise<unknown> {
  return request(`/cache/${id}/pin`, { method: 'POST' })
}

export function unpinCache(id: number): Promise<unknown> {
  return request(`/cache/${id}/unpin`, { method: 'POST' })
}

export function reverifyCacheEntry(id: number): Promise<unknown> {
  return request(`/cache/${id}/reverify`, { method: 'POST' })
}

export function scanCache(): Promise<ScanResult | undefined> {
  return request<ScanResult>('/cache/scan', { method: 'POST' })
}
