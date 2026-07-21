import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import StatTiles from './StatTiles'
import * as client from '../../api/client'
import * as cache from '../../api/cache'
import * as configs from '../../api/configs'
import * as clusters from '../../api/clusters'

vi.mock('../../api/client'); vi.mock('../../api/cache'); vi.mock('../../api/configs'); vi.mock('../../api/clusters')
afterEach(() => vi.resetAllMocks())

beforeEach(() => {
  vi.mocked(client.listHosts).mockResolvedValue([
    { mac: 'a', hostname: 'h1', ip: '', approved: true },
    { mac: 'b', hostname: 'h2', ip: '', approved: false },
  ] as never)
  vi.mocked(cache.listCache).mockResolvedValue([{ size: 10 }, { size: 20 }, { size: 5 }] as never) // 3 → OS Images tile, distinct from Hosts=2
  vi.mocked(configs.listConfigs).mockResolvedValue([{ id: 1 }] as never)
  vi.mocked(clusters.listClusters).mockResolvedValue([] as never)
})

const renderTiles = () => render(<MemoryRouter><StatTiles /></MemoryRouter>)

describe('StatTiles', () => {
  it('shows host total and highlights pending approvals', async () => {
    renderTiles()
    expect(await screen.findByText('2')).toBeInTheDocument() // total hosts
    expect(await screen.findByText(/1 pending/i)).toBeInTheDocument()
  })
  it('renders an inline error for a failed panel without throwing', async () => {
    vi.mocked(clusters.listClusters).mockRejectedValue(new Error('boom'))
    renderTiles()
    expect(await screen.findByText(/couldn.t load/i)).toBeInTheDocument()
    expect(await screen.findByText('2')).toBeInTheDocument() // other tiles still render
  })
})
