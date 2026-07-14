import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { message } from 'antd'
import type { CacheEntry } from '../api/cache'
import CacheView from './CacheView'
import * as api from '../api/cache'
import * as configsApi from '../api/configs'

vi.mock('../api/cache')
vi.mock('../api/configs')

beforeEach(() => {
  // Every test needs a schematic catalogue: CacheView cross-references it to name
  // its groups. Default to empty; individual tests override.
  vi.mocked(configsApi.listConfigs).mockResolvedValue([])
})

const entry = (o: Partial<CacheEntry>): CacheEntry => ({
  id: 1, os: 'talos', arch: 'amd64', version: 'v1.0.0', size: 1024,
  state: 'in-cycle', pinned: false, inWindow: true, fetchedAt: '', ...o,
})

afterEach(() => {
  vi.resetAllMocks()
  // AntD's message toasts mount into document.body OUTSIDE the render container
  // (see the note above the `segmented` helper) and RTL's cleanup doesn't reach
  // them, so a toast from one test can leak into the next test's DOM. This
  // matters here specifically because the scan test's "...0 orphans" toast would
  // otherwise satisfy a later /orphan/i assertion for the wrong reason.
  message.destroy()
})

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

  it('the Pinned segmented filter re-queries the server with pinned=true', async () => {
    vi.mocked(api.listCache).mockResolvedValue([entry({ id: 1, version: 'v1.9.0', pinned: true })])
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))
    await userEvent.click(segmented('Pinned'))
    await waitFor(() => expect(api.listCache).toHaveBeenCalledWith({ pinned: true }))
  })

  it('clears stale row/detail selections when an OS filter change hides the selected entries', async () => {
    // Regression: switching filters must not leave selectedRows/selectedId
    // pointing at entries the user can no longer see — otherwise the bulk
    // buttons stay enabled and act on invisible rows, and the detail pane
    // silently shows "No selection" while selectedId is still set underneath
    // (final-review Minor 2 / Minor 3).
    vi.mocked(api.listCache).mockImplementation(async (f) =>
      f?.os === 'flatcar'
        ? [entry({ id: 3, os: 'flatcar', version: 'v1.0.0' })]
        : [
            entry({ id: 1, os: 'talos', version: 'v1.9.0' }),
            entry({ id: 2, os: 'talos', version: 'v1.8.0', inWindow: false, state: 'archived' }),
            entry({ id: 3, os: 'flatcar', version: 'v1.0.0' }),
          ],
    )
    render(<CacheView />)
    await waitFor(() => screen.getByText('v1.9.0'))

    // Capture the row references BEFORE selecting one — once the detail pane
    // shows a version, its Card title duplicates the row's text, making a plain
    // getByText ambiguous.
    const row1 = screen.getByText('v1.9.0').closest('tr')!
    const row2 = screen.getByText('v1.8.0').closest('tr')!

    await userEvent.click(within(row1).getByText('v1.9.0'))
    expect(within(screen.getByTestId('cache-detail')).getByText('v1.9.0')).toBeInTheDocument()

    await userEvent.click(within(row1).getByRole('checkbox'))
    await userEvent.click(within(row2).getByRole('checkbox'))
    expect(screen.getByRole('button', { name: 'Pin all' })).toBeEnabled()

    // Switch the OS filter to flatcar — the talos rows (and the selection state
    // they carried) vanish from view.
    await userEvent.click(screen.getByRole('combobox'))
    await waitFor(() => expect(document.querySelector('.ant-select-item-option-content')).toBeInTheDocument())
    await userEvent.click(await screen.findByText('flatcar', { selector: '.ant-select-item-option-content' }))

    await waitFor(() => expect(screen.queryByText('v1.9.0')).not.toBeInTheDocument())
    // Bulk buttons must not stay enabled on rows the user can no longer see.
    expect(screen.getByRole('button', { name: 'Pin all' })).toBeDisabled()
    // The detail pane must not keep pointing at a now-hidden row.
    expect(screen.getByTestId('cache-detail')).toHaveTextContent('No selection')
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

  it('names a schematic group after the live schematic it matches', async () => {
    const id = `43fac7${'0'.repeat(54)}1367`
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', params: { schematic: id }, version: 'v1.9.0' }),
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 9, name: 'rpi4-tailscale', kind: 'schematic', activeRevision: 1, revisionCount: 1, derivedSchematicId: id, updatedAt: '' },
    ])
    render(<CacheView />)
    expect(await screen.findByText('talos · rpi4-tailscale')).toBeInTheDocument()
    expect(screen.getByText('schematic 43fac7…1367')).toBeInTheDocument()
  })

  it('names the predefined default target after the seeded vanilla schematic', async () => {
    // The predefined Talos target (pkg/cache/seed.go:53) carries the constant
    // DefaultTalosSchematic id, and SeedVanillaSchematic seeds a matching
    // kind=schematic config named "vanilla" at startup — so it names itself.
    const vanilla = '376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba'
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', params: { schematic: vanilla }, version: 'v1.8.0' }),
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([
      { id: 1, name: 'vanilla', kind: 'schematic', activeRevision: 1, revisionCount: 1, derivedSchematicId: vanilla, updatedAt: '' },
    ])
    render(<CacheView />)
    expect(await screen.findByText('talos · vanilla')).toBeInTheDocument()
  })

  it('makes NO claim about a schematic target it cannot match to a config', async () => {
    // An unmatched target may be stranded, OR it may be a host-bound raw ID that a
    // host is booting right now (Import-by-ID creates exactly this, with no
    // config). CacheEntryDTO carries no provenance, so we cannot tell — and must
    // not imply the images are disposable. Show the id, claim nothing.
    const id = `9f21ab${'0'.repeat(54)}7c40`
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', params: { schematic: id }, version: 'v1.8.0' }),
    ])
    vi.mocked(configsApi.listConfigs).mockResolvedValue([])
    render(<CacheView />)
    expect(await screen.findByText('talos · 9f21ab…7c40')).toBeInTheDocument()
    expect(screen.queryByText(/not referenced/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/orphan/i)).not.toBeInTheDocument()
  })

  it('keeps two distinct schematics in separate groups', async () => {
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', params: { schematic: 'aaa' }, version: 'v1.9.0' }),
      entry({ id: 2, os: 'talos', params: { schematic: 'bbb' }, version: 'v1.9.0' }),
    ])
    render(<CacheView />)
    await waitFor(() => expect(screen.getByText('talos · aaa')).toBeInTheDocument())
    expect(screen.getByText('talos · bbb')).toBeInTheDocument()
  })

  it('still renders the cache when the schematic catalogue fails to load', async () => {
    // Labels degrade to bare short IDs; the cache list itself must keep working.
    const id = `9f21ab${'0'.repeat(54)}7c40`
    vi.mocked(api.listCache).mockResolvedValue([
      entry({ id: 1, os: 'talos', params: { schematic: id }, version: 'v1.8.0' }),
    ])
    vi.mocked(configsApi.listConfigs).mockRejectedValue(new Error('boom'))
    render(<CacheView />)
    expect(await screen.findByText('talos · 9f21ab…7c40')).toBeInTheDocument()
    expect(screen.getByText('v1.8.0')).toBeInTheDocument()
  })
})
