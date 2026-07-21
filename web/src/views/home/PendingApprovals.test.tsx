import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import PendingApprovals from './PendingApprovals'
import * as client from '../../api/client'

vi.mock('../../api/client')
afterEach(() => vi.resetAllMocks())
beforeEach(() => {
  vi.mocked(client.approveHost).mockResolvedValue({})
})

const renderPanel = () => render(<MemoryRouter><PendingApprovals /></MemoryRouter>)

describe('PendingApprovals', () => {
  it('lists unapproved hosts and approves one inline', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([
      { mac: 'aa', hostname: 'unbooted', ip: '', approved: false },
      { mac: 'bb', hostname: 'done', ip: '', approved: true },
    ] as never)
    renderPanel()
    expect(await screen.findByText('unbooted')).toBeInTheDocument()
    expect(screen.queryByText('done')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    await waitFor(() => expect(client.approveHost).toHaveBeenCalledWith('aa'))
  })
  it('renders nothing when there are no pending hosts', async () => {
    vi.mocked(client.listHosts).mockResolvedValue([{ mac: 'x', hostname: 'ok', ip: '', approved: true }] as never)
    const { container } = renderPanel()
    await waitFor(() => expect(client.listHosts).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })
})
