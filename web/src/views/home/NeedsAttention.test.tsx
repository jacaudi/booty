import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import NeedsAttention from './NeedsAttention'
import * as client from '../../api/client'

vi.mock('../../api/client')
afterEach(() => vi.resetAllMocks())
beforeEach(() => { vi.mocked(client.approveHost).mockResolvedValue({}) })

describe('NeedsAttention', () => {
  it('lists unapproved hosts (approved omitted) and approves one inline', async () => {
    const onChange = vi.fn()
    render(
      <MemoryRouter>
        <NeedsAttention
          hosts={[
            { mac: 'aa', hostname: 'unbooted', ip: '' },
            { mac: 'bb', hostname: 'done', ip: '', approved: true },
          ] as never}
          failedImages={0}
          onChange={onChange}
        />
      </MemoryRouter>,
    )
    expect(screen.getByText('unbooted')).toBeInTheDocument()
    expect(screen.queryByText('done')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /approve/i }))
    await waitFor(() => expect(client.approveHost).toHaveBeenCalledWith('aa'))
    await waitFor(() => expect(onChange).toHaveBeenCalled())
  })

  it('surfaces failed-verification images', () => {
    render(<MemoryRouter><NeedsAttention hosts={[]} failedImages={2} onChange={() => {}} /></MemoryRouter>)
    expect(screen.getByText(/2 cached images failed verification/i)).toBeInTheDocument()
  })

  it('shows an all-clear message when nothing is pending or failed', () => {
    render(
      <MemoryRouter>
        <NeedsAttention hosts={[{ mac: 'x', hostname: 'ok', ip: '', approved: true }] as never} failedImages={0} onChange={() => {}} />
      </MemoryRouter>,
    )
    expect(screen.getByText(/nothing needs attention/i)).toBeInTheDocument()
  })
})
