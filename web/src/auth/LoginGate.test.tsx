import { useEffect } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import LoginGate from './LoginGate'
import { onUnauthorized } from '../api/client'

// onUnauthorizedTrigger invokes whatever handler LoginGate registered, which is
// how a real 401 from any view reaches the gate.
//
// NOTE: `vi.mock` calls are hoisted by Vitest above all imports in this file,
// so this block and the `registered` declaration it closes over live above the
// `describe` below rather than at the bottom — placing them after the tests
// that call `onUnauthorizedTrigger()` produced a temporal-dead-zone failure.
let registered: (() => void) | undefined
vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    onUnauthorized: (h: () => void) => {
      registered = h
    },
  }
})
function onUnauthorizedTrigger() {
  if (!registered) throw new Error('LoginGate did not register an unauthorized handler')
  registered()
}
void onUnauthorized

afterEach(() => vi.restoreAllMocks())

describe('LoginGate', () => {
  it('renders its children while authorized', () => {
    render(<LoginGate><div>inner content</div></LoginGate>)
    expect(screen.getByText('inner content')).toBeInTheDocument()
  })

  it('shows the token prompt once a 401 is reported', async () => {
    render(<LoginGate><div>inner content</div></LoginGate>)
    onUnauthorizedTrigger()
    await waitFor(() => expect(screen.getByLabelText(/api token/i)).toBeInTheDocument())
  })

  it('logs in with the pasted token and restores the children', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    render(<LoginGate><div>inner content</div></LoginGate>)
    onUnauthorizedTrigger()
    await waitFor(() => expect(screen.getByLabelText(/api token/i)).toBeInTheDocument())

    await userEvent.type(screen.getByLabelText(/api token/i), 'pasted-token')
    await userEvent.click(screen.getByRole('button', { name: /unlock/i }))

    await waitFor(() => expect(screen.getByText('inner content')).toBeInTheDocument())
    expect(fetchMock).toHaveBeenCalledWith('/login', expect.objectContaining({
      body: JSON.stringify({ token: 'pasted-token' }),
    }))
  })

  it('remounts its children after a successful unlock, so views refetch', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const mounts = vi.fn()

    function Probe() {
      useEffect(() => {
        mounts()
      }, [])
      return <div>inner content</div>
    }

    render(<LoginGate><Probe /></LoginGate>)
    expect(mounts).toHaveBeenCalledTimes(1)

    onUnauthorizedTrigger()
    await waitFor(() => expect(screen.getByLabelText(/api token/i)).toBeInTheDocument())
    await userEvent.type(screen.getByLabelText(/api token/i), 'pasted-token')
    await userEvent.click(screen.getByRole('button', { name: /unlock/i }))

    // Design section 10 asks the interceptor to "retry". request() does not
    // replay the failed call; instead the gate unmounts its children while
    // locked and remounts them on unlock, so every view's effect refires and
    // the data reloads. That is the retry, and this asserts it actually
    // happens rather than assuming React does it.
    await waitFor(() => expect(mounts).toHaveBeenCalledTimes(2))
  })

  it('keeps the prompt up and shows an error when the token is wrong', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))

    render(<LoginGate><div>inner content</div></LoginGate>)
    onUnauthorizedTrigger()
    await waitFor(() => expect(screen.getByLabelText(/api token/i)).toBeInTheDocument())

    await userEvent.type(screen.getByLabelText(/api token/i), 'wrong')
    await userEvent.click(screen.getByRole('button', { name: /unlock/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.queryByText('inner content')).not.toBeInTheDocument()
  })
})
