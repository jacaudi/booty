// How a Factory-derived schematic ID (64 lowercase hex) is abbreviated for
// display. Single-sourced because the Schematics list and the OS Images cache
// group labels must abbreviate it IDENTICALLY — cross-referencing the two tabs
// by eye is the whole point of grouping the cache by schematic.
export function shortSchematicId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 6)}…${id.slice(-4)}` : id
}
