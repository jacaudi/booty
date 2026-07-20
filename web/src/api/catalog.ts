import { request } from './client'

export type Family = { name: string; configKind: string; authoringKinds: string[] }
export type OS = { name: string; family: string; requiredParams: string[] }
export type FamilyKinds = { bootConfigKinds: string[]; osFamily: Record<string, string[]> }

export async function listFamilies(): Promise<Family[]> {
  return (await request<{ families: Family[] }>('/families'))?.families ?? []
}

export async function listOS(): Promise<OS[]> {
  return (await request<{ os: OS[] }>('/os'))?.os ?? []
}

let cache: Promise<FamilyKinds> | undefined

export function loadFamilyKinds(): Promise<FamilyKinds> {
  if (!cache) {
    cache = (async () => {
      const [families, os] = await Promise.all([listFamilies(), listOS()])
      const byFamily = new Map(families.map((f) => [f.name, f.authoringKinds]))
      const bootConfigKinds = [...new Set(families.flatMap((f) => f.authoringKinds))]
      const osFamily: Record<string, string[]> = {}
      for (const o of os) osFamily[o.name] = byFamily.get(o.family) ?? []
      return { bootConfigKinds, osFamily }
    })()
  }
  return cache
}

// Test-only: clear the module cache between cases.
export function __resetCatalogCache(): void {
  cache = undefined
}
