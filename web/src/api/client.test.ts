import { afterEach, describe, expect, it, vi } from 'vitest'
import { approveHost, bindSchematic, listHosts, login, logout, onUnauthorized, request, revokeHost, setMenuMode } from './client'

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
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/hosts', expect.objectContaining({ credentials: 'same-origin' }))
  })

  it('approveHost POSTs the mac path with colons intact (no double-encoding)', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await approveHost('52:54:00:00:50:50')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/hosts/52:54:00:00:50:50/approve',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('revokeHost and setMenuMode POST their endpoints', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await revokeHost('aa:bb')
    await setMenuMode('aa:bb')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/hosts/aa:bb/revoke', expect.objectContaining({ method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/hosts/aa:bb/menu', expect.objectContaining({ method: 'POST' }))
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

  it('AboutView-style info reads go through request(), so their 401 reaches the gate', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    const handler = vi.fn()
    onUnauthorized(handler)

    // request('/info') is exactly what AboutView calls.
    await expect(request('/info')).rejects.toThrow()
    expect(handler).toHaveBeenCalledOnce()
  })
})

describe('auth in the request layer', () => {
  it('sends credentials so the session cookie rides along', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ hosts: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await listHosts()
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/hosts',
      expect.objectContaining({ credentials: 'same-origin' }),
    )
  })

  it('fires the unauthorized handler on a 401 instead of throwing a raw error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    const handler = vi.fn()
    onUnauthorized(handler)

    await expect(listHosts()).rejects.toThrow()
    expect(handler).toHaveBeenCalledOnce()
  })

  it('does not fire the unauthorized handler on other failures', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('boom', { status: 500 })))
    const handler = vi.fn()
    onUnauthorized(handler)

    await expect(listHosts()).rejects.toThrow(/failed: 500/)
    expect(handler).not.toHaveBeenCalled()
  })

  it('login POSTs the token to /login outside the /api/v1 prefix', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await login('secret-token')
    expect(fetchMock).toHaveBeenCalledWith('/login', expect.objectContaining({
      method: 'POST',
      credentials: 'same-origin',
      body: JSON.stringify({ token: 'secret-token' }),
    }))
  })

  it('login rejects on a bad token', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    await expect(login('wrong')).rejects.toThrow()
  })

  it('logout POSTs /logout', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await logout()
    expect(fetchMock).toHaveBeenCalledWith('/logout', expect.objectContaining({ method: 'POST' }))
  })

  it('reports a 401 exactly once per failed call, so the gate does not thrash', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    const handler = vi.fn()
    onUnauthorized(handler)

    await expect(listHosts()).rejects.toThrow()
    await expect(listHosts()).rejects.toThrow()
    expect(handler).toHaveBeenCalledTimes(2)
  })
})
