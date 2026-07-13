import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Alert, Button, Collapse, Form, Input, Segmented, Select, Space, Tabs, Typography, message, theme,
} from 'antd'
import { ArrowLeftOutlined, CopyOutlined } from '@ant-design/icons'
import type { Config } from '../api/configs'
import { createConfig, getConfig, updateConfig } from '../api/configs'
import { buildCustomization, parseCustomization } from '../api/schematicYaml'
import { parseBuilderParams, serializeBuilderParams } from '../api/schematicUrl'
import { EXTENSION_CATALOGUE } from '../api/schematicCatalogue'

const HW_OPTIONS = ['metal', 'cloud', 'sbc']
const ARCH_OPTIONS = ['amd64', 'arm64']

export default function SchematicBuilder({
  config,
  onBack,
  onSaved,
}: {
  config: Config | null
  onBack: () => void
  // "The list is stale, refresh it" — this must NOT navigate away from the
  // builder, or the derived-ID Alert below never paints (SGE B6).
  onSaved: () => void
}) {
  const { token } = theme.useToken()
  const [searchParams, setSearchParams] = useSearchParams()
  // Read the deep-link ONCE on mount; after that this component owns the URL.
  const initial = useMemo(() => parseBuilderParams(searchParams), []) // eslint-disable-line react-hooks/exhaustive-deps

  const [name, setName] = useState(config?.name ?? '')
  const [hw, setHw] = useState(initial.hw ?? 'metal')
  const [arch, setArch] = useState(initial.arch ?? 'amd64')
  const [version, setVersion] = useState(initial.version ?? '')
  const [extensions, setExtensions] = useState<string[]>(initial.ext)
  const [overlayName, setOverlayName] = useState('')
  const [overlayImage, setOverlayImage] = useState('')
  const [rawSource, setRawSource] = useState<string | null>(null) // non-null = outside the generated subset
  const [savedId, setSavedId] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [overlayError, setOverlayError] = useState<string | null>(null)
  // Adopted after a successful create so a SECOND Generate updates the same
  // record instead of creating a duplicate schematic (SGE review Important 1).
  const [activeConfig, setActiveConfig] = useState<Config | null>(config)

  // Edit mode: seed the form from the stored source. A source outside the
  // generated subset (hand-authored, or the legacy nested-overlay shape after
  // Task 6) parses to null -> show it read-only rather than destroy it.
  useEffect(() => {
    if (!config) return
    let cancelled = false
    getConfig(config.id)
      .then((detail) => {
        if (cancelled || !detail) return
        const f = parseCustomization(detail.source)
        if (f) {
          setExtensions(f.extensions)
          setOverlayName(f.overlayName ?? '')
          setOverlayImage(f.overlayImage ?? '')
          setRawSource(null)
        } else {
          setRawSource(detail.source)
        }
      })
      .catch((e) => message.error(e instanceof Error ? e.message : 'failed to load schematic'))
    return () => {
      cancelled = true
    }
  }, [config])

  // Live customization YAML the client would POST. hw/arch/version are builder
  // CONTEXT (image path params) and are NOT part of the customization body
  // (SGE C2) — they live only in the deep-link URL.
  const yaml = useMemo(
    () =>
      buildCustomization({
        extensions,
        overlayName: overlayName || undefined,
        overlayImage: overlayImage || undefined,
      }),
    [extensions, overlayName, overlayImage],
  )

  // A saved derived ID is only valid for the exact body it was computed from;
  // any subsequent edit to that body invalidates it so the success Alert never
  // shows a stale ID next to a changed live preview (SGE review Important 2).
  useEffect(() => {
    setSavedId(null)
  }, [extensions, overlayName, overlayImage])

  // Deep-link sync in an EFFECT keyed on the state (SGE B7). Calling a sync
  // helper from each handler would serialize the PRE-update closure values, so
  // the URL would be permanently one interaction stale.
  useEffect(() => {
    setSearchParams(
      serializeBuilderParams({ hw, arch, version: version || undefined, ext: extensions }),
      { replace: true },
    )
  }, [hw, arch, version, extensions, setSearchParams])

  // Both-or-neither: buildCustomization emits the overlay ONLY when both fields
  // are set, so a half-filled pair would silently produce an overlay-less
  // schematic (SGE I2). Block the save instead.
  const overlayIncomplete = !!overlayName !== !!overlayImage

  const save = async () => {
    if (!name.trim()) {
      message.error('Name is required')
      return
    }
    if (overlayIncomplete) {
      setOverlayError('Overlay requires both a name and an image')
      return
    }
    setOverlayError(null)
    setSaving(true)
    try {
      const result = activeConfig
        ? await updateConfig(activeConfig.id, yaml)
        : await createConfig({ name, kind: 'schematic', source: yaml })
      setSavedId(result?.derivedSchematicId ?? null)
      // Adopt the created record so the NEXT save updates it instead of
      // calling createConfig again with the same name (SGE review Important 1).
      if (result && !activeConfig) setActiveConfig(result)
      message.success(activeConfig ? `Rebuilt ${name}` : `Built ${name}`)
      onSaved() // refresh the list; we STAY here so the Alert below renders
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'schematic build failed')
    } finally {
      setSaving(false)
    }
  }

  const builderForm = (
    <Form layout="vertical">
      <Form.Item label="Name" htmlFor="schematic-name" required>
        <Input id="schematic-name" value={name} onChange={(e) => setName(e.target.value)} disabled={!!activeConfig} />
      </Form.Item>
      <Form.Item label="Hardware type">
        <Segmented value={hw} onChange={(v) => setHw(v as string)} options={HW_OPTIONS} />
      </Form.Item>
      <Space size="large">
        <Form.Item label="Talos version" htmlFor="schematic-version">
          <Input id="schematic-version" placeholder="v1.9.0" value={version} onChange={(e) => setVersion(e.target.value)} />
        </Form.Item>
        <Form.Item label="Architecture" htmlFor="schematic-arch">
          <Select
            id="schematic-arch"
            style={{ width: 140 }}
            value={arch}
            onChange={setArch}
            options={ARCH_OPTIONS.map((a) => ({ value: a, label: a }))}
          />
        </Form.Item>
      </Space>
      <Form.Item label="System extensions" htmlFor="schematic-extensions">
        <Select
          id="schematic-extensions"
          mode="tags"
          value={extensions}
          onChange={setExtensions}
          tokenSeparators={[',', ' ']}
          placeholder="siderolabs/…"
          options={EXTENSION_CATALOGUE.map((c) => ({ value: c.name, label: `${c.name} — ${c.description}` }))}
        />
      </Form.Item>
      <Collapse
        ghost
        items={[
          {
            key: 'advanced',
            label: 'Advanced',
            children: (
              <Space direction="vertical" style={{ width: '100%' }}>
                <Form.Item
                  label="Overlay name (SBCs)"
                  htmlFor="schematic-overlay-name"
                  validateStatus={overlayError && !overlayName ? 'error' : undefined}
                >
                  <Input
                    id="schematic-overlay-name"
                    placeholder="rpi_generic"
                    value={overlayName}
                    onChange={(e) => setOverlayName(e.target.value)}
                  />
                </Form.Item>
                <Form.Item
                  label="Overlay image"
                  htmlFor="schematic-overlay-image"
                  validateStatus={overlayError && !overlayImage ? 'error' : undefined}
                  help={overlayError ?? undefined}
                >
                  <Input
                    id="schematic-overlay-image"
                    placeholder="siderolabs/sbc-raspberrypi"
                    value={overlayImage}
                    onChange={(e) => setOverlayImage(e.target.value)}
                  />
                </Form.Item>
              </Space>
            ),
          },
        ]}
      />
    </Form>
  )

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Space>
        <Button icon={<ArrowLeftOutlined />} onClick={onBack}>Schematics</Button>
        <Typography.Title level={4} style={{ margin: 0 }}>{activeConfig ? activeConfig.name : 'New schematic'}</Typography.Title>
      </Space>

      {savedId && (
        <Alert
          type="success"
          showIcon
          message={
            <Space>
              <span>Derived schematic ID: <Typography.Text code>{savedId}</Typography.Text></span>
              <Button
                size="small"
                type="text"
                icon={<CopyOutlined />}
                onClick={() => {
                  if (!navigator.clipboard) {
                    message.error('Clipboard unavailable')
                    return
                  }
                  navigator.clipboard.writeText(savedId).then(
                    () => message.success('Copied'),
                    () => message.error('Copy failed'),
                  )
                }}
              />
            </Space>
          }
        />
      )}

      {rawSource !== null && (
        <Alert
          type="info"
          showIcon
          message="This schematic's source is not in the generated form; it is shown read-only and cannot be rebuilt from the builder."
        />
      )}

      <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <Tabs
            items={[
              { key: 'builder', label: 'Builder', children: rawSource !== null ? <Input.TextArea readOnly rows={16} value={rawSource} /> : builderForm },
              { key: 'raw', label: 'Raw YAML', children: <Input.TextArea readOnly rows={16} value={rawSource ?? yaml} /> },
            ]}
          />
        </div>
        <div style={{ flex: 1, minWidth: 280 }}>
          <Typography.Text strong>Live customization</Typography.Text>
          <pre
            style={{
              background: token.colorFillTertiary,
              padding: token.paddingSM,
              borderRadius: token.borderRadiusLG,
              overflowX: 'auto',
            }}
          >
            {rawSource ?? yaml}
          </pre>
        </div>
      </div>

      <Space>
        <Button type="primary" loading={saving} disabled={rawSource !== null} onClick={save}>
          {activeConfig ? 'Save' : 'Generate'}
        </Button>
        <Button onClick={onBack}>Cancel</Button>
      </Space>
    </Space>
  )
}
