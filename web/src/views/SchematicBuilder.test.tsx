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

const renderBuilder = (props: Partial<ComponentProps<typeof SchematicBuilder>> = {}) =>
  render(
    <MemoryRouter>
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
    renderBuilder()
    await userEvent.type(screen.getByLabelText('Name'), 'rpi')
    await userEvent.click(screen.getByRole('button', { name: /Advanced/ }))
    await userEvent.type(screen.getByLabelText('Overlay name (SBCs)'), 'rpi_generic')
    await userEvent.type(screen.getByLabelText('Overlay image'), 'siderolabs/sbc-raspberrypi')
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
})
