import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, AutoComplete, Badge, Button, Form, Modal, Select, Space, Tabs, Table, Typography, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Host } from '../api/types'
import { approveHostWith, bindSchematic, listHosts, revokeHost, setMenuMode } from '../api/client'
import type { Config } from '../api/configs'
import { listConfigs } from '../api/configs'
import type { Role } from '../api/roles'
import { listRoles } from '../api/roles'
import { SCHEMATIC_KIND, isBootConfigKind, kindsForHostOS } from '../api/configKinds'

export default function HostsView() {
  const [hosts, setHosts] = useState<Host[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [configs, setConfigs] = useState<Config[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [allowing, setAllowing] = useState<Host | null>(null)
  const [allowForm] = Form.useForm()

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setHosts(await listHosts())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load hosts')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const act = async (fn: (mac: string) => Promise<unknown>, mac: string, ok: string) => {
    try {
      await fn(mac)
      message.success(ok)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
  }

  const openAllow = async (h: Host) => {
    allowForm.resetFields()
    setAllowing(h)
    try {
      const [c, r] = await Promise.all([listConfigs(), listRoles()])
      setConfigs(c)
      setRoles(r)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load configs/roles')
    }
  }

  const submitAllow = async () => {
    if (!allowing) return
    const values = await allowForm.validateFields()
    const mac = allowing.mac
    try {
      if (values.schematic) {
        // Bind BEFORE approve so approve's target-param encoding picks the
        // schematic up (approve reads host.Schematic at approval time).
        const byName = configs.find((c) => c.kind === SCHEMATIC_KIND && c.name === values.schematic)
        await bindSchematic(mac, byName ? { configId: byName.id } : { schematic: values.schematic })
      }
      await approveHostWith(mac, { configId: values.configId, roleIds: values.roleIds })
      message.success(`Approved ${mac}`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
    setAllowing(null)
  }

  const pending = hosts.filter((h) => !h.approved)
  const approved = hosts.filter((h) => h.approved)

  // Only kinds THIS HOST's OS family admits. familyAllowsKind is per-family
  // (render.go:34-43): a butane config on a Talos host is as silently useless
  // as a taloscluster — resolveConfig falls through to the default file with
  // only a slog.Warn. An unknown OS falls back to the full boot-config union,
  // which still excludes schematic and taloscluster. Hoisted out of the
  // Select's options filter so it isn't recomputed on every render.
  const allowedKinds = useMemo(() => kindsForHostOS(allowing?.os), [allowing?.os])

  const baseCols: ColumnsType<Host> = [
    { title: 'MAC', dataIndex: 'mac', key: 'mac' },
    { title: 'Hostname', dataIndex: 'hostname', key: 'hostname' },
    { title: 'IP', dataIndex: 'ip', key: 'ip' },
    { title: 'OS', dataIndex: 'os', key: 'os' },
    { title: 'Booted', dataIndex: 'booted', key: 'booted' },
  ]

  const pendingCols: ColumnsType<Host> = [
    ...baseCols,
    {
      title: 'Actions',
      key: 'actions',
      render: (_, h) => (
        <Space>
          {/* Hidden (not just disabled) while its own Allow modal is open: an
              accessible-but-disabled button would still satisfy role/name
              queries and collide with the modal's OK button. */}
          {allowing?.mac !== h.mac && (
            <Button type="primary" size="small" onClick={() => openAllow(h)}>
              Allow
            </Button>
          )}
          <Button size="small" onClick={() => act(setMenuMode, h.mac, `Boot menu for ${h.mac}`)}>
            Boot menu
          </Button>
        </Space>
      ),
    },
  ]

  const approvedCols: ColumnsType<Host> = [
    ...baseCols,
    { title: 'Boot Mode', dataIndex: 'bootMode', key: 'bootMode' },
    { title: 'Assigned OS', dataIndex: 'assignedOS', key: 'assignedOS' },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, h) => (
        <Space>
          <Button danger size="small" onClick={() => act(revokeHost, h.mac, `Revoked ${h.mac}`)}>
            Revoke
          </Button>
          <Button size="small" onClick={() => act(setMenuMode, h.mac, `Boot menu for ${h.mac}`)}>
            Boot menu
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3}>Hosts</Typography.Title>
      {error && <Alert type="error" message={error} showIcon />}
      <Tabs
        items={[
          {
            key: 'pending',
            label: <Badge count={pending.length} offset={[12, -2]} size="small">Pending</Badge>,
            children: <Table rowKey="mac" loading={loading} columns={pendingCols} dataSource={pending} pagination={false} />,
          },
          {
            key: 'approved',
            label: 'Approved',
            children: <Table rowKey="mac" loading={loading} columns={approvedCols} dataSource={approved} pagination={false} />,
          },
        ]}
      />

      <Modal title={`Allow ${allowing?.mac ?? ''}`} open={!!allowing} onOk={submitAllow} onCancel={() => setAllowing(null)} destroyOnHidden>
        <Form form={allowForm} layout="vertical">
          <Form.Item name="configId" label="Config">
            <Select
              allowClear
              placeholder="none"
              options={configs
                .filter((c) => isBootConfigKind(c.kind) && allowedKinds.includes(c.kind))
                .map((c) => ({ value: c.id, label: c.name }))}
            />
          </Form.Item>
          <Form.Item name="roleIds" label="Roles">
            <Select
              mode="multiple"
              allowClear
              placeholder="none"
              options={roles.map((r) => ({ value: r.id, label: r.name }))}
            />
          </Form.Item>
          {allowing?.os === 'talos' && (
            <Form.Item name="schematic" label="Talos schematic" help="pick a named schematic or paste a raw ID">
              <AutoComplete
                allowClear
                placeholder="vanilla"
                options={configs
                  .filter((c) => c.kind === SCHEMATIC_KIND)
                  .map((c) => ({ value: c.name, label: c.name }))}
              />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </Space>
  )
}
