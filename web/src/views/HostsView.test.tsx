import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Host } from '../api/types'
import HostsView from './HostsView'
import * as client from '../api/client'

vi.mock('../api/client')

const host = (over: Partial<Host>): Host => ({
  mac: 'aa',
  hostname: 'h',
  ip: 'i',
  booted: '',
  ...over,
})

afterEach(() => vi.resetAllMocks())

describe('HostsView', () => {
  it('renders pending and approved hosts', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([
      host({ mac: 'pending-mac', approved: false }),
      host({ mac: 'approved-mac', approved: true, bootMode: 'assigned', assignedOS: 'talos' }),
    ])
    render(<HostsView />)
    await waitFor(() => expect(screen.getByText('pending-mac')).toBeInTheDocument())
    expect(screen.getByText('approved-mac')).toBeInTheDocument()
    expect(screen.getByText('talos')).toBeInTheDocument()
  })

  it('approve calls approveHost then reloads', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([host({ mac: 'p1', approved: false })])
    vi.mocked(client.approveHost).mockResolvedValue(undefined)
    render(<HostsView />)
    await waitFor(() => screen.getByText('p1'))
    await userEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(client.approveHost).toHaveBeenCalledWith('p1')
    await waitFor(() => expect(client.listHosts).toHaveBeenCalledTimes(2))
  })

  it('revoke calls revokeHost on an approved host', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([host({ mac: 'a1', approved: true })])
    vi.mocked(client.revokeHost).mockResolvedValue(undefined)
    render(<HostsView />)
    await waitFor(() => screen.getByText('a1'))
    await userEvent.click(screen.getByRole('button', { name: 'Revoke' }))
    expect(client.revokeHost).toHaveBeenCalledWith('a1')
  })

  it('shows an error alert when loading fails', async () => {
    vi.mocked(client.listHosts).mockRejectedValue(new Error('boom'))
    render(<HostsView />)
    await waitFor(() => expect(screen.getByText('boom')).toBeInTheDocument())
  })
})
