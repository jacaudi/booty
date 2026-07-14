import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Form, Input, Modal, Select, Space, Table, Tag, Tooltip, Typography, message } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import type { Config } from '../api/configs'
import { getConfig, listConfigs } from '../api/configs'
import { parseCustomization } from '../api/schematicYaml'
import { bindSchematic, listHosts } from '../api/client'
import type { Host } from '../api/types'
import { SCHEMATIC_KIND } from '../api/configKinds'
import { shortSchematicId } from '../api/schematicId'
import SchematicBuilder from './SchematicBuilder'

function shortId(id?: string): string {
  return id ? shortSchematicId(id) : 'Not built'
}

type Mode = { screen: 'list' } | { screen: 'builder'; config: Config | null }

export default function SchematicsView() {
  const [mode, setMode] = useState<Mode>({ screen: 'list' })
  const [schematics, setSchematics] = useState<Config[]>([])
  const [sources, setSources] = useState<Record<number, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [hosts, setHosts] = useState<Host[]>([])
  const [importForm] = Form.useForm()

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const sch = (await listConfigs()).filter((c) => c.kind === SCHEMATIC_KIND)
      setSchematics(sch)
      const details = await Promise.all(sch.map((c) => getConfig(c.id)))
      const bySrc: Record<number, string> = {}
      for (const d of details) if (d) bySrc[d.id] = d.source
      setSources(bySrc)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load schematics')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (mode.screen === 'list') load()
  }, [load, mode.screen])

  const openImport = async () => {
    importForm.resetFields()
    setImportOpen(true)
    try {
      setHosts((await listHosts()).filter((h) => h.os === 'talos'))
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load hosts')
    }
  }

  const submitImport = async () => {
    // AntD renders per-field errors on rejection (e.g. the 64-hex-char rule);
    // nothing else to surface here.
    const v = await importForm.validateFields().catch(() => null)
    if (!v) return
    try {
      await bindSchematic(v.mac, { schematic: v.schematic })
      message.success(`Bound ${shortId(v.schematic)} to ${v.mac}`)
      setImportOpen(false)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'bind failed')
    }
  }

  if (mode.screen === 'builder') {
    return (
      <SchematicBuilder
        config={mode.config}
        onBack={() => setMode({ screen: 'list' })}
        // Refresh the background list only — do NOT switch screens here, or the
        // builder unmounts before its derived-ID success Alert paints (SGE B6).
        onSaved={load}
      />
    )
  }

  const columns: ColumnsType<Config> = [
    {
      title: 'Name',
      key: 'name',
      render: (_, c) => <a onClick={() => setMode({ screen: 'builder', config: c })}>{c.name}</a>,
    },
    {
      title: 'Extensions',
      key: 'ext',
      render: (_, c) => {
        const f = parseCustomization(sources[c.id] ?? '')
        return f ? f.extensions.length : <Tag>custom</Tag>
      },
    },
    {
      title: 'Schematic ID',
      key: 'id',
      render: (_, c) => (
        <Space>
          <Tooltip title={c.derivedSchematicId}>
            <Typography.Text code>{shortId(c.derivedSchematicId)}</Typography.Text>
          </Tooltip>
          {c.derivedSchematicId && (
            <Button
              size="small"
              type="text"
              icon={<CopyOutlined />}
              onClick={() => navigator.clipboard?.writeText(c.derivedSchematicId!)}
            />
          )}
        </Space>
      ),
    },
    { title: 'Updated', dataIndex: 'updatedAt', key: 'updatedAt' },
    {
      title: 'Actions',
      key: 'actions',
      render: () => (
        <Tooltip title="available after authentication (P10)">
          <Button size="small" danger disabled>Delete</Button>
        </Tooltip>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      {error && <Alert type="error" message={error} showIcon />}
      <Space style={{ justifyContent: 'flex-end', width: '100%' }}>
        <Button onClick={openImport}>Import by ID</Button>
        <Button type="primary" onClick={() => setMode({ screen: 'builder', config: null })}>New schematic</Button>
      </Space>
      <Table rowKey="id" loading={loading} columns={columns} dataSource={schematics} pagination={false} />

      <Modal title="Import schematic by ID" open={importOpen} onOk={submitImport} onCancel={() => setImportOpen(false)} okText="Bind" destroyOnHidden>
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="Binds a raw schematic ID directly to a Talos host. It is not added to the catalog and the builder is not pre-populated (needs a Factory proxy — see #32)."
        />
        <Form form={importForm} layout="vertical">
          <Form.Item
            name="schematic"
            label="Schematic ID"
            rules={[
              { required: true },
              // Cheap paste-truncation guard: the server rejects garbage anyway, but a
              // truncated paste deserves an inline message, not a failed bind round-trip.
              { pattern: /^[0-9a-f]{64}$/, message: 'A schematic ID is 64 lowercase hex characters' },
            ]}
          >
            <Input placeholder="sha256 schematic id" />
          </Form.Item>
          <Form.Item name="mac" label="Host" rules={[{ required: true }]}>
            <Select
              placeholder="target Talos host"
              options={hosts.map((h) => ({ value: h.mac, label: `${h.mac} (${h.hostname})` }))}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
