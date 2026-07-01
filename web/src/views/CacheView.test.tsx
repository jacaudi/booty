import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { CacheEntry } from '../api/cache'
import CacheView from './CacheView'
import * as api from '../api/cache'

vi.mock('../api/cache')

const entry = (o: Partial<CacheEntry>): CacheEntry => ({
  id: 1, os: 'talos', arch: 'amd64', version: 'v1', size: 1024,
  state: 'in-cycle', pinned: false, inWindow: true, fetchedAt: '', ...o,
})

afterEach(() => vi.resetAllMocks())

describe('CacheView', () => {
  it('renders cache rows with state', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, version: 'v1', state: 'in-cycle' }),
      entry({ id: 2, version: 'v0', state: 'archived', inWindow: false }),
    ])
    render(<CacheView />)
    await waitFor(() => expect(screen.getByText('v1')).toBeInTheDocument())
    expect(screen.getByText('v0')).toBeInTheDocument()
    expect(screen.getByText('archived')).toBeInTheDocument()
  })

  it('pin calls pinCache then reloads', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 5, pinned: false })])
    vi.mocked(api.pinCache).mockResolvedValue(undefined)
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1'))
    await userEvent.click(screen.getByRole('button', { name: 'Pin' }))
    expect(api.pinCache).toHaveBeenCalledWith(5)
  })

  it('scan calls scanCache', async () => {
    vi.mocked(api.listCache).mockResolvedValue([])
    vi.mocked(api.scanCache).mockResolvedValue({ scanned: 0, updated: 0, orphans: 0 })
    render(<CacheView />)
    await userEvent.click(screen.getByRole('button', { name: 'Scan' }))
    expect(api.scanCache).toHaveBeenCalled()
  })
})
