import type { ReactNode } from 'react'
import { useEffect, useState } from 'react'
import { Alert, Button, Form, Input, Modal, Space, Table, Tag, message } from 'antd'
import type { Cluster } from '../api/clusters'
import { addMember, createCluster, importCluster, listClusters, removeMember } from '../api/clusters'

export default function ClustersView() {
  const [clusters, setClusters] = useState<Cluster[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [createForm] = Form.useForm()
  const [importForm] = Form.useForm()

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
    const v = await importForm.validateFields()
    try {
      await importCluster(v)
      message.success(`Imported ${v.name}`)
      setImportOpen(false)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'import failed')
    }
  }

  const columns = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    { title: 'Endpoint', dataIndex: 'endpoint', key: 'endpoint' },
    { title: 'Talos', dataIndex: 'talosVersion', key: 'talos' },
    { title: 'Kubernetes', dataIndex: 'k8sVersion', key: 'k8s' },
    { title: 'Members', key: 'members', render: (_: unknown, c: Cluster) => c.members.length },
  ]

  const memberColumns = (clusterId: number) => [
    { title: 'MAC', dataIndex: 'mac', key: 'mac' },
    { title: 'Type', dataIndex: 'machineType', key: 'type' },
    { title: 'Schematic', dataIndex: 'schematic', key: 'schematic', render: (s?: string) => s ? s.slice(0, 12) : '—' },
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
              <Table rowKey="mac" size="small" columns={memberColumns(c.id)} dataSource={c.members} pagination={false} />
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
          <Form.Item name="controlplaneMac" label="Control-plane host MAC" rules={[{ required: true }]}><Input placeholder="aa:bb:cc:dd:ee:ff" /></Form.Item>
          <Form.Item name="controlplane" label="controlplane.yaml" rules={[{ required: true }]}><Input.TextArea rows={10} /></Form.Item>
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
