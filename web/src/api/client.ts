import type { Host } from './types'

const BASE = '/api/v1'

// unauthorizedHandler is set once by LoginGate. request() is the single choke
// point every view goes through, so a 401 anywhere surfaces the login prompt
// with no per-call token handling. AboutView is the one view that historically
// bypassed this with a raw fetch; it was moved onto request() in the same
// change that gated /info.
let unauthorizedHandler: (() => void) | undefined

export function onUnauthorized(handler: () => void): void {
  unauthorizedHandler = handler
}

export async function request<T>(path: string, init?: RequestInit): Promise<T | undefined> {
  // same-origin so the httpOnly session cookie rides along; the token itself
  // is never held in JS, localStorage, or sessionStorage.
  const res = await fetch(`${BASE}${path}`, { ...init, credentials: 'same-origin' })
  if (res.status === 401) {
    unauthorizedHandler?.()
    throw new Error(`${init?.method ?? 'GET'} ${path} failed: 401`)
  }
  if (!res.ok) {
    const body = (await res.text()).trim()
    const base = `${init?.method ?? 'GET'} ${path} failed: ${res.status}`
    throw new Error(body ? `${base}: ${body}` : base)
  }
  if (res.status === 204) return undefined
  const text = await res.text()
  return text ? (JSON.parse(text) as T) : undefined
}

// login lives on the base mux, OUTSIDE the /api/v1 prefix, so it bypasses
// request() the same way health.ts bypasses it for /healthz.
//
// There is no client-side logout() here: POST /logout is a real, tested
// server endpoint (pkg/http/auth_test.go), useful to scripts, but nothing in
// the UI currently offers a logout control -- the only prior caller of a
// client logout() was its own test. See docs/CONFIGURATION.md's
// Authentication section for the endpoint; a UI logout control is a
// follow-up, not implemented here.
export async function login(token: string): Promise<void> {
  const res = await fetch('/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (!res.ok) throw new Error(`login failed: ${res.status}`)
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

// approveHostWith is the atomic attach+allow used by the "Allow" modal (Task 12):
// it lets the operator bind a config/roles to a host in the same request that approves it.
export function approveHostWith(mac: string, body?: { configId?: number; roleIds?: number[] }): Promise<unknown> {
  return request(`/hosts/${mac}/approve`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })
}

export function bindHost(mac: string, body: { configId?: number; roleIds?: number[] }): Promise<unknown> {
  return request(`/hosts/${mac}/bind`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
}

// bindSchematic writes a Talos schematic onto a host (P5): configId picks a
// named schematic-kind config (its current built ID is bound server-side);
// schematic is the raw free-entry ID. Exactly one is required.
export function bindSchematic(mac: string, body: { configId?: number; schematic?: string }): Promise<unknown> {
  return request(`/hosts/${mac}/schematic`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
}

export function revokeHost(mac: string): Promise<unknown> {
  return request(`/hosts/${mac}/revoke`, { method: 'POST' })
}

export function setMenuMode(mac: string): Promise<unknown> {
  return request(`/hosts/${mac}/menu`, { method: 'POST' })
}
