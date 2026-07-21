import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import HomeView from './HomeView'
import * as client from '../api/client'
import * as cache from '../api/cache'
import * as configs from '../api/configs'
import * as clusters from '../api/clusters'
import * as health from '../api/health'

vi.mock('../api/client'); vi.mock('../api/cache'); vi.mock('../api/configs'); vi.mock('../api/clusters'); vi.mock('../api/health')
afterEach(() => vi.resetAllMocks())

const seedEmpty = () => {
  vi.mocked(client.listHosts).mockResolvedValue([])
  vi.mocked(cache.listCache).mockResolvedValue([])
  vi.mocked(configs.listConfigs).mockResolvedValue([])
  vi.mocked(clusters.listClusters).mockResolvedValue([])
  vi.mocked(health.checkHealth).mockResolvedValue({ ok: true })
}

describe('HomeView', () => {
  beforeEach(seedEmpty)

  it('renders the dashboard heading and stat tiles when data exists', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([{ mac: 'a', hostname: 'h', ip: '', approved: true }] as never)
    render(<MemoryRouter><HomeView /></MemoryRouter>)
    expect(screen.getByRole('heading', { name: /booty/i })).toBeInTheDocument()
    expect(await screen.findByText('Hosts')).toBeInTheDocument()
  })

  it('shows a getting-started card on a fresh install (all zero)', async () => {
    render(<MemoryRouter><HomeView /></MemoryRouter>)
    expect(await screen.findByText(/get started/i)).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /approve your first host/i })).toHaveAttribute('href', '/hosts')
  })

  it('surfaces a pending host (approved omitted) in the Needs attention panel', async () => {
    // Real API contract: an unapproved host omits `approved`. This is the
    // end-to-end case the mocked-`approved:false` tests could not catch.
    vi.mocked(client.listHosts).mockResolvedValue([{ mac: 'aa', hostname: 'unbooted', ip: '' }] as never)
    render(<MemoryRouter><HomeView /></MemoryRouter>)
    expect(await screen.findByText(/needs attention/i)).toBeInTheDocument()
    expect(await screen.findByText('unbooted')).toBeInTheDocument()
    // exact name: QuickActions also has an "Approve hosts" button
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeInTheDocument()
  })
})
