import { request } from './client'

export interface Role {
  id: number
  name: string
  defaultConfigId?: number
  hostCount: number
}

export async function listRoles(): Promise<Role[]> {
  const body = await request<{ roles: Role[] }>('/roles')
  return body?.roles ?? []
}

export function createRole(input: { name: string; defaultConfigId?: number }): Promise<Role | undefined> {
  return request<Role>('/roles', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

export function updateRole(id: number, input: { name?: string; defaultConfigId?: number }): Promise<Role | undefined> {
  return request<Role>(`/roles/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}
