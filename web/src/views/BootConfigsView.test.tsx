import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import BootConfigsView from './BootConfigsView'
import * as configsApi from '../api/configs'
import type { Config } from '../api/configs'
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

  it('Schematics tab delegates to SchematicsView', async () => {
    // Full schematic list/builder/import coverage now lives in
    // SchematicsView.test.tsx (extracted in Task 8) — this is a smoke test
    // that the tab actually wires up the extracted view. Wrapped in
    // MemoryRouter because SchematicsView's builder screen (Task 9) needs it.
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(
      <MemoryRouter>
        <BootConfigsView />
      </MemoryRouter>,
    )
    await userEvent.click(screen.getByRole('tab', { name: /schematics/i }))
    expect(await screen.findByRole('button', { name: 'New schematic' })).toBeInTheDocument()
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

  // The schematic create/edit form (Name, Official extensions, Overlay
  // name/image, both-or-neither validation) moved with the extraction and is
  // NOT reimplemented by the stub builder in this task — Task 9 rebuilds that
  // form and its coverage (including the both-or-neither rule) lands in
  // SchematicBuilder.test.tsx there.

  const cfgRow = (o: Partial<Config> = {}): Config => ({
    id: 1, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '', ...o,
  })

  it('Validate reports valid when preview returns rendered output', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([cfgRow()])
    vi.mocked(configsApi.previewConfig).mockResolvedValue({ rendered: 'variant: fcos', contentType: 'text/plain', report: '' })
    render(<BootConfigsView />)
    await screen.findByText('web')
    await userEvent.click(screen.getAllByRole('button', { name: 'Validate' })[0])
    // Matcher must be specific: /valid/i also matches the "Validate" button label.
    expect(await screen.findByText(/is valid/)).toBeInTheDocument()
  })

  it('Validate reports INVALID when preview 200s with the error folded into report', async () => {
    // This is the real backend behavior (api_configs.go:236-240) — a bad config is
    // NOT a rejection. Awaiting the promise is not a validity check (SGE B1).
    vi.mocked(configsApi.listConfigs).mockResolvedValue([cfgRow()])
    vi.mocked(configsApi.previewConfig).mockResolvedValue({
      rendered: '',
      contentType: '',
      report: 'render failed | butane: line 3: unknown key "storag"',
    })
    render(<BootConfigsView />)
    await screen.findByText('web')
    await userEvent.click(screen.getAllByRole('button', { name: 'Validate' })[0])
    expect(await screen.findByText(/unknown key/)).toBeInTheDocument()
  })

  it('Validate surfaces the error body when preview rejects (422 non-renderable / no revision)', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([cfgRow()])
    vi.mocked(configsApi.previewConfig).mockRejectedValue(
      new Error('POST /configs/1/preview failed: 422: config has no active revision'),
    )
    render(<BootConfigsView />)
    await screen.findByText('web')
    await userEvent.click(screen.getAllByRole('button', { name: 'Validate' })[0])
    expect(await screen.findByText(/no active revision/)).toBeInTheDocument()
  })

  it('Validate is disabled for taloscluster configs (not renderable)', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([cfgRow({ id: 2, name: 'prod-spec', kind: 'taloscluster' })])
    render(<BootConfigsView />)
    await screen.findByText('prod-spec')
    expect(screen.getByRole('button', { name: 'Validate' })).toBeDisabled()
  })

  it('changing a role default config inline calls updateRole', async () => {
    vi.mocked(rolesApi.listRoles).mockResolvedValue([{ id: 1, name: 'cp', defaultConfigId: undefined, hostCount: 0 }])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 7, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    vi.mocked(rolesApi.updateRole).mockResolvedValue(undefined)
    render(<BootConfigsView />)
    // switch to Roles tab
    await userEvent.click(screen.getByRole('tab', { name: 'Roles' }))
    await screen.findByText('cp')
    // Find the select wrapper by aria-label
    const selectWrapper = document.querySelector('[aria-label="default config for cp"]') as HTMLElement
    expect(selectWrapper).toBeInTheDocument()
    // Find the clickable selector div inside
    const selector = selectWrapper?.querySelector('.ant-select-selector') as HTMLElement
    expect(selector).toBeInTheDocument()
    await userEvent.click(selector)
    // Wait for the dropdown to render
    await waitFor(() => {
      const option = document.querySelector('.ant-select-item-option-content')
      expect(option).toBeInTheDocument()
    })
    // Find and click the "web" option in the dropdown
    const webInDropdown = await screen.findByText('web', { selector: '.ant-select-item-option-content' })
    await userEvent.click(webInDropdown)
    // Verify the API was called
    await waitFor(() => expect(rolesApi.updateRole).toHaveBeenCalledWith(1, { name: 'cp', defaultConfigId: 7 }))
  })
})
