export type Health = { ok: boolean; version?: string }

// /healthz lives on the base mux, outside the /api/v1 surface, so this bypasses
// the request() client (which prefixes /api/v1) and hits the path directly.
// Returns liveness + the build version (design §11: the System card shows both).
export async function checkHealth(): Promise<Health> {
  try {
    const res = await fetch('/healthz')
    if (!res.ok) return { ok: false }
    const body = (await res.json()) as { version?: string }
    return { ok: true, version: body.version }
  } catch {
    return { ok: false }
  }
}
