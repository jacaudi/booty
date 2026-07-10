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

  it('Allow on a talos host binds the schematic BEFORE approving', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([
      { mac: 'ta:lo', hostname: '', ip: '', os: 'talos', booted: '', approved: false } as Host,
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 4, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1, derivedSchematicId: 'a1b2c3d4', updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    const order: string[] = []
    vi.mocked(client.bindSchematic).mockImplementation(async () => { order.push('bind') })
    vi.mocked(client.approveHostWith).mockImplementation(async () => { order.push('approve') })
    render(<HostsView />)
    await waitFor(() => screen.getByText('ta:lo'))
    await userEvent.click(screen.getByRole('button', { name: /allow/i }))
    const schematicInput = await screen.findByLabelText(/talos schematic/i)
    await userEvent.type(schematicInput, 'iscsi')
    await userEvent.click(await screen.findByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(client.bindSchematic).toHaveBeenCalledWith('ta:lo', { configId: 4 }))
    expect(order).toEqual(['bind', 'approve'])
  })

  it('Allow on a non-talos host shows no schematic field', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([
      { mac: 'fl:at', hostname: '', ip: '', os: 'flatcar', booted: '', approved: false } as Host,
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<HostsView />)
    await waitFor(() => screen.getByText('fl:at'))
    await userEvent.click(screen.getByRole('button', { name: /allow/i }))
    await screen.findByLabelText(/config/i)
    expect(screen.queryByLabelText(/talos schematic/i)).not.toBeInTheDocument()
  })
})
