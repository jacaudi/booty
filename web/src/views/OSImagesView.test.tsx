import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import OSImagesView from './OSImagesView'
import * as cacheApi from '../api/cache'
import * as configsApi from '../api/configs'

// SchematicsView (mounted via the Schematics tab) imports bindSchematic/listHosts
// from '../api/client'; mock the module so an unrelated real network call can't
// leak into these tests. Neither export is exercised here (both only fire from
// the Import modal), so no identifier from the module is otherwise needed.
vi.mock('../api/client')
vi.mock('../api/cache')
vi.mock('../api/configs')
vi.mock('../api/client')

beforeEach(() => {
  vi.mocked(cacheApi.listCache).mockResolvedValue([])
  vi.mocked(configsApi.listConfigs).mockResolvedValue([])
})

afterEach(() => vi.resetAllMocks())

describe('OSImagesView', () => {
  it('holds the Cached versions and Schematics tabs under one OS Images page', async () => {
    render(<MemoryRouter><OSImagesView /></MemoryRouter>)
    expect(screen.getByRole('heading', { name: 'OS Images' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Cached versions' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Schematics' })).toBeInTheDocument()
    // The cache tab is the default: its Scan affordance is present without a click.
    expect(await screen.findByRole('button', { name: /scan now/i })).toBeInTheDocument()
  })

  it('the Schematics tab renders SchematicsView', async () => {
    render(<MemoryRouter><OSImagesView /></MemoryRouter>)
    await userEvent.click(screen.getByRole('tab', { name: 'Schematics' }))
    expect(await screen.findByRole('button', { name: 'New schematic' })).toBeInTheDocument()
  })
})
