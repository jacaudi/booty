import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { message } from 'antd'
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
      { id: 5, name: 'c', kind: 'debianconfig', activeRevision: 2, revisionCount: 2, updatedAt: '' },
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

  it('Configs list excludes taloscluster (a cluster spec is not a boot config)', async () => {
    // It is owned by the Clusters page now. It was never renderable
    // (api_configs.go:211-215), which is why its Validate button was permanently
    // disabled — the row itself was the mistake.
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      cfgRow({ id: 1, name: 'web' }),
      cfgRow({ id: 2, name: 'prod-spec', kind: 'taloscluster' }),
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await screen.findByText('web')
    expect(screen.queryByText('prod-spec')).not.toBeInTheDocument()
  })

  it('the Kind cell leads with the OS product name over the raw server kind', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      cfgRow({ id: 1, name: 'talos-node', kind: 'machineconfig' }),
      cfgRow({ id: 2, name: 'fc', kind: 'butane' }),
      cfgRow({ id: 3, name: 'deb', kind: 'debianconfig' }),
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await screen.findByText('talos-node')
    expect(screen.getByText('Talos Linux')).toBeInTheDocument()
    expect(screen.getByText('Flatcar / Fedora CoreOS')).toBeInTheDocument()
    expect(screen.getByText('Debian')).toBeInTheDocument()
    // The literal server kind is still shown beneath the product name.
    expect(screen.getByText('machineconfig')).toBeInTheDocument()
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
    // AntD forwards aria-label to both the wrapper div and the inner combobox
    // input, so plain getByLabelText is ambiguous (matches both). Scoping to
    // role=combobox makes it unique and is the accessible way to reach it.
    const selector = screen.getByRole('combobox', { name: 'default config for cp' })
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

  it('a rejected inline default-config update surfaces an error and does not show the attempted value as saved', async () => {
    const errorSpy = vi.spyOn(message, 'error')
    const successSpy = vi.spyOn(message, 'success')
    try {
      vi.mocked(rolesApi.listRoles).mockResolvedValue([{ id: 1, name: 'cp', defaultConfigId: undefined, hostCount: 0 }])
      vi.mocked(configsApi.listConfigs).mockResolvedValue([
        { id: 7, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
      ])
      vi.mocked(rolesApi.updateRole).mockRejectedValue(new Error('PUT /roles/1 failed: 500: db error'))
      render(<BootConfigsView />)
      await userEvent.click(screen.getByRole('tab', { name: 'Roles' }))
      await screen.findByText('cp')
      const selector = screen.getByRole('combobox', { name: 'default config for cp' })
      await userEvent.click(selector)
      await waitFor(() => {
        expect(document.querySelector('.ant-select-item-option-content')).toBeInTheDocument()
      })
      const webInDropdown = await screen.findByText('web', { selector: '.ant-select-item-option-content' })
      await userEvent.click(webInDropdown)
      await waitFor(() => expect(rolesApi.updateRole).toHaveBeenCalledWith(1, { name: 'cp', defaultConfigId: 7 }))
      await waitFor(() => expect(errorSpy).toHaveBeenCalledWith('PUT /roles/1 failed: 500: db error'))
      expect(successSpy).not.toHaveBeenCalled()
      // The role list was never reloaded (act() only calls load() on success), so the
      // Select must still reflect the unchanged (empty) value, not the attempted "web".
      // AntD's single (non-search) combobox <input> always has DOM value "" regardless
      // of selection, so asserting on it proves nothing; the actual selected label (if
      // any) renders in a sibling `.ant-select-selection-item`. Assert that is absent.
      const selectRoot = screen.getByRole('combobox', { name: 'default config for cp' }).closest('.ant-select')
      expect(selectRoot?.querySelector('.ant-select-selection-item')).not.toBeInTheDocument()
    } finally {
      errorSpy.mockRestore()
      successSpy.mockRestore()
    }
  })

  it('the create form picks an OS and derives the kind on the wire', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(configsApi.createConfig).mockResolvedValue(undefined)
    render(<BootConfigsView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Create Config' }))
    await userEvent.type(await screen.findByLabelText('Name'), 'debian-worker')
    await userEvent.click(screen.getByRole('radio', { name: 'Debian' }))
    await userEvent.type(screen.getByLabelText('Source'), 'hostname: w1')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() =>
      expect(configsApi.createConfig).toHaveBeenCalledWith({
        name: 'debian-worker',
        kind: 'debianconfig',
        source: 'hostname: w1',
      }),
    )
  })

  it('the create form shows the derived kind without letting you choose it', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Create Config' }))
    await userEvent.click(await screen.findByRole('radio', { name: 'Talos Linux' }))
    expect(await screen.findByTestId('derived-kind')).toHaveTextContent('machineconfig')
    // There is no Kind input to choose from.
    expect(screen.queryByRole('combobox', { name: 'Kind' })).not.toBeInTheDocument()
  })

  it('the create form does not offer raw preseed (debianconfig is the authoring format)', async () => {
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<BootConfigsView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Create Config' }))
    await screen.findByRole('radio', { name: 'Debian' })
    expect(screen.queryByRole('radio', { name: /preseed/i })).not.toBeInTheDocument()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
  })

  it('the role default-config Select offers neither a schematic nor a taloscluster', async () => {
    // Binding either silently no-ops: familyAllowsKind rejects them and
    // resolveConfig falls through to the default file with only a slog.Warn —
    // a bound config and an unbound boot (resolve.go:30,39-41).
    vi.mocked(rolesApi.listRoles).mockResolvedValue([{ id: 1, name: 'cp', defaultConfigId: undefined, hostCount: 0 }])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 7, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
      { id: 8, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1, updatedAt: '' },
      { id: 9, name: 'prod-spec', kind: 'taloscluster', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: 'Roles' }))
    await screen.findByText('cp')
    await userEvent.click(screen.getByRole('combobox', { name: 'default config for cp' }))
    await screen.findByText('web', { selector: '.ant-select-item-option-content' })
    expect(screen.queryByText('iscsi', { selector: '.ant-select-item-option-content' })).not.toBeInTheDocument()
    expect(screen.queryByText('prod-spec', { selector: '.ant-select-item-option-content' })).not.toBeInTheDocument()
  })

  it('the Edit Role modal Default Config Select offers no clear affordance', async () => {
    // UpdateRole (pkg/db/roles.go) only writes default_config_id when the
    // pointer is non-nil, and the PUT body field is a single *int64
    // (pkg/http/api_roles.go) — a JSON null and an absent key decode to the
    // same nil, so the server cannot express "unbind." An allowClear here
    // would silently no-op while the UI reports success. (This exact trap was
    // already hit and fixed once on the inline table-cell Select above; the
    // Create Role modal keeps allowClear on purpose — CreateRole inserts the
    // pointer directly, so there's no pre-existing binding to preserve.)
    vi.mocked(rolesApi.listRoles).mockResolvedValue([{ id: 1, name: 'cp', defaultConfigId: 7, hostCount: 0 }])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 7, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    render(<BootConfigsView />)
    await userEvent.click(screen.getByRole('tab', { name: 'Roles' }))
    await screen.findByText('cp')
    await userEvent.click(screen.getByRole('button', { name: /edit/i }))
    // Wait for the modal to open and the form to prefill.
    await screen.findByDisplayValue('cp')
    expect(document.querySelector('.ant-select-clear')).not.toBeInTheDocument()
  })
})
