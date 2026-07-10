import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import BootConfigsView from './BootConfigsView'
import * as configsApi from '../api/configs'
import * as rolesApi from '../api/roles'

vi.mock('../api/configs')
vi.mock('../api/roles')

afterEach(() => vi.resetAllMocks())

describe('BootConfigsView', () => {
  it('renders Configs and Roles tabs and lists configs', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 1, name: 'prod', kind: 'butane', activeRevision: 2, revisionCount: 2, updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    expect(screen.getByRole('tab', { name: /configs/i })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /roles/i })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('prod')).toBeInTheDocument())
  })

  it('rollback calls rollbackConfig then reloads', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 5, name: 'c', kind: 'preseed', activeRevision: 2, revisionCount: 2, updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.listRevisions).mockResolvedValue([
      { revision: 2, sha256: 'b', createdAt: '', active: true },
      { revision: 1, sha256: 'a', createdAt: '', active: false },
    ])
    vi.mocked(configsApi.rollbackConfig).mockResolvedValue(undefined as never)
    render(<BootConfigsView />)
    await waitFor(() => screen.getByText('c'))
    await userEvent.click(screen.getByRole('button', { name: /revisions/i }))
    await userEvent.click(await screen.findByRole('button', { name: /rollback/i }))
    expect(configsApi.rollbackConfig).toHaveBeenCalledWith(5, 1)
    await waitFor(() => expect(configsApi.listConfigs).toHaveBeenCalledTimes(2))
  })

  it('prefills the Edit modal with the config current source', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 7, name: 'edit-me', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.getConfig).mockResolvedValue({
      id: 7,
      name: 'edit-me',
      kind: 'butane',
      activeRevision: 1,
      revisionCount: 1,
      updatedAt: '',
      source: 'EXISTING SOURCE CONTENT',
    })
    render(<BootConfigsView />)
    await waitFor(() => screen.getByText('edit-me'))
    await userEvent.click(screen.getByRole('button', { name: /edit/i }))
    expect(await screen.findByDisplayValue('EXISTING SOURCE CONTENT')).toBeInTheDocument()
    expect(configsApi.getConfig).toHaveBeenCalledWith(7)
  })

  it('lists schematics with short ID and extension tags in the Schematics tab', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      {
        id: 3,
        name: 'iscsi',
        kind: 'schematic',
        activeRevision: 1,
        revisionCount: 1,
        derivedSchematicId: 'a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8',
        updatedAt: '',
      },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.getConfig).mockResolvedValue({
      id: 3,
      name: 'iscsi',
      kind: 'schematic',
      activeRevision: 1,
      revisionCount: 1,
      updatedAt: '',
      source:
        'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n',
    })
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    await waitFor(() => expect(screen.getByText('iscsi')).toBeInTheDocument())
    expect(screen.getByText('a1b2c3…a7b8')).toBeInTheDocument()
    expect(screen.getByText('siderolabs/iscsi-tools')).toBeInTheDocument()
  })

  it('Configs tab excludes schematic-kind entries', async () => {
    // Separate test (no tab switch): antd keeps visited tab panels mounted but
    // hidden, so a switch-back queryByText would false-fail on the hidden row.
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 1, name: 'prod', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
      { id: 3, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1, derivedSchematicId: 'a1b2c3d4', updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await waitFor(() => expect(screen.getByText('prod')).toBeInTheDocument())
    expect(screen.queryByText('iscsi')).not.toBeInTheDocument()
  })

  it('creates a schematic from form fields, composing the customization YAML', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.createConfig).mockResolvedValue({
      id: 9,
      name: 'gpu',
      kind: 'schematic',
      activeRevision: 1,
      revisionCount: 1,
      derivedSchematicId: 'e5f6a7b8',
      updatedAt: '',
    })
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    await userEvent.click(await screen.findByRole('button', { name: /create schematic/i }))
    await userEvent.type(screen.getByLabelText('Name'), 'gpu') // exact: /name/i would also match "Overlay name"
    await userEvent.type(screen.getByLabelText(/official extensions/i), 'siderolabs/nvidia-open-gpu-kernel-modules{enter}')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() =>
      expect(configsApi.createConfig).toHaveBeenCalledWith({
        name: 'gpu',
        kind: 'schematic',
        source:
          'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/nvidia-open-gpu-kernel-modules\n',
      }),
    )
  })

  it('rejects saving a schematic with only overlay name set (both-or-neither)', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    await userEvent.click(await screen.findByRole('button', { name: /create schematic/i }))
    await userEvent.type(screen.getByLabelText('Name'), 'sbc')
    await userEvent.type(screen.getByLabelText(/overlay name/i), 'rpi_generic')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    expect((await screen.findAllByText(/overlay requires both a name and an image/i)).length).toBeGreaterThan(0)
    expect(configsApi.createConfig).not.toHaveBeenCalled()
  })

  it('rejects saving a schematic with only overlay image set (both-or-neither)', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    await userEvent.click(await screen.findByRole('button', { name: /create schematic/i }))
    await userEvent.type(screen.getByLabelText('Name'), 'sbc')
    await userEvent.type(screen.getByLabelText(/overlay image/i), 'siderolabs/sbc-raspberrypi')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    expect((await screen.findAllByText(/overlay requires both a name and an image/i)).length).toBeGreaterThan(0)
    expect(configsApi.createConfig).not.toHaveBeenCalled()
  })

  it('creates a schematic with both overlay name and image set', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.createConfig).mockResolvedValue({
      id: 10,
      name: 'sbc',
      kind: 'schematic',
      activeRevision: 1,
      revisionCount: 1,
      derivedSchematicId: 'abcd',
      updatedAt: '',
    })
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    await userEvent.click(await screen.findByRole('button', { name: /create schematic/i }))
    await userEvent.type(screen.getByLabelText('Name'), 'sbc')
    await userEvent.type(screen.getByLabelText(/overlay name/i), 'rpi_generic')
    await userEvent.type(screen.getByLabelText(/overlay image/i), 'siderolabs/sbc-raspberrypi')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() =>
      expect(configsApi.createConfig).toHaveBeenCalledWith({
        name: 'sbc',
        kind: 'schematic',
        source: 'customization:\n  overlay:\n    name: rpi_generic\n    image: siderolabs/sbc-raspberrypi\n',
      }),
    )
  })
})
