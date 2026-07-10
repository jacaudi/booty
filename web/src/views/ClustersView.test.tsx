import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ClustersView from './ClustersView'
import * as clustersApi from '../api/clusters'

vi.mock('../api/clusters')

beforeEach(() => vi.resetAllMocks())

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
})
