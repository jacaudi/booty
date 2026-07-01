import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Space, Table, Typography, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Host } from '../api/types'
import { approveHost, listHosts, revokeHost, setMenuMode } from '../api/client'

export default function HostsView() {
  const [hosts, setHosts] = useState<Host[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

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

  const pending = hosts.filter((h) => !h.approved)
  const approved = hosts.filter((h) => h.approved)

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
          <Button type="primary" size="small" onClick={() => act(approveHost, h.mac, `Approved ${h.mac}`)}>
            Approve
          </Button>
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
      <div>
        <Typography.Title level={5}>Pending</Typography.Title>
        <Table rowKey="mac" loading={loading} columns={pendingCols} dataSource={pending} pagination={false} />
      </div>
      <div>
        <Typography.Title level={5}>Approved</Typography.Title>
        <Table rowKey="mac" loading={loading} columns={approvedCols} dataSource={approved} pagination={false} />
      </div>
    </Space>
  )
}
