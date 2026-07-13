import { afterEach, describe, expect, it, vi } from 'vitest'
import { addMember, createCluster, exportClusterSecrets, importCluster, listClusters, removeMember, updateCluster } from './clusters'

afterEach(() => vi.restoreAllMocks())

describe('clusters api client', () => {
  it('listClusters GETs /api/v1/clusters and unwraps', async () => {
    const clusters = [{ id: 1, name: 'p', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' }]
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ clusters }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listClusters()).resolves.toEqual(clusters)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters', undefined)
  })

  it('createCluster POSTs the pinned inputs', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 1 }), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await createCluster({ name: 'p', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters', expect.objectContaining({ method: 'POST' }))
  })

  it('addMember POSTs to /clusters/{id}/members', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 1 }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await addMember(1, { mac: 'aa:bb', machineType: 'worker' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters/1/members', expect.objectContaining({ method: 'POST', body: JSON.stringify({ mac: 'aa:bb', machineType: 'worker' }) }))
  })

  it('removeMember DELETEs /clusters/{id}/members/{mac}', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await removeMember(1, 'aa:bb')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters/1/members/aa:bb', expect.objectContaining({ method: 'DELETE' }))
  })

  it('importCluster POSTs /clusters/import', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 1 }), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await importCluster({ name: 'a', controlplane: 'yaml', controlplaneMac: 'aa:bb' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters/import', expect.objectContaining({ method: 'POST' }))
  })

  it('exportClusterSecrets POSTs /clusters/{id}/export and unwraps', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ secretsYaml: 'apiVersion: v1alpha1' }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(exportClusterSecrets(3)).resolves.toEqual({ secretsYaml: 'apiVersion: v1alpha1' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/clusters/3/export', expect.objectContaining({ method: 'POST' }))
  })

  it('updateCluster PUTs the pinned inputs', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 3 }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await updateCluster(3, { endpoint: 'https://e:6443', talosVersion: 'v1.13.6', k8sVersion: 'v1.34.0' })
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/clusters/3',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ endpoint: 'https://e:6443', talosVersion: 'v1.13.6', k8sVersion: 'v1.34.0' }) }),
    )
  })
})
