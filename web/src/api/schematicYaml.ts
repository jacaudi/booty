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
  // `customization` and `overlay` are SIBLINGS in the Factory schematic schema —
  // overlay is NOT a child of customization (it was, wrongly, until this fix).
  const cust = ['customization:']
  if (f.extensions.length > 0) {
    cust.push('  systemExtensions:', '    officialExtensions:')
    for (const e of f.extensions) cust.push(`      - ${e}`)
  }
  const lines = cust.length === 1 ? ['customization: {}'] : cust
  if (f.overlayName && f.overlayImage) {
    lines.push('overlay:', `  name: ${f.overlayName}`, `  image: ${f.overlayImage}`)
  }
  return lines.join('\n') + '\n'
}

export function parseCustomization(source: string): SchematicFields | null {
  const extensions = [...source.matchAll(/^ {6}- (.+)$/gm)].map((m) => m[1].trim())
  const overlayName = source.match(/^ {2}name: (.+)$/m)?.[1]?.trim()
  const overlayImage = source.match(/^ {2}image: (.+)$/m)?.[1]?.trim()
  const fields: SchematicFields = { extensions, overlayName, overlayImage }
  // Round-trip guard: anything our builder would not emit byte-identically is
  // outside the subset (hand-edited, unknown keys, or the OLD nested overlay
  // shape) -> null -> the edit UI shows the raw source read-only.
  return buildCustomization(fields) === source ? fields : null
}
