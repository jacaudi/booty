import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ClustersView from './ClustersView'
import * as clustersApi from '../api/clusters'
import * as configsApi from '../api/configs'

vi.mock('../api/clusters')
vi.mock('../api/configs')

beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(configsApi.listConfigs).mockResolvedValue([])
})

describe('ClustersView', () => {
  it('lists clusters with member counts and versions', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [{ mac: 'aa:bb', hostname: 'cp', machineType: 'controlplane', status: 'booted' }], updatedAt: '' },
    ])
    render(<ClustersView />)
    await waitFor(() => screen.getByText('prod'))
    expect(screen.getByText('v1.13.5')).toBeInTheDocument()
  })

  it('renders a memberless cluster (members null) without crashing', async () => {
    // The API serializes a memberless cluster's members as null (Go nil slice);
    // the view must tolerate it and show a 0 count, not throw on .length.
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'nomembers', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: null as unknown as [], updatedAt: '' },
    ])
    render(<ClustersView />)
    await waitFor(() => screen.getByText('nomembers'))
    expect(screen.getByText('nomembers')).toBeInTheDocument()
  })

  it('create submits the pinned inputs', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([])
    vi.mocked(clustersApi.createCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await userEvent.click(await screen.findByRole('button', { name: /create cluster/i }))
    await userEvent.type(await screen.findByLabelText(/name/i), 'newc')
    await userEvent.type(screen.getByLabelText(/endpoint/i), 'https://10.0.0.10:6443')
    await userEvent.type(screen.getByLabelText(/talos version/i), 'v1.13.5')
    await userEvent.type(screen.getByLabelText(/kubernetes version/i), 'v1.34.0')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(clustersApi.createCluster).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'newc', endpoint: 'https://10.0.0.10:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0' }),
    ))
  })

  it('create will not submit while required fields are empty', async () => {
    // validateFields() REJECTS on the empty required inputs; clicking OK must
    // surface inline errors and early-return, not leak an unhandled promise
    // rejection (which fails the vitest CI run even when this assertion passes).
    vi.mocked(clustersApi.listClusters).mockResolvedValue([])
    vi.mocked(clustersApi.createCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await userEvent.click(await screen.findByRole('button', { name: /create cluster/i }))
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(clustersApi.createCluster).not.toHaveBeenCalled())
  })

  it('Edit will not submit when a required field is cleared', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(clustersApi.updateCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.clear(screen.getByLabelText(/endpoint/i))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(clustersApi.updateCluster).not.toHaveBeenCalled())
  })

  it('Export shows the returned secrets yaml', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(clustersApi.exportClusterSecrets).mockResolvedValue({ secretsYaml: 'SECRET-BUNDLE-YAML' })
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Export' }))
    expect(await screen.findByDisplayValue('SECRET-BUNDLE-YAML')).toBeInTheDocument()
  })

  it('Edit PUTs the updated pinned inputs without a specConfigId', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(clustersApi.updateCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    const version = screen.getByLabelText('Talos version')
    await userEvent.clear(version)
    await userEvent.type(version, 'v1.13.6')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(clustersApi.updateCluster).toHaveBeenCalledWith(1, {
      endpoint: 'https://e:6443', talosVersion: 'v1.13.6', k8sVersion: 'v1.34.0',
    }))
  })

  it('Edit surfaces an error and does not claim success when updateCluster fails', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(clustersApi.updateCluster).mockRejectedValue(new Error('422: pin failed'))
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(clustersApi.updateCluster).toHaveBeenCalled())
    expect(await screen.findByText('422: pin failed')).toBeInTheDocument()
    // The modal must stay open on failure — closing it would misrepresent the update as done.
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
  })

  it('Export surfaces an error when exportClusterSecrets fails', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(clustersApi.exportClusterSecrets).mockRejectedValue(new Error('422: export requires --secretsKey (fail-closed)'))
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Export' }))
    expect(await screen.findByText('422: export requires --secretsKey (fail-closed)')).toBeInTheDocument()
  })

  it('the Edit modal offers only taloscluster configs as the Spec config', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 9, name: 'prod-spec', kind: 'taloscluster', activeRevision: 1, revisionCount: 1, updatedAt: '' },
      { id: 7, name: 'web', kind: 'butane', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(await screen.findByRole('combobox', { name: 'Spec config' }))
    expect(await screen.findByText('prod-spec', { selector: '.ant-select-item-option-content' })).toBeInTheDocument()
    expect(screen.queryByText('web', { selector: '.ant-select-item-option-content' })).not.toBeInTheDocument()
  })

  it('Edit sends specConfigId once a spec is picked', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', members: [], updatedAt: '' },
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 9, name: 'prod-spec', kind: 'taloscluster', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    vi.mocked(clustersApi.updateCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    await userEvent.click(await screen.findByRole('combobox', { name: 'Spec config' }))
    await userEvent.click(await screen.findByText('prod-spec', { selector: '.ant-select-item-option-content' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(clustersApi.updateCluster).toHaveBeenCalledWith(1, {
      endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', specConfigId: 9,
    }))
  })

  it('offers no way to clear a bound spec (the server cannot express it)', async () => {
    // Omitting specConfigId PRESERVES the binding and the server cannot clear one
    // (a nil pointer is indistinguishable from an explicit null,
    // api_clusters.go:198-206). An allowClear would silently no-op while
    // reporting success. Unbinding is backend follow-up work.
    vi.mocked(clustersApi.listClusters).mockResolvedValue([
      { id: 1, name: 'prod', endpoint: 'https://e:6443', talosVersion: 'v1.13.5', k8sVersion: 'v1.34.0', specConfigId: 9, members: [], updatedAt: '' },
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 9, name: 'prod-spec', kind: 'taloscluster', activeRevision: 1, revisionCount: 1, updatedAt: '' },
    ])
    render(<ClustersView />)
    await screen.findByText('prod')
    await userEvent.click(screen.getByRole('button', { name: 'Edit' }))
    const select = (await screen.findByRole('combobox', { name: 'Spec config' })).closest('.ant-select')
    expect(select?.querySelector('.ant-select-clear')).not.toBeInTheDocument()
  })

  it('import adds control-plane rows and submits them as an array', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([])
    vi.mocked(clustersApi.importCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Import' }))
    await userEvent.type(await screen.findByLabelText(/name/i), 'adopted')
    // First (default) row.
    await userEvent.type(screen.getAllByPlaceholderText('aa:bb:cc:dd:ee:ff')[0], 'aa:bb:cc:dd:ee:00')
    await userEvent.type(screen.getAllByPlaceholderText(/paste this node/i)[0], 'CONFIG-0')
    // Add a second row and fill it.
    await userEvent.click(screen.getByRole('button', { name: /add control-plane host/i }))
    await userEvent.type(screen.getAllByPlaceholderText('aa:bb:cc:dd:ee:ff')[1], 'aa:bb:cc:dd:ee:01')
    await userEvent.type(screen.getAllByPlaceholderText(/paste this node/i)[1], 'CONFIG-1')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(clustersApi.importCluster).toHaveBeenCalledWith({
      name: 'adopted',
      controlPlanes: [
        { mac: 'aa:bb:cc:dd:ee:00', controlplane: 'CONFIG-0' },
        { mac: 'aa:bb:cc:dd:ee:01', controlplane: 'CONFIG-1' },
      ],
    }))
  })

  it('import will not submit while a row is missing its config', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([])
    vi.mocked(clustersApi.importCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Import' }))
    await userEvent.type(await screen.findByLabelText(/name/i), 'adopted')
    // Fill only the MAC; leave the controlplane.yaml empty → required validation blocks submit.
    await userEvent.type(screen.getAllByPlaceholderText('aa:bb:cc:dd:ee:ff')[0], 'aa:bb:cc:dd:ee:00')
    await userEvent.click(screen.getByRole('button', { name: /^ok$/i }))
    await waitFor(() => expect(clustersApi.importCluster).not.toHaveBeenCalled())
  })

  it('import can remove an added control-plane row', async () => {
    vi.mocked(clustersApi.listClusters).mockResolvedValue([])
    vi.mocked(clustersApi.importCluster).mockResolvedValue(undefined)
    render(<ClustersView />)
    await userEvent.click(await screen.findByRole('button', { name: 'Import' }))
    // One row by default; add a second → two MAC inputs.
    await userEvent.click(screen.getByRole('button', { name: /add control-plane host/i }))
    expect(screen.getAllByPlaceholderText('aa:bb:cc:dd:ee:ff')).toHaveLength(2)
    // Remove it → back to one.
    await userEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    expect(screen.getAllByPlaceholderText('aa:bb:cc:dd:ee:ff')).toHaveLength(1)
  })
})
