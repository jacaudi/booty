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
}

export interface ScanResult {
  scanned: number
  updated: number
  orphans: number
}

export async function listCache(): Promise<CacheEntry[]> {
  const body = await request<{ entries: CacheEntry[] }>('/cache')
  return body?.entries ?? []
}

export function pinCache(id: number): Promise<unknown> {
  return request(`/cache/${id}/pin`, { method: 'POST' })
}

export function unpinCache(id: number): Promise<unknown> {
  return request(`/cache/${id}/unpin`, { method: 'POST' })
}

export function scanCache(): Promise<ScanResult | undefined> {
  return request<ScanResult>('/cache/scan', { method: 'POST' })
}
