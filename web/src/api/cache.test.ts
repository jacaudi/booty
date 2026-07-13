import { afterEach, describe, expect, it, vi } from 'vitest'
import { listCache, pinCache, reverifyCacheEntry, scanCache } from './cache'

afterEach(() => vi.restoreAllMocks())

describe('cache api client', () => {
  it('listCache GETs /api/v1/cache and unwraps entries', async () => {
    const entries = [{ id: 1, os: 'talos', arch: 'amd64', version: 'v1', size: 10, state: 'in-cycle', pinned: false, inWindow: true, fetchedAt: '' }]
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ entries }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listCache()).resolves.toEqual(entries)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache', undefined)
  })

  it('listCache with no filter GETs bare /cache', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ entries: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await listCache()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache', undefined)
  })

  it('listCache maps os/state/pinned to the query string', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ entries: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await listCache({ os: 'talos', state: 'archived', pinned: true })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache?os=talos&state=archived&pinned=true', undefined)
  })

  it('listCache omits empty filter fields', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ entries: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await listCache({ os: 'talos' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache?os=talos', undefined)
  })

  it('pinCache POSTs the id path', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await pinCache(7)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache/7/pin', { method: 'POST' })
  })

  it('reverifyCacheEntry POSTs to the reverify path', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await reverifyCacheEntry(7)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache/7/reverify', { method: 'POST' })
  })

  it('scanCache POSTs /cache/scan and returns the summary', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ scanned: 3, updated: 3, orphans: 1 }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(scanCache()).resolves.toEqual({ scanned: 3, updated: 3, orphans: 1 })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/cache/scan', { method: 'POST' })
  })
})
