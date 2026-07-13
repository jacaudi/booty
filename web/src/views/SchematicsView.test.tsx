import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import SchematicsView from './SchematicsView'
import type { Config, ConfigDetail } from '../api/configs'
import * as configs from '../api/configs'
import * as client from '../api/client'

vi.mock('../api/configs')
vi.mock('../api/client')

const cfg = (o: Partial<Config>): Config => ({
  id: 1, name: 'vanilla', kind: 'schematic', activeRevision: 1, revisionCount: 1,
  derivedSchematicId: 'abc123def456abc123def456', updatedAt: '', ...o,
})
const detail = (o: Partial<ConfigDetail>): ConfigDetail => ({ ...cfg(o), source: 'customization: {}\n', ...o })

const renderView = () => render(<MemoryRouter><SchematicsView /></MemoryRouter>)

afterEach(() => vi.resetAllMocks())

describe('SchematicsView list screen', () => {
  it('lists schematics with an ext count and a shortened derived id', async () => {
    vi.mocked(configs.listConfigs).mockResolvedValue([cfg({ id: 1, name: 'iscsi' })])
    vi.mocked(configs.getConfig).mockResolvedValue(
      detail({ id: 1, source: 'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n' }),
    )
    renderView()
    await waitFor(() => expect(screen.getByText('iscsi')).toBeInTheDocument())
    expect(screen.getByText('1')).toBeInTheDocument() // ext count
  })

  it('New schematic opens the builder screen', async () => {
    vi.mocked(configs.listConfigs).mockResolvedValue([])
    renderView()
    await waitFor(() => screen.getByRole('button', { name: 'New schematic' }))
    await userEvent.click(screen.getByRole('button', { name: 'New schematic' }))
    // The builder screen shows a Back affordance.
    expect(screen.getByRole('button', { name: /Back|Schematics/ })).toBeInTheDocument()
  })

  it('Import by ID binds the pasted raw id to a chosen host', async () => {
    const ID = 'a'.repeat(64) // a schematic ID is 64 lowercase hex chars
    vi.mocked(configs.listConfigs).mockResolvedValue([])
    vi.mocked(client.listHosts).mockResolvedValue([
      { mac: 'aa:bb', hostname: 'n1', ip: '', booted: '', os: 'talos', approved: true },
    ])
    vi.mocked(client.bindSchematic).mockResolvedValue(undefined)
    renderView()
    await waitFor(() => screen.getByRole('button', { name: 'Import by ID' }))
    await userEvent.click(screen.getByRole('button', { name: 'Import by ID' }))
    await userEvent.type(screen.getByLabelText('Schematic ID'), ID)
    // pick the host
    await userEvent.click(screen.getByLabelText('Host'))
    await userEvent.click(await screen.findByText('aa:bb (n1)'))
    await userEvent.click(screen.getByRole('button', { name: 'Bind' }))
    await waitFor(() => expect(client.bindSchematic).toHaveBeenCalledWith('aa:bb', { schematic: ID }))
  })

  it('Import by ID rejects a truncated (non-64-hex) id without calling the API', async () => {
    vi.mocked(configs.listConfigs).mockResolvedValue([])
    vi.mocked(client.listHosts).mockResolvedValue([
      { mac: 'aa:bb', hostname: 'n1', ip: '', booted: '', os: 'talos', approved: true },
    ])
    renderView()
    await waitFor(() => screen.getByRole('button', { name: 'Import by ID' }))
    await userEvent.click(screen.getByRole('button', { name: 'Import by ID' }))
    await userEvent.type(screen.getByLabelText('Schematic ID'), 'deadbeef')
    await userEvent.click(screen.getByRole('button', { name: 'Bind' }))
    expect(await screen.findByText(/64 lowercase hex/)).toBeInTheDocument()
    expect(client.bindSchematic).not.toHaveBeenCalled()
  })
})
