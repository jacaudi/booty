import type { ReactNode } from 'react'
import { useEffect, useState } from 'react'
import { Alert, Button, Form, Input, Modal, Select, Space, Table, Tag, message } from 'antd'
import type { Cluster } from '../api/clusters'
import { addMember, createCluster, exportClusterSecrets, importCluster, listClusters, removeMember, updateCluster } from '../api/clusters'
import { TALOSCLUSTER_KIND } from '../api/configKinds'
import type { Config } from '../api/configs'
import { listConfigs } from '../api/configs'
import { shortSchematicId } from '../api/schematicId'

export default function ClustersView() {
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [createForm] = Form.useForm()
  const [importForm] = Form.useForm()
  const [exportYaml, setExportYaml] = useState<string | null>(null)
  const [editing, setEditing] = useState<Cluster | null>(null)
  const [editForm] = Form.useForm()
  const [saving, setSaving] = useState(false)
  const [specConfigs, setSpecConfigs] = useState<Config[]>([])

  const load = async () => {
    setLoading(true)
    try {
      setClusters(await listClusters())
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load clusters')
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { void load() }, [])

  const submitCreate = async () => {
    const v = await createForm.validateFields()
    try {
      await createCluster(v)
      message.success(`Created ${v.name}`)
      setCreateOpen(false)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'create failed')
    }
  }

  const submitImport = async () => {
    // validateFields() REJECTS on invalid input; catch it to an early return so an
    // incomplete row surfaces inline errors instead of leaking an unhandled promise
    // rejection (which fails the vitest CI run even though every assertion passes).
    const v = await importForm.validateFields().catch(() => undefined)
    if (!v) return
    try {
      await importCluster(v)
      message.success(`Imported ${v.name}`)
      setImportOpen(false)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'import failed')
    }
  }

  const doExport = async (c: Cluster) => {
    try {
      const res = await exportClusterSecrets(c.id)
      setExportYaml(res?.secretsYaml ?? '')
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'export failed')
    }
  }

  const openEdit = async (c: Cluster) => {
    // Prefill with the CURRENT binding: an untouched Select then re-sends the same
    // id (a no-op), and an unbound cluster sends nothing at all — both preserve
    // the server's state, which is the only thing PUT can express.
    editForm.setFieldsValue({
      endpoint: c.endpoint,
      talosVersion: c.talosVersion,
      k8sVersion: c.k8sVersion,
      specConfigId: c.specConfigId,
    })
    setEditing(c)
    try {
      setSpecConfigs((await listConfigs()).filter((cfg) => cfg.kind === TALOSCLUSTER_KIND))
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load cluster specs')
    }
  }

  const submitEdit = async () => {
    if (!editing) return
    const v = await editForm.validateFields()
    setSaving(true)
    try {
      // A Talos-version bump ensures + pins every member's cache targets BEFORE
      // committing, then kicks an async reconcile — so this can 422, but it does
      // NOT block on downloads (SGE I4).
      const input: { endpoint: string; talosVersion: string; k8sVersion: string; specConfigId?: number } = {
        endpoint: v.endpoint,
        talosVersion: v.talosVersion,
        k8sVersion: v.k8sVersion,
      }
      // Omitted => the server PRESERVES the existing binding. There is no way to
      // clear one (api_clusters.go:198-206), which is why the Select has no clear.
      if (v.specConfigId !== undefined && v.specConfigId !== null) input.specConfigId = v.specConfigId
      await updateCluster(editing.id, input)
      message.success(`Updated ${editing.name}`)
      setEditing(null)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'update failed')
    } finally {
      setSaving(false)
    }
  }

  const columns = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Endpoint', dataIndex: 'endpoint', key: 'endpoint' },
    { title: 'Talos', dataIndex: 'talosVersion', key: 'talos' },
    { title: 'Kubernetes', dataIndex: 'k8sVersion', key: 'k8s' },
    { title: 'Members', key: 'members', render: (_: unknown, c: Cluster) => c.members?.length ?? 0 },
    {
      title: 'Actions', key: 'actions',
      render: (_: unknown, c: Cluster) => (
        <Space>
          <Button size="small" onClick={() => openEdit(c)}>Edit</Button>
          <Button size="small" onClick={() => doExport(c)}>Export</Button>
        </Space>
      ),
    },
  ]

  const memberColumns = (clusterId: number) => [
    { title: 'MAC', dataIndex: 'mac', key: 'mac' },
    { title: 'Type', dataIndex: 'machineType', key: 'type' },
    { title: 'Schematic', dataIndex: 'schematic', key: 'schematic', render: (s?: string) => s ? shortSchematicId(s) : '—' },
    {
      title: 'Status', dataIndex: 'status', key: 'status',
      render: (s: string) => <Tag color={s === 'booted' ? 'green' : 'default'}>{s}</Tag>,
    },
    {
      title: '', key: 'actions',
      render: (_: unknown, m: { mac: string }) => (
        <Button size="small" danger onClick={async () => {
          try { await removeMember(clusterId, m.mac); await load() }
          catch (e) { message.error(e instanceof Error ? e.message : 'remove failed') }
        }}>Remove</Button>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      {error && <Alert type="error" message={error} showIcon />}
      <Space style={{ justifyContent: 'flex-end', width: '100%' }}>
        <Button onClick={() => { importForm.resetFields(); setImportOpen(true) }}>Import</Button>
        <Button type="primary" onClick={() => { createForm.resetFields(); setCreateOpen(true) }}>Create Cluster</Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        columns={columns}
        dataSource={clusters}
        pagination={false}
        expandable={{
          expandedRowRender: (c) => (
            <AssignMemberPanel cluster={c} onChange={load}>
              <Table rowKey="mac" size="small" columns={memberColumns(c.id)} dataSource={c.members ?? []} pagination={false} />
            </AssignMemberPanel>
          ),
        }}
      />

      <Modal title="Create Cluster" open={createOpen} onOk={submitCreate} onCancel={() => setCreateOpen(false)} destroyOnHidden>
        <Form form={createForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="endpoint" label="Endpoint" rules={[{ required: true }]}><Input placeholder="https://10.0.0.10:6443" /></Form.Item>
          <Form.Item name="talosVersion" label="Talos version" rules={[{ required: true }]}><Input placeholder="v1.13.5" /></Form.Item>
          <Form.Item name="k8sVersion" label="Kubernetes version" rules={[{ required: true }]}><Input placeholder="v1.34.0" /></Form.Item>
        </Form>
      </Modal>

      <Modal title="Import Cluster" open={importOpen} onOk={submitImport} onCancel={() => setImportOpen(false)} destroyOnHidden>
        <Form form={importForm} layout="vertical">
          <Form.Item name="name" label="Name" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.List name="controlPlanes" initialValue={[{ mac: '', controlplane: '' }]}>
            {(fields, { add, remove }) => (
              <Space direction="vertical" style={{ width: '100%' }}>
                {fields.map((field, idx) => (
                  <Space key={field.key} direction="vertical" style={{ width: '100%' }}>
                    <Space style={{ width: '100%', justifyContent: 'space-between' }}>
                      <span>Control-plane host {idx + 1}</span>
                      {fields.length > 1 && (
                        <Button size="small" danger onClick={() => remove(field.name)}>Remove</Button>
                      )}
                    </Space>
                    <Form.Item name={[field.name, 'mac']} label="MAC" rules={[{ required: true }]}>
                      <Input placeholder="aa:bb:cc:dd:ee:ff" />
                    </Form.Item>
                    <Form.Item name={[field.name, 'controlplane']} label="controlplane.yaml" rules={[{ required: true }]}>
                      <Input.TextArea rows={8} placeholder="Paste this node's controlplane.yaml" />
                    </Form.Item>
                  </Space>
                ))}
                <Button onClick={() => add({ mac: '', controlplane: '' })}>Add control-plane host</Button>
              </Space>
            )}
          </Form.List>
        </Form>
      </Modal>

      <Modal
        title="Cluster Secrets"
        open={exportYaml !== null}
        onCancel={() => setExportYaml(null)}
        footer={<Button onClick={() => setExportYaml(null)}>Close</Button>}
        destroyOnHidden
      >
        <Input.TextArea rows={16} value={exportYaml ?? ''} readOnly />
      </Modal>

      <Modal
        title="Edit Cluster"
        open={editing !== null}
        onOk={submitEdit}
        onCancel={() => setEditing(null)}
        okText="Save"
        okButtonProps={{ loading: saving }}
        cancelButtonProps={{ disabled: saving }}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="endpoint" label="Endpoint" rules={[{ required: true }]}><Input placeholder="https://10.0.0.10:6443" /></Form.Item>
          <Form.Item
            name="talosVersion"
            label="Talos version"
            rules={[{ required: true }]}
            extra="A version change pins new boot assets for every member before saving; caching then happens in the background."
          >
            <Input placeholder="v1.13.5" />
          </Form.Item>
          <Form.Item name="k8sVersion" label="Kubernetes version" rules={[{ required: true }]}><Input placeholder="v1.34.0" /></Form.Item>
          <Form.Item
            name="specConfigId"
            label="Spec config"
            extra="A taloscluster config, layered into every generated node config. It cannot be unbound once set."
          >
            <Select
              placeholder="None"
              options={specConfigs.map((c) => ({ value: c.id, label: c.name }))}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}

// AssignMemberPanel wraps the members table with an inline "assign a host"
// control (mac + machineType + optional schematic) that calls addMember.
function AssignMemberPanel({ cluster, onChange, children }: { cluster: Cluster; onChange: () => Promise<void>; children: ReactNode }) {
  const [form] = Form.useForm()
  const submit = async () => {
    const v = await form.validateFields()
    try {
      await addMember(cluster.id, v)
      message.success(`Assigned ${v.mac}`)
      form.resetFields()
      await onChange()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'assign failed')
    }
  }
  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      {children}
      <Form form={form} layout="inline" onFinish={submit}>
        <Form.Item name="mac" rules={[{ required: true }]}><Input placeholder="host MAC" /></Form.Item>
        <Form.Item name="machineType" rules={[{ required: true }]}>
          <Input placeholder="controlplane | worker" />
        </Form.Item>
        <Form.Item name="schematic"><Input placeholder="schematic (optional)" /></Form.Item>
        <Form.Item><Button htmlType="submit">Assign host</Button></Form.Item>
      </Form>
    </Space>
  )
}
