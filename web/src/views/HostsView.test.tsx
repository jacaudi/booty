import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Host } from '../api/types'
import HostsView from './HostsView'
import * as client from '../api/client'
import * as configsApi from '../api/configs'
import * as rolesApi from '../api/roles'

vi.mock('../api/client')
vi.mock('../api/configs')
vi.mock('../api/roles')

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

  it('Allow submits the extended approve', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([{ mac: 'aa:bb', hostname: '', ip: '', os: 'flatcar', booted: '', approved: false } as Host])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(client.approveHostWith).mockResolvedValue(undefined)
    render(<HostsView />)
    await waitFor(() => screen.getByText('aa:bb'))
    await userEvent.click(screen.getByRole('button', { name: /allow/i }))
    await userEvent.click(await screen.findByRole('button', { name: /^ok$|allow/i }))
    expect(client.approveHostWith).toHaveBeenCalledWith('aa:bb', expect.any(Object))
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
