import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import CacheHealth from './CacheHealth'
import * as cache from '../../api/cache'

vi.mock('../../api/cache')
afterEach(() => vi.resetAllMocks())
const renderPanel = () => render(<MemoryRouter><CacheHealth /></MemoryRouter>)

describe('CacheHealth', () => {
  it('surfaces failed-verification entries', async () => {
    vi.mocked(cache.listCache).mockResolvedValue([
      { id: 1, os: 'flatcar', version: '1', size: 1, verified: false } as never,
      { id: 2, os: 'talos', version: '2', size: 1, verified: true } as never,
    ])
    renderPanel()
    expect(await screen.findByText(/1 cached image failed verification/i)).toBeInTheDocument()
  })
  it('renders nothing when the cache is healthy', async () => {
    vi.mocked(cache.listCache).mockResolvedValue([{ id: 2, os: 'talos', version: '2', size: 1, verified: true } as never])
    const { container } = renderPanel()
    await waitFor(() => expect(cache.listCache).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })
})
