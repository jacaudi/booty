import { describe, expect, it } from 'vitest'
import { navEntries } from './nav'

describe('nav', () => {
  it('orders Home | Hosts | Boot Configs | Cache | Clusters | About', () => {
    expect(navEntries.map((e) => e.label)).toEqual(['Home', 'Hosts', 'Boot Configs', 'Cache', 'Clusters', 'About'])
  })
})
