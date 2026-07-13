import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { message } from 'antd'
import type { CacheEntry } from '../api/cache'
import CacheView from './CacheView'
import * as api from '../api/cache'

vi.mock('../api/cache')

const entry = (o: Partial<CacheEntry>): CacheEntry => ({
  id: 1, os: 'talos', arch: 'amd64', version: 'v1.0.0', size: 1024,
  state: 'in-cycle', pinned: false, inWindow: true, fetchedAt: '', ...o,
})

afterEach(() => vi.resetAllMocks())

describe('CacheView', () => {
  it('shows a summary strip with used bytes and counts (no budget bar)', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, size: 1024, state: 'in-cycle' }),
      entry({ id: 2, size: 2048, state: 'archived', inWindow: false, verified: false }),
    ])
    render(<CacheView />)
    await waitFor(() => expect(screen.getByText('Used')).toBeInTheDocument())
    // 3072 bytes -> "3.0 KB"; no "/" budget denominator and no progressbar role.
    expect(screen.getByText('3.0 KB')).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  })

  it('groups versions under an os/channel section', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', version: 'v1.9.0' }),
      entry({ id: 2, os: 'talos', version: 'v1.8.0', inWindow: false, state: 'archived' }),
    ])
    render(<CacheView />)
    await waitFor(() => expect(screen.getByText('talos/talos')).toBeInTheDocument())
    expect(screen.getByText('v1.9.0')).toBeInTheDocument()
    expect(screen.getByText('v1.8.0')).toBeInTheDocument()
  })

  it('selecting a version drives the detail pane and pin acts on it', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 5, version: 'v1.9.0', pinned: false })])
    vi.mocked(api.pinCache).mockResolvedValue(undefined)
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(screen.getByText('v1.9.0'))
    const detail = screen.getByTestId('cache-detail')
    await userEvent.click(within(detail).getByRole('button', { name: 'Pin' }))
    expect(api.pinCache).toHaveBeenCalledWith(5)
    await waitFor(() => expect(api.listCache).toHaveBeenCalledTimes(2))
  })

  it('shows a warning icon (not an em-dash) for a not-yet-verified entry', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 1, version: 'v1.9.0', verified: undefined })])
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    expect(screen.getAllByLabelText('not yet verified').length).toBeGreaterThan(0)
    expect(screen.queryByText('—')).not.toBeInTheDocument()
  })

  it('surfaces arch as a column so same-version entries differing only by arch are distinguishable', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, version: 'v1.9.0', arch: 'amd64' }),
      entry({ id: 2, version: 'v1.9.0', arch: 'arm64' }),
    ])
    render(<CacheView />)
    await waitFor(() => screen.getByText('talos/talos'))
    expect(screen.getByText('amd64')).toBeInTheDocument()
    expect(screen.getByText('arm64')).toBeInTheDocument()
  })

  it('keeps a manually collapsed group collapsed after a reload triggered by an action', async () => {
    // rc-collapse keeps panel content mounted (hidden via CSS, which jsdom doesn't
    // compute) rather than removing it from the DOM, so assert on the panel
    // header's `aria-expanded` — the real signal of collapsed/expanded state —
    // rather than on row visibility.
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 5, version: 'v1.9.0', pinned: false })])
    vi.mocked(api.pinCache).mockResolvedValue(undefined)
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    // Select the version first (detail pane doesn't depend on collapse state).
    await userEvent.click(screen.getByText('v1.9.0'))
    const detail = screen.getByTestId('cache-detail')
    const header = screen.getByRole('button', { name: /talos\/talos/ })
    expect(header).toHaveAttribute('aria-expanded', 'true')
    // Collapse the talos/talos group.
    await userEvent.click(header)
    await waitFor(() => expect(header).toHaveAttribute('aria-expanded', 'false'))
    // Trigger a reload via an unrelated action (pin from the still-visible detail pane).
    await userEvent.click(within(detail).getByRole('button', { name: 'Pin' }))
    await waitFor(() => expect(api.listCache).toHaveBeenCalledTimes(2))
    // The group must still be collapsed after the reload.
    expect(header).toHaveAttribute('aria-expanded', 'false')
  })

  it('the detail Delete button is disabled (403 until P10)', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 5, version: 'v1.9.0' })])
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(screen.getByText('v1.9.0'))
    const detail = screen.getByTestId('cache-detail')
    expect(within(detail).getByRole('button', { name: 'Delete' })).toBeDisabled()
  })

  // NOTE (SGE B5b): "Failed" / "In cycle" each appear TWICE in the DOM — once as a
  // summary Statistic title and once as a Segmented option — so `getByText` throws
  // on multiple matches. AntD Segmented renders its options as radios; select by role.
  // The <input> itself is `pointer-events: none` (AntD positions the visible label
  // on top); a real click lands on the wrapping <label>, which the browser then
  // delegates to the input natively — so click the label, not the input.
  const segmented = (name: string) => {
    const label = screen.getByRole('radio', { name }).closest('label')
    if (!label) throw new Error(`no label wrapping radio "${name}"`)
    return label
  }

  it('the Failed segmented filter is applied client-side without refetching', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, version: 'v1.9.0', verified: true }),
      entry({ id: 2, version: 'v1.8.0', verified: false }),
    ])
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(segmented('Failed'))
    // Client filter hides the verified row; listCache is NOT re-called for Failed
    // (its server filter is identical to All's). This asserts the SGE B5a fix:
    // load() must key on the DERIVED SERVER FILTER, not on stateFilter directly.
    await waitFor(() => expect(screen.queryByText('v1.9.0')).not.toBeInTheDocument())
    expect(screen.getByText('v1.8.0')).toBeInTheDocument()
    expect(api.listCache).toHaveBeenCalledTimes(1)
  })

  it('the In cycle segmented filter re-queries the server with state', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 1, version: 'v1.9.0' })])
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(segmented('In cycle'))
    await waitFor(() => expect(api.listCache).toHaveBeenCalledWith({ state: 'in-cycle' }))
  })

  it('the summary strip stays whole-cache while a server filter is active', async () => {
    // Unfiltered snapshot drives the strip; the filtered list drives the table.
    vi.mocked(api.listCache).mockImplementation(async (f) =>
      f?.state === 'in-cycle'
        ? [entry({ id: 1, version: 'v1.9.0', state: 'in-cycle' })]
        : [
            entry({ id: 1, version: 'v1.9.0', state: 'in-cycle' }),
            entry({ id: 2, version: 'v1.8.0', state: 'archived', inWindow: false }),
          ],
    )
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(segmented('In cycle'))
    await waitFor(() => expect(screen.queryByText('v1.8.0')).not.toBeInTheDocument())
    // Archived count is still 1 — the strip summarizes the whole cache, not the selection.
    const archived = screen.getByTestId('summary-archived')
    expect(within(archived).getByText('1')).toBeInTheDocument()
  })

  it('bulk re-verify fans out over selected rows', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, version: 'v1.9.0' }),
      entry({ id: 2, version: 'v1.8.0', inWindow: false, state: 'archived' }),
    ])
    vi.mocked(api.reverifyCacheEntry).mockResolvedValue(undefined)
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    const checkboxes = screen.getAllByRole('checkbox')
    await userEvent.click(checkboxes[0])
    await userEvent.click(checkboxes[1])
    await userEvent.click(screen.getByRole('button', { name: 'Re-verify all' }))
    await waitFor(() => expect(api.reverifyCacheEntry).toHaveBeenCalledTimes(2))
  })

  // AntD's message toasts mount into document.body (outside the render container)
  // and aren't torn down between tests, so asserting on rendered toast DOM leaks
  // across tests. Spying on the real `message` API — the same singleton the
  // component imports from 'antd' — observes the actual call the component makes
  // without that pollution. Each spy is restored so it doesn't leak into later tests.
  it('bulk action reports an error (not success) when every fanned-out call rejects', async () => {
    const successSpy = vi.spyOn(message, 'success')
    const errorSpy = vi.spyOn(message, 'error')
    try {
      vi.mocked(api.listCache).mockResolvedValue([
        entry({ id: 1, version: 'v1.9.0' }),
        entry({ id: 2, version: 'v1.8.0', inWindow: false, state: 'archived' }),
      ])
      vi.mocked(api.reverifyCacheEntry).mockRejectedValue(new Error('reverify failed'))
      render(<CacheView />)
      await waitFor(() => screen.getByText('v1.9.0'))
      const checkboxes = screen.getAllByRole('checkbox')
      await userEvent.click(checkboxes[0])
      await userEvent.click(checkboxes[1])
      await userEvent.click(screen.getByRole('button', { name: 'Re-verify all' }))
      await waitFor(() => expect(api.reverifyCacheEntry).toHaveBeenCalledTimes(2))
      // A total failure must surface as an error toast, never the success message.
      await waitFor(() => expect(errorSpy).toHaveBeenCalledWith(expect.stringMatching(/failed/i)))
      expect(successSpy).not.toHaveBeenCalled()
    } finally {
      successSpy.mockRestore()
      errorSpy.mockRestore()
    }
  })

  it('bulk action reports a partial failure when some fanned-out calls reject', async () => {
    const successSpy = vi.spyOn(message, 'success')
    const warningSpy = vi.spyOn(message, 'warning')
    try {
      vi.mocked(api.listCache).mockResolvedValue([
        entry({ id: 1, version: 'v1.9.0' }),
        entry({ id: 2, version: 'v1.8.0', inWindow: false, state: 'archived' }),
      ])
      vi.mocked(api.reverifyCacheEntry).mockImplementation((id) =>
        id === 1 ? Promise.resolve(undefined) : Promise.reject(new Error('reverify failed')),
      )
      render(<CacheView />)
      await waitFor(() => screen.getByText('v1.9.0'))
      const checkboxes = screen.getAllByRole('checkbox')
      await userEvent.click(checkboxes[0])
      await userEvent.click(checkboxes[1])
      await userEvent.click(screen.getByRole('button', { name: 'Re-verify all' }))
      await waitFor(() => expect(api.reverifyCacheEntry).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(warningSpy).toHaveBeenCalledWith(expect.stringMatching(/1 succeeded, 1 failed/i)))
      expect(successSpy).not.toHaveBeenCalled()
    } finally {
      successSpy.mockRestore()
      warningSpy.mockRestore()
    }
  })

  it('guards against double-firing a bulk fan-out while one is already in flight', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 1, version: 'v1.9.0' })])
    let resolveReverify: (() => void) | undefined
    vi.mocked(api.reverifyCacheEntry).mockReturnValue(
      new Promise((resolve) => {
        resolveReverify = () => resolve(undefined)
      }),
    )
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(screen.getAllByRole('checkbox')[0])
    const button = screen.getByRole('button', { name: 'Re-verify all' })
    await userEvent.click(button)
    await waitFor(() => expect(button).toBeDisabled())
    // A second click while the first fan-out is still in flight must not fire again.
    await userEvent.click(button)
    expect(api.reverifyCacheEntry).toHaveBeenCalledTimes(1)
    resolveReverify?.()
    await waitFor(() => expect(api.listCache).toHaveBeenCalledTimes(2))
  })

  it('scan calls scanCache', async () => {
    vi.mocked(api.listCache).mockResolvedValue([])
    vi.mocked(api.scanCache).mockResolvedValue({ scanned: 0, updated: 0, orphans: 0 })
    render(<CacheView />)
    await waitFor(() => expect(api.listCache).toHaveBeenCalled())
    await userEvent.click(screen.getByRole('button', { name: /Scan/ }))
    expect(api.scanCache).toHaveBeenCalled()
  })
})
