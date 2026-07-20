import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Config } from './configs'
import { createConfig, listConfigs, previewConfig, rollbackConfig, updateConfig } from './configs'

afterEach(() => vi.restoreAllMocks())

describe('configs api client', () => {
  it('listConfigs GETs /api/v1/configs and unwraps', async () => {
    const configs = [{ id: 1, name: 'p', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' }]
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ configs }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listConfigs()).resolves.toEqual(configs)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/configs', undefined)
  })

  it('createConfig POSTs name/kind/source', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 1, name: 'p', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' }), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await createConfig({ name: 'p', kind: 'butane', source: 's' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/configs', expect.objectContaining({ method: 'POST' }))
  })

  it('updateConfig PUTs the source', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await updateConfig(3, 'new-source')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/configs/3', expect.objectContaining({ method: 'PUT' }))
  })

  it('previewConfig POSTs mac (optional)', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ rendered: 'x', contentType: 'text/plain', report: '' }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await previewConfig(2, 'aa:bb:cc:dd:ee:ff')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/configs/2/preview', expect.objectContaining({ method: 'POST' }))
  })

  it('rollbackConfig POSTs the revision', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await rollbackConfig(2, 1)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/configs/2/rollback', expect.objectContaining({ method: 'POST' }))
  })

  it('accepts all five server kinds on the Config type', () => {
    const kinds: Config['kind'][] = ['butane', 'machineconfig', 'schematic', 'taloscluster', 'debianconfig']
    expect(kinds).toHaveLength(5)
  })
})
