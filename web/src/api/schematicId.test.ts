import { describe, expect, it } from 'vitest'
import { shortSchematicId } from './schematicId'

describe('shortSchematicId', () => {
  it('abbreviates a 64-hex Factory-derived id as first6…last4', () => {
    const id = `43fac7${'0'.repeat(54)}1367`
    expect(id).toHaveLength(64)
    expect(shortSchematicId(id)).toBe('43fac7…1367')
  })

  it('leaves an already-short id untouched', () => {
    expect(shortSchematicId('abc123')).toBe('abc123')
  })
})
