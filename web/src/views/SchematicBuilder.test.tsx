import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router-dom'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import SchematicBuilder from './SchematicBuilder'
import * as configs from '../api/configs'

vi.mock('../api/configs')

// Probe that renders the current query string so the deep-link sync is assertable
// (SGE B7: the first draft had NO url-sync test, which is how the stale-closure
// bug survived).
function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="search">{loc.search}</div>
}

const renderBuilder = (
  props: Partial<ComponentProps<typeof SchematicBuilder>> = {},
  initialEntries: string[] = ['/'],
) =>
  render(
    <MemoryRouter initialEntries={initialEntries}>
      <SchematicBuilder config={null} onBack={() => {}} onSaved={() => {}} {...props} />
      <LocationProbe />
    </MemoryRouter>,
  )

afterEach(() => vi.resetAllMocks())

describe('SchematicBuilder', () => {
  it('shows a live YAML pane reflecting the name-independent customization', async () => {
    renderBuilder()
    // Empty builder -> vanilla customization in the live pane.
    await waitFor(() => expect(screen.getByText(/customization: \{\}/)).toBeInTheDocument())
  })

  it('saves a new schematic via createConfig and STAYS on the builder showing the derived id', async () => {
    const onSaved = vi.fn()
    vi.mocked(configs.createConfig).mockResolvedValue({
      id: 9, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abcdef012345', updatedAt: '',
    })
    renderBuilder({ onSaved })
    await userEvent.type(screen.getByLabelText('Name'), 'iscsi')
    await userEvent.click(screen.getByRole('button', { name: 'Generate' }))
    await waitFor(() => expect(configs.createConfig).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'iscsi', kind: 'schematic', source: 'customization: {}\n' }),
    ))
    // The Alert is reachable because the builder is still mounted (SGE B6).
    expect(await screen.findByText(/abcdef012345/)).toBeInTheDocument()
    // onSaved is a "refresh the list" signal only — it must NOT unmount the builder.
    expect(onSaved).toHaveBeenCalled()
    expect(screen.getByLabelText('Name')).toBeInTheDocument()
  })

  it('adopts the newly-created config so a second Generate calls updateConfig, not a second createConfig', async () => {
    // Regression for the duplicate-create bug: after a successful create the
    // component stays mounted with `config` still null. Without adopting the
    // created record locally, editing and saving again would call createConfig
    // a second time with the same name, producing a second schematic record.
    vi.mocked(configs.createConfig).mockResolvedValue({
      id: 9, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abcdef012345', updatedAt: '',
    })
    vi.mocked(configs.updateConfig).mockResolvedValue({
      id: 9, name: 'iscsi', kind: 'schematic', activeRevision: 2, revisionCount: 2,
      derivedSchematicId: 'newlyderivedid', updatedAt: '',
    })
    renderBuilder()
    await userEvent.type(screen.getByLabelText('Name'), 'iscsi')
    await userEvent.click(screen.getByRole('button', { name: 'Generate' }))
    await waitFor(() => expect(configs.createConfig).toHaveBeenCalledTimes(1))
    // The button label now reflects the adopted (saved) record.
    expect(await screen.findByRole('button', { name: 'Save' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.type(screen.getByLabelText('Overlay name (SBCs)'), 'rpi_generic')
    await userEvent.type(screen.getByLabelText('Overlay image'), 'siderolabs/sbc-raspberrypi')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(configs.updateConfig).toHaveBeenCalledWith(
      9,
      'customization: {}\noverlay:\n  name: rpi_generic\n  image: siderolabs/sbc-raspberrypi\n',
    ))
    expect(configs.createConfig).toHaveBeenCalledTimes(1)
  })

  it('clears the stale derived-ID alert once the form is edited after a save', async () => {
    // The Alert must never show an ID for a customization body that no longer
    // matches the live preview — a stale ID could name the wrong image.
    vi.mocked(configs.createConfig).mockResolvedValue({
      id: 9, name: 'iscsi', kind: 'schematic', activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abcdef012345', updatedAt: '',
    })
    renderBuilder()
    await userEvent.type(screen.getByLabelText('Name'), 'iscsi')
    await userEvent.click(screen.getByRole('button', { name: 'Generate' }))
    expect(await screen.findByText(/abcdef012345/)).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.type(screen.getByLabelText('Overlay name (SBCs)'), 'r')
    await waitFor(() => expect(screen.queryByText(/abcdef012345/)).not.toBeInTheDocument())
  })

  it('hydrates the form from deep-link URL params on mount', async () => {
    renderBuilder({}, ['/?hw=sbc&arch=arm64&version=v1.9.0&ext=siderolabs%2Ftailscale'])
    await waitFor(() => expect(screen.getByRole('radio', { name: 'sbc' })).toBeChecked())
    expect(screen.getByLabelText('Talos version')).toHaveValue('v1.9.0')
    expect(screen.getByText('arm64')).toBeInTheDocument()
    expect(await screen.findByText(/siderolabs\/tailscale — /)).toBeInTheDocument()
  })

  it('syncs the builder state into the URL query string (deep-link)', async () => {
    renderBuilder()
    // The radio input itself is visually hidden (pointer-events: none by design,
    // see rc-segmented) — a real user clicks the visible label, so we do too.
    const radio = screen.getByRole('radio', { name: 'sbc' })
    await userEvent.click(radio.closest('label')!)
    // Must reflect the CURRENT value, not the previous one (SGE B7 stale-closure bug).
    await waitFor(() => expect(screen.getByTestId('search').textContent).toContain('hw=sbc'))
    expect(screen.getByTestId('search').textContent).not.toContain('kargs')
    expect(screen.getByTestId('search').textContent).not.toContain('secureboot')
  })

  it('rejects a half-filled overlay (both-or-neither) and does not save', async () => {
    renderBuilder()
    await userEvent.type(screen.getByLabelText('Name'), 'rpi')
    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.type(screen.getByLabelText('Overlay name (SBCs)'), 'rpi_generic')
    // image intentionally left blank
    await userEvent.click(screen.getByRole('button', { name: 'Generate' }))
    expect(await screen.findByText(/Overlay requires both a name and an image/)).toBeInTheDocument()
    expect(configs.createConfig).not.toHaveBeenCalled()
  })

  it('builds a full overlay into the customization when both fields are set', async () => {
    vi.mocked(configs.createConfig).mockResolvedValue({
      id: 9, name: 'rpi', kind: 'schematic', activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'aaa', updatedAt: '',
    })
    const { container } = renderBuilder()
    await userEvent.type(screen.getByLabelText('Name'), 'rpi')
    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.type(screen.getByLabelText('Overlay name (SBCs)'), 'rpi_generic')
    await userEvent.type(screen.getByLabelText('Overlay image'), 'siderolabs/sbc-raspberrypi')
    // Assert the actual rendered live-preview pane text — not just the createConfig
    // argument — so a rendering-only regression (e.g. the pane going stale, or
    // showing the overlay nested instead of top-level) would be caught here too.
    await waitFor(() => expect(container.querySelector('pre')?.textContent).toBe(
      'customization: {}\noverlay:\n  name: rpi_generic\n  image: siderolabs/sbc-raspberrypi\n',
    ))
    await userEvent.click(screen.getByRole('button', { name: 'Generate' }))
    // Overlay is a TOP-LEVEL sibling of customization (Task 6).
    await waitFor(() => expect(configs.createConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        source: 'customization: {}\noverlay:\n  name: rpi_generic\n  image: siderolabs/sbc-raspberrypi\n',
      }),
    ))
  })

  it('Back invokes onBack', async () => {
    const onBack = vi.fn()
    renderBuilder({ onBack })
    await userEvent.click(screen.getByRole('button', { name: /Schematics/ }))
    expect(onBack).toHaveBeenCalled()
  })

  it('renders no kernel-args field (D5) and no secureboot toggle (deferred)', async () => {
    renderBuilder()
    expect(screen.queryByLabelText(/kernel arg/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/secure ?boot/i)).not.toBeInTheDocument()
  })

  it('edit mode prefills from the stored source and Save calls updateConfig (not createConfig)', async () => {
    const cfg = {
      id: 5, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abc', updatedAt: '',
    }
    vi.mocked(configs.getConfig).mockResolvedValue({
      ...cfg,
      source: 'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n',
    })
    vi.mocked(configs.updateConfig).mockResolvedValue({ ...cfg, derivedSchematicId: 'updated123' })
    renderBuilder({ config: cfg })
    // Match the Select's rendered tag specifically (catalogue label form "name — description"),
    // not the raw YAML also present in the Raw YAML pane.
    expect(await screen.findByText(/siderolabs\/iscsi-tools — /)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(configs.updateConfig).toHaveBeenCalledWith(
      5,
      'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n',
    ))
    expect(configs.createConfig).not.toHaveBeenCalled()
  })

  it('disables Save while the edit-mode detail fetch is still pending', async () => {
    const cfg = {
      id: 5, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abc', updatedAt: '',
    }
    vi.mocked(configs.getConfig).mockReturnValue(new Promise(() => {})) // never settles
    renderBuilder({ config: cfg })
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(configs.updateConfig).not.toHaveBeenCalled()
  })

  it('disables Save and never calls updateConfig when the edit-mode detail fetch rejects', async () => {
    // Without this gate, a failed load left extensions/overlay at their initial
    // (empty) state while Save stayed enabled — clicking it would silently
    // overwrite the real config with a vanilla body (final-review Critical).
    const cfg = {
      id: 5, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abc', updatedAt: '',
    }
    vi.mocked(configs.getConfig).mockRejectedValue(new Error('network blip'))
    renderBuilder({ config: cfg })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled())
    expect(configs.updateConfig).not.toHaveBeenCalled()
  })

  it('edit mode ignores a stale ?ext= URL param: the loaded config wins and the URL value never reaches the saved body', async () => {
    const cfg = {
      id: 5, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abc', updatedAt: '',
    }
    vi.mocked(configs.getConfig).mockResolvedValue({
      ...cfg,
      source: 'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n',
    })
    vi.mocked(configs.updateConfig).mockResolvedValue({ ...cfg, derivedSchematicId: 'updated123' })
    renderBuilder({ config: cfg }, ['/?ext=siderolabs%2Ftailscale'])

    // Synchronously, before the getConfig() microtask has resolved: old code
    // seeded `extensions` from the URL unconditionally on mount, so the stale
    // tailscale tag would already be painted on the very first render, before
    // the real config data ever arrives. This is the window where a fast Save
    // click could leak the URL's extensions into the saved body.
    expect(screen.queryByText(/siderolabs\/tailscale — /)).not.toBeInTheDocument()

    expect(await screen.findByText(/siderolabs\/iscsi-tools — /)).toBeInTheDocument()
    expect(screen.queryByText(/siderolabs\/tailscale — /)).not.toBeInTheDocument()

    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled())
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(configs.updateConfig).toHaveBeenCalledWith(
      5,
      'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n',
    ))
  })

  it('surfaces a load error and keeps Save disabled when the edit-mode detail fetch resolves with an empty body', async () => {
    // request() resolves an empty response body as `undefined` -- this must
    // NOT be silently treated like "cancelled" (component unmounted / a newer
    // load superseded this one). It's a real failure and must surface the
    // same way a rejected fetch does, or Save is left dead with no
    // explanation at all (final-review Minor finding).
    const cfg = {
      id: 5, name: 'iscsi', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'abc', updatedAt: '',
    }
    vi.mocked(configs.getConfig).mockResolvedValue(undefined)
    renderBuilder({ config: cfg })
    expect(await screen.findByText(/Failed to load this schematic's stored source/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(configs.updateConfig).not.toHaveBeenCalled()
    expect(configs.createConfig).not.toHaveBeenCalled()
  })

  it('edit mode falls back to read-only raw source for a legacy shape that does not round-trip', async () => {
    // The legacy nested-overlay shape (overlay UNDER customization, pre-Task-6)
    // does not round-trip through parseCustomization/buildCustomization — this
    // is the safety net that stops a legacy schematic from being silently
    // clobbered, so it must show read-only and block Save.
    const cfg = {
      id: 7, name: 'legacy', kind: 'schematic' as const, activeRevision: 1, revisionCount: 1,
      derivedSchematicId: 'legacyid', updatedAt: '',
    }
    const legacySource =
      'customization:\n  systemExtensions:\n    officialExtensions:\n      - siderolabs/iscsi-tools\n' +
      '  overlay:\n    name: rpi_generic\n    image: siderolabs/sbc-raspberrypi\n'
    vi.mocked(configs.getConfig).mockResolvedValue({ ...cfg, source: legacySource })
    renderBuilder({ config: cfg })

    // Default normalizer collapses whitespace (incl. newlines) — disable it so
    // this multi-line value is matched byte-for-byte, not just "similar".
    const textareas = await screen.findAllByDisplayValue(legacySource, { normalizer: (s) => s })
    expect(textareas.length).toBeGreaterThan(0)
    for (const ta of textareas) expect(ta).toHaveAttribute('readonly')

    expect(await screen.findByText(/is not in the generated form/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(configs.createConfig).not.toHaveBeenCalled()
    expect(configs.updateConfig).not.toHaveBeenCalled()
  })
})
