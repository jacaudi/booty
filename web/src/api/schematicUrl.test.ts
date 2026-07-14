import { describe, expect, it } from 'vitest'
import { parseBuilderParams, serializeBuilderParams } from './schematicUrl'

describe('schematicUrl', () => {
  it('parses hw/arch/version/ext from a query string', () => {
    const sp = new URLSearchParams('hw=metal&arch=amd64&version=v1.9.0&ext=siderolabs/iscsi-tools&ext=siderolabs/util-linux-tools')
    expect(parseBuilderParams(sp)).toEqual({
      hw: 'metal', arch: 'amd64', version: 'v1.9.0',
      ext: ['siderolabs/iscsi-tools', 'siderolabs/util-linux-tools'],
    })
  })

  it('defaults ext to [] when absent', () => {
    expect(parseBuilderParams(new URLSearchParams(''))).toEqual({ ext: [] })
  })

  it('serializes, omitting empty fields', () => {
    const sp = serializeBuilderParams({ hw: 'metal', arch: 'amd64', version: 'v1.9.0', ext: ['a/b'] })
    expect(sp.toString()).toBe('hw=metal&arch=amd64&version=v1.9.0&ext=a%2Fb')
  })

  it('round-trips through serialize -> parse', () => {
    const p = { hw: 'sbc', arch: 'arm64', version: 'v1.9.0', ext: ['a/b', 'c/d'] }
    expect(parseBuilderParams(serializeBuilderParams(p))).toEqual(p)
  })

  it('never emits kargs (D5) or secureboot (deferred, SGE B4)', () => {
    const sp = serializeBuilderParams({ ext: [], version: 'v1' })
    expect(sp.has('kargs')).toBe(false)
    expect(sp.has('secureboot')).toBe(false)
  })
})
