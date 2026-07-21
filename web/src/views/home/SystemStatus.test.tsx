import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import SystemStatus from './SystemStatus'
import * as health from '../../api/health'

vi.mock('../../api/health')
afterEach(() => vi.resetAllMocks())

describe('SystemStatus', () => {
  it('shows Healthy and the version when /healthz is ok', async () => {
    vi.mocked(health.checkHealth).mockResolvedValue({ ok: true, version: 'v9.9' })
    render(<SystemStatus />)
    expect(await screen.findByText(/healthy/i)).toBeInTheDocument()
    expect(await screen.findByText(/v9\.9/)).toBeInTheDocument()
  })
  it('shows Unreachable when /healthz fails', async () => {
    vi.mocked(health.checkHealth).mockResolvedValue({ ok: false })
    render(<SystemStatus />)
    expect(await screen.findByText(/unreachable/i)).toBeInTheDocument()
  })
})
