import { describe, expect, it } from 'vitest'
import { buildCustomization, parseCustomization } from './schematicYaml'

describe('schematicYaml', () => {
  it('builds the vanilla (empty) customization', () => {
    expect(buildCustomization({ extensions: [] })).toBe('customization: {}\n')
  })

  it('builds extensions with overlay at the TOP LEVEL (Factory schema)', () => {
    expect(
      buildCustomization({
        extensions: ['siderolabs/iscsi-tools', 'siderolabs/util-linux-tools'],
        overlayName: 'rpi_generic',
        overlayImage: 'siderolabs/sbc-raspberrypi',
      }),
    ).toBe(
      'customization:\n' +
        '  systemExtensions:\n' +
        '    officialExtensions:\n' +
        '      - siderolabs/iscsi-tools\n' +
        '      - siderolabs/util-linux-tools\n' +
        'overlay:\n' +
        '  name: rpi_generic\n' +
        '  image: siderolabs/sbc-raspberrypi\n',
    )
  })

  it('builds an overlay-only schematic (no extensions) with a valid empty customization', () => {
    expect(buildCustomization({ extensions: [], overlayName: 'rpi_generic', overlayImage: 'siderolabs/sbc-raspberrypi' })).toBe(
      'customization: {}\noverlay:\n  name: rpi_generic\n  image: siderolabs/sbc-raspberrypi\n',
    )
  })

  it('no longer round-trips the OLD nested overlay shape (falls back to read-only raw)', () => {
    const legacy = 'customization:\n  overlay:\n    name: rpi_generic\n    image: siderolabs/sbc-raspberrypi\n'
    expect(parseCustomization(legacy)).toBeNull()
  })

  it('round-trips build -> parse for every field combination', () => {
    for (const fields of [
      { extensions: [] },
      { extensions: ['siderolabs/iscsi-tools'] },
      { extensions: ['a/b', 'c/d'], overlayName: 'n', overlayImage: 'i' },
    ]) {
      expect(parseCustomization(buildCustomization(fields))).toEqual({
        extensions: fields.extensions,
        overlayName: fields.overlayName,
        overlayImage: fields.overlayImage,
      })
    }
  })

  it('returns null for source outside the generated subset', () => {
    expect(parseCustomization('customization:\n  extraKernelArgs:\n    - nomodeset\n')).toBeNull()
    expect(parseCustomization('not yaml at all')).toBeNull()
  })
})
