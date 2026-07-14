import { describe, expect, it } from 'vitest'
import { navEntries } from './nav'

describe('nav', () => {
  it('orders Home | Hosts | OS Images | Boot Configs | Clusters | About', () => {
    expect(navEntries.map((e) => e.label)).toEqual([
      'Home', 'Hosts', 'OS Images', 'Boot Configs', 'Clusters', 'About',
    ])
  })

  it('serves OS Images at /images (the old /cache route is gone)', () => {
    expect(navEntries.map((e) => e.path)).toContain('/images')
    expect(navEntries.map((e) => e.path)).not.toContain('/cache')
  })
})
