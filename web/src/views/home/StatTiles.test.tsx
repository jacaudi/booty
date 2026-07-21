import type { ComponentProps } from 'react'
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import StatTiles from './StatTiles'

type Props = ComponentProps<typeof StatTiles>
const renderTiles = (props: Partial<Props>) =>
  render(<MemoryRouter><StatTiles hosts={[]} cache={[]} configs={[]} clusters={[]} {...props} /></MemoryRouter>)

describe('StatTiles', () => {
  it('counts a pending host when `approved` is OMITTED (real API omitzero contract)', () => {
    // The second host has no `approved` field at all — exactly how the API
    // serializes an unapproved host. `=== false` would miss it; `!approved` catches it.
    renderTiles({
      hosts: [
        { mac: 'a', hostname: 'h1', ip: '', approved: true },
        { mac: 'b', hostname: 'h2', ip: '' },
      ] as never,
    })
    expect(screen.getByText('2')).toBeInTheDocument() // total hosts
    expect(screen.getByText(/1 pending approval/i)).toBeInTheDocument()
  })

  it('shows "all approved" when every host is approved', () => {
    renderTiles({ hosts: [{ mac: 'a', hostname: 'h', ip: '', approved: true }] as never })
    expect(screen.getByText(/all approved/i)).toBeInTheDocument()
  })

  it('surfaces failed cache images on the OS Images tile', () => {
    renderTiles({
      cache: [
        { id: 1, size: 1024, verified: false },
        { id: 2, size: 1024, verified: true },
      ] as never,
    })
    expect(screen.getByText(/1 failed/i)).toBeInTheDocument()
  })

  it('names clusters and marks configs ready', () => {
    renderTiles({ configs: [{ id: 1 }] as never, clusters: [{ id: 1, name: 'prod' }] as never })
    expect(screen.getByText('prod')).toBeInTheDocument()
    expect(screen.getByText(/ready to assign/i)).toBeInTheDocument()
  })
})
