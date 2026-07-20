import { request } from './client'

export interface Config {
  id: number
  name: string
  kind: 'butane' | 'machineconfig' | 'schematic' | 'taloscluster' | 'debianconfig'
  activeRevision: number
  revisionCount: number
  derivedSchematicId?: string
  updatedAt: string
}

export interface ConfigDetail extends Config {
  source: string
}

export interface Revision {
  revision: number
  sha256: string
  createdAt: string
  active: boolean
}

export interface Preview {
  rendered: string
  contentType: string
  report: string
}

export async function listConfigs(): Promise<Config[]> {
  const body = await request<{ configs: Config[] }>('/configs')
  return body?.configs ?? []
}

export function getConfig(id: number): Promise<ConfigDetail | undefined> {
  return request<ConfigDetail>(`/configs/${id}`)
}

export function createConfig(input: { name: string; kind: string; source: string }): Promise<Config | undefined> {
  return request<Config>('/configs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

export function updateConfig(id: number, source: string): Promise<Config | undefined> {
  return request<Config>(`/configs/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source }) })
}

export function previewConfig(id: number, mac?: string): Promise<Preview | undefined> {
  return request<Preview>(`/configs/${id}/preview`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(mac ? { mac } : {}) })
}

export async function listRevisions(id: number): Promise<Revision[]> {
  const body = await request<{ revisions: Revision[] }>(`/configs/${id}/revisions`)
  return body?.revisions ?? []
}

export function rollbackConfig(id: number, revision: number): Promise<Config | undefined> {
  return request<Config>(`/configs/${id}/rollback`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision }) })
}
