import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Collapse, Drawer, Form, Input, Modal, Radio, Select, Space, Table, Tabs, Tag, Tooltip, Typography, Upload, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Config, Preview, Revision } from '../api/configs'
import { createConfig, getConfig, listConfigs, listRevisions, previewConfig, rollbackConfig, updateConfig } from '../api/configs'
import type { Role } from '../api/roles'
import { createRole, listRoles, updateRole } from '../api/roles'
import { OS_CHOICES, isBootConfigKind, kindForOS, osNameForKind } from '../api/configKinds'

function ConfigsTab() {
  const [configs, setConfigs] = useState<Config[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [createOpen, setCreateOpen] = useState(false)
  const [createForm] = Form.useForm()
  // The kind is DERIVED from the OS, never chosen. Watched so the form can show
  // the user what the OS resolves to before they submit.
  const createOS = Form.useWatch<string | undefined>('os', createForm)

  const [editing, setEditing] = useState<Config | null>(null)
  const [editForm] = Form.useForm()

  const [previewing, setPreviewing] = useState<Config | null>(null)
  const [previewHost, setPreviewHost] = useState('')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)

  const [revisionsFor, setRevisionsFor] = useState<Config | null>(null)
  const [revisions, setRevisions] = useState<Revision[]>([])
  const [revisionsLoading, setRevisionsLoading] = useState(false)

  const [validating, setValidating] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      // Only kinds renderConfig can actually serve to a machine. This excludes
      // `schematic` (an IMAGE identity, now on OS Images) and `taloscluster` (a
      // cluster spec, owned by the Clusters page) — neither is allowed by any
      // family in familyAllowsKind (render.go:34-43).
      setConfigs((await listConfigs()).filter((c) => isBootConfigKind(c.kind)))
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
    const kind = kindForOS(values.os)
    if (!kind) return // unreachable: the OS field is required and comes from OS_CHOICES
    await act(
      () => createConfig({ name: values.name, kind, source: values.source }),
      `Created ${values.name}`,
    )
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

  const validate = async (c: Config) => {
    setValidating(c.id)
    try {
      const preview = await previewConfig(c.id)
      // A resolved preview is NOT proof of validity: the server returns 200 and
      // folds a render failure into `report` (api_configs.go:236-240). Inspect the
      // body — an empty `rendered` means the render failed (SGE B1).
      if (!preview?.rendered) {
        message.error(preview?.report || `${c.name} is invalid`)
      } else {
        message.success(`${c.name} is valid`)
      }
    } catch (e) {
      // Real rejections: non-renderable kind, no active revision, transport. Task 1
      // preserved the response body, so this message is actually useful.
      message.error(e instanceof Error ? e.message : `${c.name} is invalid`)
    } finally {
      setValidating(null)
    }
  }

  const columns: ColumnsType<Config> = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    {
      title: 'Kind',
      key: 'kind',
      // There is no OS column: ConfigDTO carries no OS (a config binds to hosts,
      // not to an OS), so one would be a pure reverse-map of this cell. The OS
      // product name leads; the literal server kind sits beneath it.
      render: (_, c) => (
        <Space direction="vertical" size={0}>
          <Typography.Text>{osNameForKind(c.kind)}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>{c.kind}</Typography.Text>
        </Space>
      ),
    },
    { title: 'Active Rev', dataIndex: 'activeRevision', key: 'activeRevision' },
    { title: 'Updated', dataIndex: 'updatedAt', key: 'updatedAt' },
    {
      title: 'Actions',
      key: 'actions',
      // No taloscluster guard on Validate any more: a taloscluster no longer
      // reaches this list, so every row here is renderable by construction.
      render: (_, c) => (
        <Space>
          <Button size="small" onClick={() => openEdit(c)}>Edit</Button>
          <Button size="small" onClick={() => openPreview(c)}>Preview</Button>
          <Button size="small" onClick={() => openRevisions(c)}>Revisions</Button>
          <Button size="small" loading={validating === c.id} onClick={() => validate(c)}>
            Validate
          </Button>
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

      <Collapse
        items={[{
          key: 'vars',
          label: 'Template variables',
          children: (
            <Typography>
              <Typography.Paragraph>Available in every rendered config:</Typography.Paragraph>
              <ul>
                <li><Typography.Text code>{'{{ .MAC }}'}</Typography.Text> — host MAC</li>
                <li><Typography.Text code>{'{{ .Hostname }}'}</Typography.Text> — host name</li>
                <li><Typography.Text code>{'{{ .IP }}'}</Typography.Text> — observed IP</li>
                <li><Typography.Text code>{'{{ .UUID }}'}</Typography.Text> — hardware UUID</li>
                <li><Typography.Text code>{'{{ .Serial }}'}</Typography.Text> — hardware serial</li>
                <li><Typography.Text code>{'{{ .ServerIP }}'}</Typography.Text> — booty server IP</li>
                <li><Typography.Text code>{'{{ .ServerHTTPPort }}'}</Typography.Text> — booty HTTP port</li>
                <li><Typography.Text code>{'{{ .JoinString }}'}</Typography.Text> — join string</li>
              </ul>
              <Typography.Paragraph>Populated for the machineconfig family only:</Typography.Paragraph>
              <ul>
                <li><Typography.Text code>{'{{ .TalosVersion }}'}</Typography.Text></li>
                <li><Typography.Text code>{'{{ .Schematic }}'}</Typography.Text></li>
                <li><Typography.Text code>{'{{ .Roles }}'}</Typography.Text></li>
              </ul>
            </Typography>
          ),
        }]}
      />

      <Modal title="Create Config" open={createOpen} onOk={submitCreate} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={createForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="os" label="OS" rules={[{ required: true, message: 'Pick the OS this config is for' }]}>
            <Radio.Group>
              <Space direction="vertical">
                {OS_CHOICES.map((o) => (
                  <Radio key={o.value} value={o.value}>{o.label}</Radio>
                ))}
              </Space>
            </Radio.Group>
          </Form.Item>
          <Form.Item label="Kind">
            <Typography.Text type="secondary" data-testid="derived-kind">
              {createOS ? kindForOS(createOS) : 'follows from the OS'}
            </Typography.Text>
          </Form.Item>
          <Form.Item label="Upload a file (optional)">
            <Upload.Dragger
              beforeUpload={(file) => {
                const reader = new FileReader()
                reader.onload = () => createForm.setFieldValue('source', String(reader.result ?? ''))
                reader.readAsText(file)
                return false // prevent auto-upload; we only read the text locally
              }}
              maxCount={1}
            >
              <p className="ant-upload-text">Drag a config file here, or click to select</p>
            </Upload.Dragger>
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
      render: (_, r) => (
        <Select
          style={{ minWidth: 180 }}
          aria-label={`default config for ${r.name}`}
          placeholder="None"
          value={r.defaultConfigId ?? null}
          options={configOptions}
          onChange={(value) => act(() => updateRole(r.id, { name: r.name, defaultConfigId: value }), `Updated ${r.name}`)}
        />
      ),
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

export default function BootConfigsView() {
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ margin: 0 }}>Boot Configs</Typography.Title>
      <Tabs
        items={[
          { key: 'configs', label: 'Configs', children: <ConfigsTab /> },
          { key: 'roles', label: 'Roles', children: <RolesTab /> },
        ]}
      />
    </Space>
  )
}
