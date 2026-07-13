import { afterEach, describe, expect, it, vi } from 'vitest'
import { approveHost, bindSchematic, listHosts, revokeHost, setMenuMode } from './client'

afterEach(() => vi.restoreAllMocks())

describe('api client', () => {
  it('listHosts GETs /api/v1/hosts and unwraps the hosts array', async () => {
    const hosts = [{ mac: 'aa:bb', hostname: 'h1', ip: '1.2.3.4', booted: '' }]
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ hosts }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(listHosts()).resolves.toEqual(hosts)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/hosts', undefined)
  })

  it('approveHost POSTs the mac path with colons intact (no double-encoding)', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await approveHost('52:54:00:00:50:50')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/hosts/52:54:00:00:50:50/approve', {
      method: 'POST',
    })
  })

  it('revokeHost and setMenuMode POST their endpoints', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await revokeHost('aa:bb')
    await setMenuMode('aa:bb')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/hosts/aa:bb/revoke', { method: 'POST' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/hosts/aa:bb/menu', { method: 'POST' })
  })

  it('throws on a non-ok response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 500 })))
    await expect(listHosts()).rejects.toThrow(/failed: 500/)
  })

  it('bindSchematic POSTs to /hosts/{mac}/schematic', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, text: () => Promise.resolve('{}') })
    vi.stubGlobal('fetch', fetchMock)
    await bindSchematic('aa:bb', { configId: 3 })
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/hosts/aa:bb/schematic',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ configId: 3 }) }),
    )
  })

  it('includes the response body in the thrown error (for Validate 422s)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ detail: 'butane: line 3: unknown key' }), { status: 422 })),
    )
    await expect(listHosts()).rejects.toThrow(/unknown key/)
  })

  it('still reports the status when the body is empty', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 500 })))
    await expect(listHosts()).rejects.toThrow(/failed: 500/)
  })
})
