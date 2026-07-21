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
  it('renders the dashboard heading and stat tiles', async () => {
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
})
