import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Drawer, Form, Input, Modal, Select, Space, Table, Tabs, Tag, Tooltip, Typography, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Config, Preview, Revision } from '../api/configs'
import { createConfig, getConfig, listConfigs, listRevisions, previewConfig, rollbackConfig, updateConfig } from '../api/configs'
import type { Role } from '../api/roles'
import { createRole, listRoles, updateRole } from '../api/roles'
import { buildCustomization, parseCustomization } from '../api/schematicYaml'

const CONFIG_KINDS = ['butane', 'machineconfig', 'preseed'] as const

function ConfigsTab() {
  const [configs, setConfigs] = useState<Config[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [createOpen, setCreateOpen] = useState(false)
  const [createForm] = Form.useForm()

  const [editing, setEditing] = useState<Config | null>(null)
  const [editForm] = Form.useForm()

  const [previewing, setPreviewing] = useState<Config | null>(null)
  const [previewHost, setPreviewHost] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)

  const [revisionsFor, setRevisionsFor] = useState<Config | null>(null)
  const [revisions, setRevisions] = useState<Revision[]>([])
  const [revisionsLoading, setRevisionsLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setConfigs((await listConfigs()).filter((c) => c.kind !== 'schematic'))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load configs')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      message.success(ok)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
  }

  const openCreate = () => {
    createForm.resetFields()
    setCreateOpen(true)
  }

  const submitCreate = async () => {
    const values = await createForm.validateFields()
    await act(() => createConfig(values), `Created ${values.name}`)
    setCreateOpen(false)
  }

  const openEdit = async (c: Config) => {
    try {
      const detail = await getConfig(c.id)
      editForm.setFieldsValue({ source: detail?.source ?? '' })
      setEditing(c)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load config')
    }
  }

  const submitEdit = async () => {
    if (!editing) return
    const values = await editForm.validateFields()
    await act(() => updateConfig(editing.id, values.source), `Updated ${editing.name}`)
    setEditing(null)
  }

  const openPreview = (c: Config) => {
    setPreviewHost('')
    setPreview(null)
    setPreviewing(c)
  }

  const runPreview = async () => {
    if (!previewing) return
    setPreviewLoading(true)
    try {
      const result = await previewConfig(previewing.id, previewHost || undefined)
      setPreview(result ?? null)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'preview failed')
    } finally {
      setPreviewLoading(false)
    }
  }

  const openRevisions = async (c: Config) => {
    setRevisionsFor(c)
    setRevisionsLoading(true)
    try {
      setRevisions(await listRevisions(c.id))
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load revisions')
    } finally {
      setRevisionsLoading(false)
    }
  }

  const doRollback = async (rev: Revision) => {
    if (!revisionsFor) return
    await act(() => rollbackConfig(revisionsFor.id, rev.revision), `Rolled back ${revisionsFor.name} to revision ${rev.revision}`)
    setRevisionsFor(null)
  }

  const columns: ColumnsType<Config> = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Kind', dataIndex: 'kind', key: 'kind', render: (k: string) => <Tag>{k}</Tag> },
    { title: 'Active Rev', dataIndex: 'activeRevision', key: 'activeRevision' },
    { title: 'Updated', dataIndex: 'updatedAt', key: 'updatedAt' },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, c) => (
        <Space>
          <Button size="small" onClick={() => openEdit(c)}>Edit</Button>
          <Button size="small" onClick={() => openPreview(c)}>Preview</Button>
          <Button size="small" onClick={() => openRevisions(c)}>Revisions</Button>
          <Tooltip title="available after authentication (P10)">
            <Button size="small" danger disabled>Delete</Button>
          </Tooltip>
        </Space>
      ),
    },
  ]

  const revisionColumns: ColumnsType<Revision> = [
    { title: 'Revision', dataIndex: 'revision', key: 'revision' },
    { title: 'SHA256', dataIndex: 'sha256', key: 'sha256' },
    { title: 'Created', dataIndex: 'createdAt', key: 'createdAt' },
    { title: 'Active', key: 'active', render: (_, r) => (r.active ? <Tag color="green">active</Tag> : null) },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, r) =>
        r.active ? null : (
          <Button size="small" onClick={() => doRollback(r)}>Rollback</Button>
        ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      {error && <Alert type="error" message={error} showIcon />}
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <div />
        <Button type="primary" onClick={openCreate}>Create Config</Button>
      </Space>
      <Table rowKey="id" loading={loading} columns={columns} dataSource={configs} pagination={false} />

      <Modal title="Create Config" open={createOpen} onOk={submitCreate} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={createForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="kind" label="Kind" rules={[{ required: true }]}>
            <Select options={CONFIG_KINDS.map((k) => ({ value: k, label: k }))} />
          </Form.Item>
          <Form.Item name="source" label="Source" rules={[{ required: true }]}>
            <Input.TextArea rows={8} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title={`Edit ${editing?.name ?? ''}`} open={!!editing} onOk={submitEdit} onCancel={() => setEditing(null)} destroyOnHidden>
        <Form form={editForm} layout="vertical">
          <Form.Item name="source" label="Source" rules={[{ required: true }]}>
            <Input.TextArea rows={8} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={`Preview ${previewing?.name ?? ''}`}
        open={!!previewing}
        onCancel={() => setPreviewing(null)}
        footer={[
          <Button key="close" onClick={() => setPreviewing(null)}>Close</Button>,
          <Button key="run" type="primary" loading={previewLoading} onClick={runPreview}>Preview</Button>,
        ]}
        destroyOnHidden
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Input
            placeholder="host MAC (optional)"
            value={previewHost}
            onChange={(e) => setPreviewHost(e.target.value)}
          />
          {preview && (
            <>
              <Typography.Text strong>Rendered</Typography.Text>
              <Input.TextArea readOnly rows={8} value={preview.rendered} />
              <Typography.Text strong>Report</Typography.Text>
              <Input.TextArea readOnly rows={4} value={preview.report} />
            </>
          )}
        </Space>
      </Modal>

      <Drawer title={`Revisions — ${revisionsFor?.name ?? ''}`} open={!!revisionsFor} onClose={() => setRevisionsFor(null)} width={640}>
        <Table rowKey="revision" loading={revisionsLoading} columns={revisionColumns} dataSource={revisions} pagination={false} />
      </Drawer>
    </Space>
  )
}

function RolesTab() {
  const [roles, setRoles] = useState<Role[]>([])
  const [configs, setConfigs] = useState<Config[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [createOpen, setCreateOpen] = useState(false)
  const [createForm] = Form.useForm()

  const [editing, setEditing] = useState<Role | null>(null)
  const [editForm] = Form.useForm()

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [r, c] = await Promise.all([listRoles(), listConfigs()])
      setRoles(r)
      setConfigs(c.filter((cfg) => cfg.kind !== 'schematic'))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load roles')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      message.success(ok)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
  }

  const configOptions = configs.map((c) => ({ value: c.id, label: c.name }))

  const openCreate = () => {
    createForm.resetFields()
    setCreateOpen(true)
  }

  const submitCreate = async () => {
    const values = await createForm.validateFields()
    await act(() => createRole(values), `Created ${values.name}`)
    setCreateOpen(false)
  }

  const openEdit = (r: Role) => {
    editForm.setFieldsValue({ name: r.name, defaultConfigId: r.defaultConfigId })
    setEditing(r)
  }

  const submitEdit = async () => {
    if (!editing) return
    const values = await editForm.validateFields()
    await act(() => updateRole(editing.id, values), `Updated ${editing.name}`)
    setEditing(null)
  }

  const columns: ColumnsType<Role> = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    {
      title: 'Default Config',
      key: 'defaultConfigId',
      render: (_, r) => configs.find((c) => c.id === r.defaultConfigId)?.name ?? '—',
    },
    { title: 'Host Count', dataIndex: 'hostCount', key: 'hostCount' },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, r) => (
        <Space>
          <Button size="small" onClick={() => openEdit(r)}>Edit</Button>
          <Tooltip title="available after authentication (P10)">
            <Button size="small" danger disabled>Delete</Button>
          </Tooltip>
        </Space>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      {error && <Alert type="error" message={error} showIcon />}
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <div />
        <Button type="primary" onClick={openCreate}>Create Role</Button>
      </Space>
      <Table rowKey="id" loading={loading} columns={columns} dataSource={roles} pagination={false} />

      <Modal title="Create Role" open={createOpen} onOk={submitCreate} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={createForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="defaultConfigId" label="Default Config">
            <Select allowClear options={configOptions} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title={`Edit ${editing?.name ?? ''}`} open={!!editing} onOk={submitEdit} onCancel={() => setEditing(null)} destroyOnHidden>
        <Form form={editForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="defaultConfigId" label="Default Config">
            <Select allowClear options={configOptions} />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}

function shortId(id?: string): string {
  return id && id.length > 12 ? `${id.slice(0, 6)}…${id.slice(-4)}` : (id ?? '—')
}

function SchematicsTab() {
  const [schematics, setSchematics] = useState<Config[]>([])
  const [sources, setSources] = useState<Record<number, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [createOpen, setCreateOpen] = useState(false)
  const [createForm] = Form.useForm()

  const [editing, setEditing] = useState<Config | null>(null)
  const [editForm] = Form.useForm()
  // Non-null when the stored source is outside the generated subset — shown
  // read-only instead of being destroyed by the form round-trip.
  const [editRaw, setEditRaw] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const sch = (await listConfigs()).filter((c) => c.kind === 'schematic')
      setSchematics(sch)
      // The list shows each schematic's extension set; sources come from the
      // detail endpoint (small N — this catalog IS the point of the view: the
      // Factory itself refuses to list schematics).
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
    load()
  }, [load])

  const fieldsToSource = (values: { extensions?: string[]; overlayName?: string; overlayImage?: string }) =>
    buildCustomization({ extensions: values.extensions ?? [], overlayName: values.overlayName, overlayImage: values.overlayImage })

  const submitCreate = async () => {
    const values = await createForm.validateFields()
    try {
      const created = await createConfig({ name: values.name, kind: 'schematic', source: fieldsToSource(values) })
      message.success(`Built ${values.name}: ${created?.derivedSchematicId ?? 'unknown id'}`)
      setCreateOpen(false)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'schematic build failed')
    }
  }

  const openEdit = (c: Config) => {
    const fields = parseCustomization(sources[c.id] ?? '')
    if (fields) {
      editForm.setFieldsValue({ extensions: fields.extensions, overlayName: fields.overlayName, overlayImage: fields.overlayImage })
      setEditRaw(null)
    } else {
      setEditRaw(sources[c.id] ?? '')
    }
    setEditing(c)
  }

  const submitEdit = async () => {
    if (!editing || editRaw !== null) {
      setEditing(null)
      return
    }
    const values = await editForm.validateFields()
    try {
      const updated = await updateConfig(editing.id, fieldsToSource(values))
      message.success(`Rebuilt ${editing.name}: ${updated?.derivedSchematicId ?? 'unknown id'}`)
      setEditing(null)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'schematic build failed')
    }
  }

  const columns: ColumnsType<Config> = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    {
      title: 'Schematic ID',
      key: 'id',
      render: (_, c) => (
        <Tooltip title={c.derivedSchematicId}>
          <Typography.Text code>{shortId(c.derivedSchematicId)}</Typography.Text>
        </Tooltip>
      ),
    },
    {
      title: 'Extensions',
      key: 'extensions',
      render: (_, c) => {
        const fields = parseCustomization(sources[c.id] ?? '')
        if (!fields) return <Tag>custom</Tag>
        if (fields.extensions.length === 0 && !fields.overlayName) return <Tag>vanilla</Tag>
        return (
          <Space wrap>
            {fields.extensions.map((e) => (
              <Tag key={e}>{e}</Tag>
            ))}
            {fields.overlayName && <Tag color="blue">overlay: {fields.overlayName}</Tag>}
          </Space>
        )
      },
    },
    { title: 'Updated', dataIndex: 'updatedAt', key: 'updatedAt' },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, c) => (
        <Space>
          <Button size="small" onClick={() => openEdit(c)}>Edit</Button>
          <Tooltip title="available after authentication (P10)">
            <Button size="small" danger disabled>Delete</Button>
          </Tooltip>
        </Space>
      ),
    },
  ]

  const schematicFormItems = (
    <>
      <Form.Item name="extensions" label="Official extensions" help="e.g. siderolabs/iscsi-tools">
        <Select mode="tags" placeholder="siderolabs/…" open={false} tokenSeparators={[',', ' ']} />
      </Form.Item>
      <Form.Item name="overlayName" label="Overlay name (SBCs, optional)">
        <Input placeholder="rpi_generic" />
      </Form.Item>
      <Form.Item name="overlayImage" label="Overlay image (optional)">
        <Input placeholder="siderolabs/sbc-raspberrypi" />
      </Form.Item>
    </>
  )

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      {error && <Alert type="error" message={error} showIcon />}
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <div />
        <Button type="primary" onClick={() => { createForm.resetFields(); setCreateOpen(true) }}>Create Schematic</Button>
      </Space>
      <Table rowKey="id" loading={loading} columns={columns} dataSource={schematics} pagination={false} />

      <Modal title="Create Schematic" open={createOpen} onOk={submitCreate} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={createForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          {schematicFormItems}
        </Form>
      </Modal>

      <Modal title={`Edit ${editing?.name ?? ''}`} open={!!editing} onOk={submitEdit} onCancel={() => setEditing(null)} destroyOnHidden>
        {editRaw !== null ? (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Alert type="info" showIcon message="This schematic's source is not in the generated form; shown read-only." />
            <Input.TextArea readOnly rows={8} value={editRaw} />
          </Space>
        ) : (
          <Form form={editForm} layout="vertical">
            {schematicFormItems}
          </Form>
        )}
      </Modal>
    </Space>
  )
}

export default function BootConfigsView() {
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ margin: 0 }}>Boot Configs</Typography.Title>
      <Tabs
        items={[
          { key: 'configs', label: 'Configs', children: <ConfigsTab /> },
          { key: 'schematics', label: 'Schematics', children: <SchematicsTab /> },
          { key: 'roles', label: 'Roles', children: <RolesTab /> },
        ]}
      />
    </Space>
  )
}
