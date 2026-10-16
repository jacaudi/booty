import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, AutoComplete, Badge, Button, Drawer, Form, Modal, Popconfirm, Select, Space, Tabs, Table, Typography, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { isPending, type DiscoveredHost, type Host, type HostHealth } from '../api/types'
import { approveHostWith, bindSchematic, createHost, deleteHost, dismissAllDiscovered, dismissDiscovered, getHostHealth, listHostInventory, revokeHost, setMenuMode, wakeHost } from '../api/client'
import type { Config } from '../api/configs'
import { listConfigs } from '../api/configs'
import type { Role } from '../api/roles'
import { listRoles } from '../api/roles'
import { SCHEMATIC_KIND, isBootConfigKind, kindsForHostOS } from '../api/configKinds'
import type { FamilyKinds, OS } from '../api/catalog'
import { listOS, loadFamilyKinds } from '../api/catalog'

export default function HostsView() {
  const [hosts, setHosts] = useState<Host[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [configs, setConfigs] = useState<Config[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [approving, setApproving] = useState<Host | null>(null)
  const [approveForm] = Form.useForm()
  const [discovered, setDiscovered] = useState<DiscoveredHost[]>([])
  const [discoveredFull, setDiscoveredFull] = useState(false)
  const [approvingNew, setApprovingNew] = useState<DiscoveredHost | null>(null)
  // AntD 5's plain Modal does not await onOk, so a second click would start a second chain.
  const [submittingNew, setSubmittingNew] = useState(false)
  const [newForm] = Form.useForm()
  const [osOptions, setOsOptions] = useState<OS[]>([])
  const [familyKinds, setFamilyKinds] = useState<FamilyKinds | undefined>()

  const [healthFor, setHealthFor] = useState<string | null>(null)
  const [health, setHealth] = useState<HostHealth | null>(null)

  // The MAC most recently requested: a slow response for an earlier host must
  // not overwrite the drawer of the one opened since.
  const healthReq = useRef<string | null>(null)

  const openHealth = async (mac: string) => {
    healthReq.current = mac
    setHealthFor(mac)
    setHealth(null)
    try {
      const h = (await getHostHealth(mac)) ?? null
      if (healthReq.current === mac) setHealth(h)
    } catch (e) {
      if (healthReq.current === mac) message.error(e instanceof Error ? e.message : String(e))
    }
  }

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const inv = await listHostInventory()
      setHosts(inv.hosts)
      setDiscovered(inv.discovered)
      setDiscoveredFull(inv.discoveredFull)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load hosts')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  useEffect(() => {
    loadFamilyKinds().then(setFamilyKinds).catch(() => {})
  }, [])

  const act = async (fn: (mac: string) => Promise<unknown>, mac: string, ok: string) => {
    try {
      await fn(mac)
      message.success(ok)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
  }

  const openApprove = async (h: Host) => {
    approveForm.resetFields()
    setApproving(h)
    try {
      const [c, r] = await Promise.all([listConfigs(), listRoles()])
      setConfigs(c)
      setRoles(r)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load configs/roles')
    }
  }

  const submitApprove = async () => {
    if (!approving) return
    const values = await approveForm.validateFields().catch(() => undefined)
    if (!values) return
    const mac = approving.mac
    try {
      if (values.schematic) {
        // Bind BEFORE approve so approve's target-param encoding picks the
        // schematic up (approve reads host.Schematic at approval time).
        const byName = configs.find((c) => c.kind === SCHEMATIC_KIND && c.name === values.schematic)
        await bindSchematic(mac, byName ? { configId: byName.id } : { schematic: values.schematic })
      }
      await approveHostWith(mac, { configId: values.configId, roleIds: values.roleIds })
      message.success(`Approved ${mac}`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    } finally {
      setApproving(null)
      await load()
    }
  }

  const openApproveNew = async (d: DiscoveredHost) => {
    newForm.resetFields()
    setApprovingNew(d)
    try {
      const [c, r, os] = await Promise.all([listConfigs(), listRoles(), listOS()])
      setConfigs(c)
      setRoles(r)
      setOsOptions(os.filter((o) => o.family !== 'tool'))
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'failed to load configs/roles/OS list')
    }
  }

  // Approving a discovered machine chains three calls: create the host, bind a
  // Talos schematic, approve. A failure after the host exists rolls the host back
  // so no registered-but-unapproved host is left behind; a failure creating it
  // deletes nothing.
  const submitApproveNew = async () => {
    if (!approvingNew || submittingNew) return
    // outside try, and caught: an unhandled validateFields rejection fails the vitest run (BootConfigsView.tsx:78-81)
    const values = await newForm.validateFields().catch(() => undefined)
    if (!values) return
    setSubmittingNew(true)
    const mac = approvingNew.mac
    let created = false
    try {
      // The list may be stale: another operator may have registered this MAC
      // since it loaded. Creating over a registered host would overwrite it, and
      // the rollback would then delete it, so re-check before any write.
      const inv = await listHostInventory()
      if (inv.hosts.some((h) => h.mac === mac)) {
        message.error(`${mac} is already registered; reload the list`)
        return
      }
      await createHost({ mac, os: values.os })
      created = true
      if (values.os === 'talos' && values.schematic) {
        const byName = configs.find((c) => c.kind === SCHEMATIC_KIND && c.name === values.schematic)
        await bindSchematic(mac, byName ? { configId: byName.id } : { schematic: values.schematic })
      }
      await approveHostWith(mac, { configId: values.configId, roleIds: values.roleIds })
      message.success(`Approved ${mac}`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'approve failed')
      if (created) {
        try {
          await deleteHost(mac)
        } catch (e2) {
          message.error(e2 instanceof Error ? e2.message : 'rollback failed')
        }
      }
    } finally {
      setSubmittingNew(false)
      setApprovingNew(null)
      await load()
    }
  }

  const pending = hosts.filter(isPending)
  const approved = hosts.filter((h) => !isPending(h))

  // Only kinds THIS HOST's OS family admits. familyAllowsKind is per-family
  // (render.go:34-43): a butane config on a Talos host is as silently useless
  // as a taloscluster — resolveConfig falls through to the default file with
  // only a slog.Warn. An unknown OS falls back to the full boot-config union,
  // which still excludes schematic and taloscluster. Hoisted out of the
  // Select's options filter so it isn't recomputed on every render.
  //
  // `undefined` while familyKinds is still loading — treated as permissive
  // (offer every bindable kind) by the options filter below, the same
  // fallback the OS-agnostic union provided before this view fetched the
  // server's family→kind mapping.
  const allowedKinds = useMemo(
    () => (familyKinds ? kindsForHostOS(approving?.os, familyKinds) : undefined),
    [approving?.os, familyKinds],
  )

  const selectedOS: string | undefined = Form.useWatch('os', newForm)
  const newAllowedKinds = useMemo(
    () => (familyKinds ? kindsForHostOS(selectedOS, familyKinds) : undefined),
    [selectedOS, familyKinds],
  )
  const configOptions = (kinds: string[] | undefined) =>
    configs
      .filter((c) => isBootConfigKind(c.kind) && (kinds === undefined || kinds.includes(c.kind)))
      .map((c) => ({ value: c.id, label: c.name }))
  const schematicOptions = configs.filter((c) => c.kind === SCHEMATIC_KIND).map((c) => ({ value: c.name, label: c.name }))

  const baseCols: ColumnsType<Host> = [
    { title: 'MAC', dataIndex: 'mac', key: 'mac' },
    { title: 'Hostname', dataIndex: 'hostname', key: 'hostname' },
    { title: 'IP', dataIndex: 'ip', key: 'ip' },
    { title: 'OS', dataIndex: 'os', key: 'os' },
    { title: 'Booted', dataIndex: 'booted', key: 'booted' },
    {
      title: 'Running',
      key: 'running',
      render: (_, h) =>
        h.health ? (
          <Space>
            <Button type="link" size="small" onClick={() => openHealth(h.mac)}>
              {h.health.running}
            </Button>
            {h.health.failedUnitCount > 0 && <Badge count={h.health.failedUnitCount} />}
          </Space>
        ) : (
          '—'
        ),
    },
    { title: 'Last report', key: 'lastReport', render: (_, h) => h.health?.receivedAt ?? '—' },
  ]

  const pendingCols: ColumnsType<Host> = [
    ...baseCols,
    {
      title: 'Actions',
      key: 'actions',
      render: (_, h) => (
        <Space>
          {/* Hidden (not just disabled) while its own Approve modal is open: an
              accessible-but-disabled button would still satisfy role/name
              queries and collide with the modal's OK button. */}
          {approving?.mac !== h.mac && (
            <Button type="primary" size="small" onClick={() => openApprove(h)}>
              Approve
            </Button>
          )}
          <Button size="small" onClick={() => act(setMenuMode, h.mac, `Boot menu for ${h.mac}`)}>
            Boot menu
          </Button>
          <Popconfirm
            title={`Send Wake-on-LAN to ${h.mac}?`}
            okText="Wake now"
            onConfirm={() => act(wakeHost, h.mac, `Wake-on-LAN sent to ${h.mac}`)}
          >
            <Button size="small">Wake</Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const discoveredCols: ColumnsType<DiscoveredHost> = [
    { title: 'MAC', dataIndex: 'mac', key: 'mac' },
    { title: 'Arch', key: 'arch', render: (_, d) => d.arch || '—' },
    { title: 'IP', dataIndex: 'ip', key: 'ip' },
    { title: 'First seen', key: 'firstSeen', render: (_, d) => new Date(d.firstSeen).toLocaleString() },
    { title: 'Last seen', key: 'lastSeen', render: (_, d) => new Date(d.lastSeen).toLocaleString() },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, d) => (
        <Space>
          {approvingNew?.mac !== d.mac && (
            <Button type="primary" size="small" onClick={() => openApproveNew(d)}>
              Approve
            </Button>
          )}
          <Popconfirm
            title={`Dismiss ${d.mac}?`}
            onConfirm={() => act(dismissDiscovered, d.mac, `Dismissed ${d.mac}`)}
          >
            <Button size="small">Dismiss</Button>
          </Popconfirm>
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
          <Popconfirm
            title={`Send Wake-on-LAN to ${h.mac}?`}
            okText="Wake now"
            onConfirm={() => act(wakeHost, h.mac, `Wake-on-LAN sent to ${h.mac}`)}
          >
            <Button size="small">Wake</Button>
          </Popconfirm>
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
            label: <Badge count={pending.length + discovered.length} offset={[12, -2]} size="small">Pending</Badge>,
            children: (
              <Space direction="vertical" size="large" style={{ width: '100%' }}>
                {(discovered.length > 0 || discoveredFull) && (
                  <Space direction="vertical" style={{ width: '100%' }}>
                    <Space>
                      <Typography.Title level={5} style={{ margin: 0 }}>New machines</Typography.Title>
                      <Popconfirm
                        title={`Dismiss all ${discovered.length} new machines?`}
                        onConfirm={async () => {
                          try {
                            const res = await dismissAllDiscovered()
                            message.success(`Dismissed ${res?.deleted ?? 0}`)
                            await load()
                          } catch (e) {
                            message.error(e instanceof Error ? e.message : 'dismiss failed')
                          }
                        }}
                      >
                        <Button size="small">Dismiss all</Button>
                      </Popconfirm>
                    </Space>
                    {discoveredFull && (
                      <Alert type="warning" showIcon message="The new-machine list is full (1024). booty is not recording new machines until you dismiss some." />
                    )}
                    <Table rowKey="mac" loading={loading} columns={discoveredCols} dataSource={discovered} pagination={false} />
                  </Space>
                )}
                <Table rowKey="mac" loading={loading} columns={pendingCols} dataSource={pending} pagination={false} />
              </Space>
            ),
          },
          {
            key: 'approved',
            label: 'Approved',
            children: <Table rowKey="mac" loading={loading} columns={approvedCols} dataSource={approved} pagination={false} />,
          },
        ]}
      />

      <Modal title={`Approve ${approving?.mac ?? ''}`} open={!!approving} onOk={submitApprove} onCancel={() => setApproving(null)} destroyOnHidden>
        <Form form={approveForm} layout="vertical">
          <Form.Item name="configId" label="Config">
            <Select
              allowClear
              placeholder="none"
              options={configOptions(allowedKinds)}
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
          {approving?.os === 'talos' && (
            <Form.Item name="schematic" label="Talos schematic" help="pick a named schematic or paste a raw ID">
              <AutoComplete
                allowClear
                placeholder="vanilla"
                options={schematicOptions}
              />
            </Form.Item>
          )}
        </Form>
      </Modal>
      <Modal title={`Approve ${approvingNew?.mac ?? ''}`} open={!!approvingNew} onOk={submitApproveNew} confirmLoading={submittingNew} onCancel={() => setApprovingNew(null)} destroyOnHidden>
        <Form
          form={newForm}
          layout="vertical"
          onValuesChange={(c) => {
            if ('os' in c) newForm.setFieldValue('configId', undefined)
          }}
        >
          <Form.Item name="os" label="OS" rules={[{ required: true, message: 'Choose an OS' }]}>
            <Select placeholder="choose an OS" options={osOptions.map((o) => ({ value: o.name, label: o.name }))} />
          </Form.Item>
          <Form.Item name="configId" label="Config">
            <Select allowClear placeholder="none" options={configOptions(newAllowedKinds)} />
          </Form.Item>
          <Form.Item name="roleIds" label="Roles">
            <Select mode="multiple" allowClear placeholder="none" options={roles.map((r) => ({ value: r.id, label: r.name }))} />
          </Form.Item>
          {selectedOS === 'talos' && (
            <Form.Item name="schematic" label="Talos schematic" help="pick a named schematic or paste a raw ID">
              <AutoComplete allowClear placeholder="vanilla" options={schematicOptions} />
            </Form.Item>
          )}
        </Form>
      </Modal>
      <Drawer title={`Health: ${healthFor ?? ''}`} open={healthFor !== null} onClose={() => setHealthFor(null)} width={520}>
        {health && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <div>Kernel: {health.kernel}</div>
            <div>Firmware: {health.firmware || '—'}</div>
            <div>
              Hardware: {health.dmi.vendor} {health.dmi.product}
            </div>
            <Typography.Text strong>Failed units</Typography.Text>
            {health.failedUnits.length === 0 ? (
              <div>None</div>
            ) : (
              <ul>
                {health.failedUnits.map((u, i) => (
                  <li key={`${i}-${u}`}>{u}</li>
                ))}
              </ul>
            )}
            <Typography.Text strong>Journal errors</Typography.Text>
            <pre style={{ whiteSpace: 'pre-wrap' }}>{health.journalErrors.join('\n')}</pre>
          </Space>
        )}
      </Drawer>
    </Space>
  )
}
