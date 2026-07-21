import { afterEach, describe, expect, it, vi } from 'vitest'
import { checkHealth } from './health'

afterEach(() => vi.restoreAllMocks())

describe('checkHealth', () => {
  it('returns ok + version on a 200 from /healthz', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"status":"ok","version":"v1.2.3"}', { status: 200 }))
    expect(await checkHealth()).toEqual({ ok: true, version: 'v1.2.3' })
  })
  it('returns ok:false on a non-ok response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('', { status: 503 }))
    expect(await checkHealth()).toEqual({ ok: false })
  })
  it('returns ok:false when the fetch rejects', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('network'))
    expect(await checkHealth()).toEqual({ ok: false })
  })
})
