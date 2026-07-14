// Deep-link (de)serialization for the schematic builder. Params mirror
// factory.talos.dev: hw, arch, version, ext (repeatable).
//
// Deliberately absent:
//   - kargs: P5 design decision D5 — extraKernelArgs are Factory-ignored on
//     booty's boot/install paths.
//   - secureboot: Secure Boot is an image-PATH selection on the Factory
//     (installer-secureboot / metal-<arch>-secureboot.iso), not a schematic
//     customization field, and booty serves no secureboot asset variants.
//     Deferred — see issue #32.

export interface BuilderParams {
  hw?: string
  arch?: string
  version?: string
  ext: string[]
}

export function parseBuilderParams(sp: URLSearchParams): BuilderParams {
  const p: BuilderParams = { ext: sp.getAll('ext') }
  const hw = sp.get('hw')
  const arch = sp.get('arch')
  const version = sp.get('version')
  if (hw) p.hw = hw
  if (arch) p.arch = arch
  if (version) p.version = version
  return p
}

export function serializeBuilderParams(p: BuilderParams): URLSearchParams {
  const sp = new URLSearchParams()
  if (p.hw) sp.set('hw', p.hw)
  if (p.arch) sp.set('arch', p.arch)
  if (p.version) sp.set('version', p.version)
  for (const e of p.ext) sp.append('ext', e)
  return sp
}
