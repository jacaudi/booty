import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { message } from 'antd'
import type { DiscoveredHost, Host } from '../api/types'
import HostsView from './HostsView'
import * as client from '../api/client'
import * as configsApi from '../api/configs'
import * as rolesApi from '../api/roles'
import { listOS, loadFamilyKinds } from '../api/catalog'

vi.mock('../api/client')
vi.mock('../api/configs')
vi.mock('../api/roles')
vi.mock('../api/catalog')

const host = (over: Partial<Host>): Host => ({
  mac: 'aa',
  hostname: 'h',
  ip: 'i',
  booted: '',
  ...over,
})

beforeEach(() => {
  vi.mocked(loadFamilyKinds).mockResolvedValue({
    bootConfigKinds: ['butane', 'machineconfig', 'debianconfig'],
    osFamily: { talos: ['machineconfig'], debian: ['debianconfig'], flatcar: ['butane'], 'fedora-coreos': ['butane'] },
  })
})

afterEach(() => vi.resetAllMocks())

describe('HostsView', () => {
  it('renders pending and approved hosts', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({ mac: 'pending-mac', approved: false }),
      host({ mac: 'approved-mac', approved: true, bootMode: 'assigned', assignedOS: 'talos' }),
    ], discovered: [], discoveredFull: false })
    render(<HostsView />)
    await waitFor(() => expect(screen.getByText('pending-mac')).toBeInTheDocument())
    await userEvent.click(screen.getByRole('tab', { name: /Approved/ }))
    expect(screen.getByText('approved-mac')).toBeInTheDocument()
    expect(screen.getByText('talos')).toBeInTheDocument()
  })

  it('Approve submits the extended approve', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [{ mac: 'aa:bb', hostname: '', ip: '', os: 'flatcar', booted: '', approved: false } as Host], discovered: [], discoveredFull: false })
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    vi.mocked(client.approveHostWith).mockResolvedValue(undefined)
    render(<HostsView />)
    await waitFor(() => screen.getByText('aa:bb'))
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    await userEvent.click(await screen.findByRole('button', { name: /^ok$|approve/i }))
    expect(client.approveHostWith).toHaveBeenCalledWith('aa:bb', expect.any(Object))
    await waitFor(() => expect(client.listHostInventory).toHaveBeenCalledTimes(2))
  })

  it('revoke calls revokeHost on an approved host', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [host({ mac: 'a1', approved: true })], discovered: [], discoveredFull: false })
    vi.mocked(client.revokeHost).mockResolvedValue(undefined)
    render(<HostsView />)
    await userEvent.click(screen.getByRole('tab', { name: /Approved/ }))
    await waitFor(() => screen.getByText('a1'))
    await userEvent.click(screen.getByRole('button', { name: 'Revoke' }))
    expect(client.revokeHost).toHaveBeenCalledWith('a1')
  })

  it('wake asks for confirmation then calls wakeHost', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [host({ mac: 'w1', approved: true })], discovered: [], discoveredFull: false })
    vi.mocked(client.wakeHost).mockResolvedValue({ sentTo: ['10.0.0.255:9'] })
    render(<HostsView />)
    await userEvent.click(screen.getByRole('tab', { name: /Approved/ }))
    await waitFor(() => screen.getByText('w1'))
    await userEvent.click(screen.getByRole('button', { name: 'Wake' }))
    expect(client.wakeHost).not.toHaveBeenCalled()
    await userEvent.click(await screen.findByRole('button', { name: 'Wake now' }))
    expect(client.wakeHost).toHaveBeenCalledWith('w1')
  })

  it('wake asks for confirmation then calls wakeHost on a pending host', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [host({ mac: 'w2', approved: false })], discovered: [], discoveredFull: false })
    vi.mocked(client.wakeHost).mockResolvedValue({ sentTo: ['10.0.0.255:9'] })
    render(<HostsView />)
    await waitFor(() => screen.getByText('w2'))
    await userEvent.click(screen.getByRole('button', { name: 'Wake' }))
    expect(client.wakeHost).not.toHaveBeenCalled()
    await userEvent.click(await screen.findByRole('button', { name: 'Wake now' }))
    expect(client.wakeHost).toHaveBeenCalledWith('w2')
  })

  it('shows an error alert when loading fails', async () => {
    vi.mocked(client.listHostInventory).mockRejectedValue(new Error('boom'))
    render(<HostsView />)
    await waitFor(() => expect(screen.getByText('boom')).toBeInTheDocument())
  })

  it('Approve on a talos host binds the schematic BEFORE approving', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      { mac: 'ta:lo', hostname: '', ip: '', os: 'talos', booted: '', approved: false } as Host,
    ], discovered: [], discoveredFull: false })
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 4, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1, derivedSchematicId: 'a1b2c3d4', updatedAt: '' },
    ])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    const order: string[] = []
    vi.mocked(client.bindSchematic).mockImplementation(async () => { order.push('bind') })
    vi.mocked(client.approveHostWith).mockImplementation(async () => { order.push('approve') })
    render(<HostsView />)
    await waitFor(() => screen.getByText('ta:lo'))
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    const schematicInput = await screen.findByLabelText(/talos schematic/i)
    await userEvent.type(schematicInput, 'iscsi')
    await userEvent.click(await screen.findByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(client.bindSchematic).toHaveBeenCalledWith('ta:lo', { configId: 4 }))
    expect(order).toEqual(['bind', 'approve'])
  })

  it('Approve on a non-talos host shows no schematic field', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      { mac: 'fl:at', hostname: '', ip: '', os: 'flatcar', booted: '', approved: false } as Host,
    ], discovered: [], discoveredFull: false })
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<HostsView />)
    await waitFor(() => screen.getByText('fl:at'))
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    await screen.findByLabelText(/config/i)
    expect(screen.queryByLabelText(/talos schematic/i)).not.toBeInTheDocument()
  })

  it('shows Pending and Approved tabs with a pending count badge', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      { mac: 'aa:bb', hostname: 'p1', ip: '', booted: '', approved: false } as Host,
      { mac: 'cc:dd', hostname: 'a1', ip: '', booted: '', approved: true } as Host,
    ], discovered: [], discoveredFull: false })
    render(<HostsView />)
    const pendingTab = await screen.findByRole('tab', { name: /Pending/ })
    expect(screen.getByRole('tab', { name: /Approved/ })).toBeInTheDocument()
    // Scope the count to the tab — a bare getByText('1') is ambiguous once other
    // numerals render on the page.
    expect(within(pendingTab).getByText('1')).toBeInTheDocument()
  })

  it('the Approved table is reachable via its tab', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      { mac: 'cc:dd', hostname: 'a1', ip: '', booted: '', approved: true } as Host,
    ], discovered: [], discoveredFull: false })
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    expect(await screen.findByText('cc:dd')).toBeInTheDocument()
  })

  const allConfigs = [
    { id: 6, name: 'talos-node', kind: 'machineconfig' as const, activeRevision: 1, revisionCount: 1, updatedAt: '' },
    { id: 7, name: 'web', kind: 'butane' as const, activeRevision: 1, revisionCount: 1, updatedAt: '' },
    { id: 8, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1, updatedAt: '' },
    { id: 9, name: 'prod-spec', kind: 'taloscluster' as const, activeRevision: 1, revisionCount: 1, updatedAt: '' },
  ]

  const openApproveFor = async (os: string | undefined) => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      { mac: 'ho:st', hostname: '', ip: '', os, booted: '', approved: false } as Host,
    ], discovered: [], discoveredFull: false })
    vi.mocked(configsApi.listConfigs).mockResolvedValue(allConfigs)
    vi.mocked(rolesApi.listRoles).mockResolvedValue([])
    render(<HostsView />)
    await waitFor(() => screen.getByText('ho:st'))
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    await userEvent.click(await screen.findByRole('combobox', { name: 'Config' }))
  }

  const optionNames = () =>
    [...document.querySelectorAll('.ant-select-item-option-content')].map((n) => n.textContent)

  it('the Approve modal Config Select offers neither a schematic nor a taloscluster', async () => {
    // Silent no-op: familyAllowsKind rejects both, so resolveConfig falls through
    // to the default file with only a slog.Warn — a bound config, an unbound boot.
    await openApproveFor('talos')
    await screen.findByText('talos-node', { selector: '.ant-select-item-option-content' })
    expect(optionNames()).not.toContain('prod-spec')
    expect(optionNames()).not.toContain('iscsi')
  })

  it('the Approve modal Config Select offers only kinds the host OS family admits', async () => {
    // familyAllowsKind is PER-FAMILY: a butane config bound to a TALOS host fails
    // on the same path with the same silent fall-through as a taloscluster.
    await openApproveFor('talos')
    await screen.findByText('talos-node', { selector: '.ant-select-item-option-content' })
    expect(optionNames()).toEqual(['talos-node'])
  })

  it('the Approve modal Config Select stays permissive for a host with no known OS', async () => {
    // A host that has not booted yet has no OS. Offering the full boot-config
    // union beats hiding every option; the server rejects a bad bind loudly.
    await openApproveFor(undefined)
    await screen.findByText('web', { selector: '.ant-select-item-option-content' })
    expect(optionNames()).toEqual(['talos-node', 'web'])
    // Still never the unbindable kinds.
    expect(optionNames()).not.toContain('iscsi')
    expect(optionNames()).not.toContain('prod-spec')
  })

  it('shows the running version and a failed-unit badge', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({
        mac: 'h1',
        approved: true,
        health: { running: '4152.2.0', kernel: 'k', firmware: 'uefi', receivedAt: 't', failedUnitCount: 2, vendor: 'v', product: 'p' },
      }),
    ], discovered: [], discoveredFull: false })
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    expect(screen.getByText('4152.2.0')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument()
  })

  it('shows a dash when a host has no health', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [host({ mac: 'h2', approved: true })], discovered: [], discoveredFull: false })
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2)
  })

  it('opens the health drawer listing failed units', async () => {
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({
        mac: 'mac',
        approved: true,
        health: { running: '4152.2.0', kernel: 'k', firmware: 'uefi', receivedAt: 't', failedUnitCount: 1, vendor: 'v', product: 'p' },
      }),
    ], discovered: [], discoveredFull: false })
    vi.mocked(client.getHostHealth).mockResolvedValue({
      running: '4152.2.0', kernel: '6.6', firmware: 'uefi', receivedAt: 't', bootID: 'b',
      failedUnits: ['sshd.service'], journalErrors: ['oops line'], dmi: { vendor: 'v', product: 'p', biosVersion: '1' },
    })
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    await userEvent.click(screen.getByRole('button', { name: /4152\.2\.0/ }))
    expect(client.getHostHealth).toHaveBeenCalledWith('mac')
    expect(await screen.findByText('Health: mac')).toBeInTheDocument()
    expect(await screen.findByText('sshd.service')).toBeInTheDocument()
  })

  it('ignores a late health response for a previously opened host', async () => {
    const h = (running: string) => ({ running, kernel: 'k', firmware: 'uefi', receivedAt: 't', failedUnitCount: 0, vendor: 'v', product: 'p' })
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({ mac: 'mac-a', approved: true, health: h('1.0.0') }),
      host({ mac: 'mac-b', approved: true, health: h('2.0.0') }),
    ], discovered: [], discoveredFull: false })
    const detail = (kernel: string) => ({
      running: 'r', kernel, firmware: 'uefi', receivedAt: 't', bootID: 'b',
      failedUnits: [], journalErrors: [], dmi: { vendor: 'v', product: 'p', biosVersion: '1' },
    })
    let resolveA: (v: ReturnType<typeof detail>) => void = () => {}
    vi.mocked(client.getHostHealth).mockImplementation((mac: string) =>
      mac === 'mac-a' ? new Promise((r) => { resolveA = r }) : Promise.resolve(detail('kernel-of-B')),
    )
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    await userEvent.click(screen.getByRole('button', { name: /1\.0\.0/ }))
    await userEvent.click(screen.getByRole('button', { name: /2\.0\.0/ }))
    expect(await screen.findByText('Kernel: kernel-of-B')).toBeInTheDocument()
    resolveA(detail('kernel-of-A'))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.getByText('Health: mac-b')).toBeInTheDocument()
    expect(screen.getByText('Kernel: kernel-of-B')).toBeInTheDocument()
    expect(screen.queryByText('Kernel: kernel-of-A')).not.toBeInTheDocument()
  })

  it('lists duplicate failed unit names without key collisions', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({ mac: 'mac', approved: true, health: { running: '9.9.9', kernel: 'k', firmware: 'uefi', receivedAt: 't', failedUnitCount: 2, vendor: 'v', product: 'p' } }),
    ], discovered: [], discoveredFull: false })
    vi.mocked(client.getHostHealth).mockResolvedValue({
      running: '9.9.9', kernel: 'k', firmware: 'uefi', receivedAt: 't', bootID: 'b',
      failedUnits: ['dup.service', 'dup.service'], journalErrors: [], dmi: { vendor: 'v', product: 'p', biosVersion: '1' },
    })
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    await userEvent.click(screen.getByRole('button', { name: /9\.9\.9/ }))
    expect(await screen.findAllByText('dup.service')).toHaveLength(2)
    expect(errSpy.mock.calls.flat().join(' ')).not.toMatch(/same key/)
    errSpy.mockRestore()
  })

  it('reports a failed health fetch with message.error and keeps rendering', async () => {
    const errSpy = vi.spyOn(message, 'error').mockImplementation(() => ({}) as ReturnType<typeof message.error>)
    vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [
      host({ mac: 'mac', approved: true, health: { running: '7.7.7', kernel: 'k', firmware: 'uefi', receivedAt: 't', failedUnitCount: 0, vendor: 'v', product: 'p' } }),
    ], discovered: [], discoveredFull: false })
    vi.mocked(client.getHostHealth).mockRejectedValue(new Error('boom'))
    render(<HostsView />)
    await userEvent.click(await screen.findByRole('tab', { name: /Approved/ }))
    await userEvent.click(screen.getByRole('button', { name: /7\.7\.7/ }))
    await waitFor(() => expect(errSpy).toHaveBeenCalledWith('boom'))
    expect(screen.getByText('Health: mac')).toBeInTheDocument()
    errSpy.mockRestore()
  })

  describe('New machines', () => {
    const D: DiscoveredHost = { mac: '52:54:00:12:34:58', arch: 'amd64', ip: '10.0.2.15', firstSeen: '2026-10-15T05:00:00Z', lastSeen: '2026-10-15T05:01:00Z' }
    const seed = (over: { hosts?: Host[]; discovered?: DiscoveredHost[]; discoveredFull?: boolean } = {}) => {
      vi.mocked(client.listHostInventory).mockResolvedValue({ hosts: [], discovered: [D], discoveredFull: false, ...over })
      vi.mocked(configsApi.listConfigs).mockResolvedValue([])
      vi.mocked(rolesApi.listRoles).mockResolvedValue([])
      vi.mocked(listOS).mockResolvedValue([
        { name: 'flatcar', family: 'ignition', requiredParams: [] },
        { name: 'talos', family: 'talos', requiredParams: [] },
        { name: 'memtest86plus', family: 'tool', requiredParams: [] },
        { name: 'fedora-coreos', family: 'ignition', requiredParams: [] },
      ])
      vi.mocked(client.createHost).mockResolvedValue(undefined)
      vi.mocked(client.bindSchematic).mockResolvedValue(undefined)
      vi.mocked(client.approveHostWith).mockResolvedValue(undefined)
      vi.mocked(client.deleteHost).mockResolvedValue(undefined)
      vi.mocked(client.dismissDiscovered).mockResolvedValue(undefined)
      vi.mocked(client.dismissAllDiscovered).mockResolvedValue({ deleted: 1 })
    }
    const openModal = async () => {
      render(<HostsView />)
      await screen.findByText(D.mac)
      await userEvent.click(screen.getByRole('button', { name: 'Approve' }))
      await screen.findByText(`Approve ${D.mac}`)
    }
    const chooseOS = async (name: string) => {
      await userEvent.click(await screen.findByRole('combobox', { name: 'OS' }))
      await userEvent.click(await screen.findByText(name, { selector: '.ant-select-item-option-content' }))
    }
    const ok = () => userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    const order = (f: unknown) => (f as { mock: { invocationCallOrder: number[] } }).mock.invocationCallOrder[0]

    it('renders New machines from discovered', async () => {
      seed()
      render(<HostsView />)
      expect(await screen.findByText('New machines')).toBeInTheDocument()
      expect(screen.getByText(D.mac)).toBeInTheDocument()
      expect(screen.getByText('amd64')).toBeInTheDocument()
      expect(screen.getByText('10.0.2.15')).toBeInTheDocument()
    })

    it('hides New machines when empty and not full', async () => {
      seed({ discovered: [] })
      render(<HostsView />)
      await waitFor(() => expect(client.listHostInventory).toHaveBeenCalled())
      expect(screen.queryByText('New machines')).toBeNull()
    })

    it('shows the full alert', async () => {
      seed({ discoveredFull: true })
      render(<HostsView />)
      expect(await screen.findByText(/full \(1024\)/)).toBeInTheDocument()
    })

    it('discovered approve requires an OS', async () => {
      seed()
      await openModal()
      await ok()
      expect(await screen.findByText('Choose an OS')).toBeInTheDocument()
      expect(client.createHost).not.toHaveBeenCalled()
      expect(client.deleteHost).not.toHaveBeenCalled()
    })

    it('OS options exclude tool OSes', async () => {
      seed()
      await openModal()
      await userEvent.click(await screen.findByRole('combobox', { name: 'OS' }))
      await screen.findByText('flatcar', { selector: '.ant-select-item-option-content' })
      expect(optionNames()).toEqual(expect.arrayContaining(['flatcar', 'talos', 'fedora-coreos']))
      expect(optionNames()).not.toContain('memtest86plus')
    })

    it('approve calls createHost then approveHostWith', async () => {
      seed()
      await openModal()
      await chooseOS('flatcar')
      await ok()
      await waitFor(() => expect(client.approveHostWith).toHaveBeenCalledWith(D.mac, { configId: undefined, roleIds: undefined }))
      expect(client.createHost).toHaveBeenCalledWith({ mac: D.mac, os: 'flatcar' })
      expect(order(client.createHost)).toBeLessThan(order(client.approveHostWith))
      expect(client.bindSchematic).not.toHaveBeenCalled()
    })

    it('talos approve binds the schematic between', async () => {
      seed()
      await openModal()
      await chooseOS('talos')
      await userEvent.type(await screen.findByLabelText(/talos schematic/i), 'abc123')
      await ok()
      await waitFor(() => expect(client.approveHostWith).toHaveBeenCalled())
      expect(client.bindSchematic).toHaveBeenCalledWith(D.mac, { schematic: 'abc123' })
      expect(order(client.createHost)).toBeLessThan(order(client.bindSchematic))
      expect(order(client.bindSchematic)).toBeLessThan(order(client.approveHostWith))
    })

    it('failed step 3 rolls back', async () => {
      seed()
      const errSpy = vi.spyOn(message, 'error').mockImplementation(() => ({}) as ReturnType<typeof message.error>)
      vi.mocked(client.approveHostWith).mockRejectedValue(new Error('boom'))
      await openModal()
      await chooseOS('flatcar')
      await ok()
      await waitFor(() => expect(client.deleteHost).toHaveBeenCalledWith(D.mac))
      await waitFor(() => expect(vi.mocked(client.listHostInventory).mock.calls.length).toBeGreaterThanOrEqual(2))
      errSpy.mockRestore()
    })

    it('failed step 1 deletes nothing', async () => {
      seed()
      const errSpy = vi.spyOn(message, 'error').mockImplementation(() => ({}) as ReturnType<typeof message.error>)
      vi.mocked(client.createHost).mockRejectedValue(new Error('nope'))
      await openModal()
      await chooseOS('flatcar')
      await ok()
      await waitFor(() => expect(errSpy).toHaveBeenCalledWith('nope'))
      expect(client.deleteHost).not.toHaveBeenCalled()
      expect(client.approveHostWith).not.toHaveBeenCalled()
      errSpy.mockRestore()
    })

    it('dismiss confirms then deletes', async () => {
      seed()
      render(<HostsView />)
      await screen.findByText(D.mac)
      await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
      expect(client.dismissDiscovered).not.toHaveBeenCalled()
      await userEvent.click(await screen.findByRole('button', { name: 'OK' }))
      await waitFor(() => expect(client.dismissDiscovered).toHaveBeenCalledWith(D.mac))
    })

    it('dismiss all confirms then deletes', async () => {
      seed()
      render(<HostsView />)
      await screen.findByText(D.mac)
      await userEvent.click(screen.getByRole('button', { name: 'Dismiss all' }))
      await userEvent.click(await screen.findByRole('button', { name: 'OK' }))
      await waitFor(() => expect(client.dismissAllDiscovered).toHaveBeenCalled())
    })

    it('registered pending host shows Approve, never Allow', async () => {
      seed({ discovered: [], hosts: [{ mac: 'aa', hostname: 'h', ip: '', booted: '' }] })
      render(<HostsView />)
      await screen.findByText('aa')
      expect(screen.queryByText(/Allow/)).toBeNull()
      await userEvent.click(screen.getByRole('button', { name: 'Approve' }))
      await screen.findByText('Approve aa')
      expect(screen.queryByText(/Allow/)).toBeNull()
    })
  })
})
