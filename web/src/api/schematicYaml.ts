// Single source of the schematic customization YAML booty authors (P5 design
// D5: extensions + overlays ONLY — extraKernelArgs/meta are Factory-ignored on
// booty's boot/install paths and deliberately not exposed). build and parse
// must round-trip; parse only understands this generated subset and returns
// null for anything else, so the edit form falls back to read-only raw source
// instead of destroying hand-authored YAML.

export interface SchematicFields {
  extensions: string[]
  overlayName?: string
  overlayImage?: string
}

export function buildCustomization(f: SchematicFields): string {
  const lines = ['customization:']
  if (f.extensions.length > 0) {
    lines.push('  systemExtensions:', '    officialExtensions:')
    for (const e of f.extensions) lines.push(`      - ${e}`)
  }
  if (f.overlayName && f.overlayImage) {
    lines.push('  overlay:', `    name: ${f.overlayName}`, `    image: ${f.overlayImage}`)
  }
  if (lines.length === 1) return 'customization: {}\n'
  return lines.join('\n') + '\n'
}

export function parseCustomization(source: string): SchematicFields | null {
  if (source.trim() === 'customization: {}') return { extensions: [] }
  const extensions = [...source.matchAll(/^ {6}- (.+)$/gm)].map((m) => m[1].trim())
  const overlayName = source.match(/^ {4}name: (.+)$/m)?.[1]?.trim()
  const overlayImage = source.match(/^ {4}image: (.+)$/m)?.[1]?.trim()
  const fields: SchematicFields = { extensions, overlayName, overlayImage }
  // Round-trip guard: anything our builder would not emit byte-identically is
  // outside the subset (hand-edited, unknown keys) -> null.
  return buildCustomization(fields) === source ? fields : null
}
