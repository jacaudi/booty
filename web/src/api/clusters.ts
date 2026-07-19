import { request } from './client'

export interface Member {
  mac: string
  hostname: string
  machineType: string
  schematic?: string
  status: string
}

export interface Cluster {
  id: number
  name: string
  endpoint: string
  talosVersion: string
  k8sVersion: string
  specConfigId?: number
  members: Member[]
  updatedAt: string
}

export async function listClusters(): Promise<Cluster[]> {
  const body = await request<{ clusters: Cluster[] }>('/clusters')
  return body?.clusters ?? []
}

export function getCluster(id: number): Promise<Cluster | undefined> {
  return request<Cluster>(`/clusters/${id}`)
}

export function createCluster(input: { name: string; endpoint: string; talosVersion: string; k8sVersion: string }): Promise<Cluster | undefined> {
  return request<Cluster>('/clusters', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

export function addMember(id: number, input: { mac: string; machineType: string; schematicId?: number; schematic?: string; patch?: string }): Promise<Cluster | undefined> {
  return request<Cluster>(`/clusters/${id}/members`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

export function removeMember(id: number, mac: string): Promise<unknown> {
  return request(`/clusters/${id}/members/${mac}`, { method: 'DELETE' })
}

export function importCluster(input: { name: string; controlPlanes: { mac: string; controlplane: string }[] }): Promise<Cluster | undefined> {
  return request<Cluster>('/clusters/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

// specConfigId binds a kind=taloscluster config as the cluster's spec (validated
// server-side, api_clusters.go:207-216). OMITTING it PRESERVES the existing
// binding (api_clusters.go:198-206) — and the server cannot clear one at all, so
// there is deliberately no way to express "unbind" here. Only PUT accepts this
// field; POST /clusters does not.
export function updateCluster(
  id: number,
  input: { endpoint: string; talosVersion: string; k8sVersion: string; specConfigId?: number },
): Promise<Cluster | undefined> {
  return request<Cluster>(`/clusters/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export function exportClusterSecrets(id: number): Promise<{ secretsYaml: string } | undefined> {
  return request<{ secretsYaml: string }>(`/clusters/${id}/export`, { method: 'POST' })
}
