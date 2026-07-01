import type { Host } from './types'

const BASE = '/api/v1'

export async function request<T>(path: string, init?: RequestInit): Promise<T | undefined> {
  const res = await fetch(`${BASE}${path}`, init)
  if (!res.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} failed: ${res.status}`)
  }
  if (res.status === 204) return undefined
  const text = await res.text()
  return text ? (JSON.parse(text) as T) : undefined
}

export async function listHosts(): Promise<Host[]> {
  const body = await request<{ hosts: Host[] }>('/hosts')
  return body?.hosts ?? []
}

// MAC is inserted raw: colons are legal path chars and MACs contain no slashes,
// so the single path segment round-trips without encodeURIComponent.
export function approveHost(mac: string): Promise<unknown> {
  return request(`/hosts/${mac}/approve`, { method: 'POST' })
}

export function revokeHost(mac: string): Promise<unknown> {
  return request(`/hosts/${mac}/revoke`, { method: 'POST' })
}

export function setMenuMode(mac: string): Promise<unknown> {
  return request(`/hosts/${mac}/menu`, { method: 'POST' })
}
