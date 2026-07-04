import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRole, listRoles } from './roles'

afterEach(() => vi.restoreAllMocks())

describe('roles api client', () => {
  it('listRoles GETs /api/v1/roles and unwraps', async () => {
    const roles = [{ id: 1, name: 'cp', hostCount: 0 }]
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ roles }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listRoles()).resolves.toEqual(roles)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/roles', undefined)
  })

  it('createRole POSTs name + optional defaultConfigId', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 1, name: 'cp', hostCount: 0 }), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    await createRole({ name: 'cp' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/roles', expect.objectContaining({ method: 'POST' }))
  })
})
