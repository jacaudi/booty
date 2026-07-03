import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Space, Table, Tag, Tooltip, Typography, message } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { CacheEntry } from '../api/cache'
import { listCache, pinCache, reverifyCacheEntry, scanCache, unpinCache } from '../api/cache'

function humanSize(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

export default function CacheView() {
  const [entries, setEntries] = useState<CacheEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setEntries(await listCache())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load cache')
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

  const scan = async () => {
    try {
      const res = await scanCache()
      message.success(`Scan: ${res?.scanned ?? 0} scanned, ${res?.updated ?? 0} updated, ${res?.orphans ?? 0} orphans`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'scan failed')
    }
  }

  const columns: ColumnsType<CacheEntry> = [
    { title: 'OS', dataIndex: 'os', key: 'os' },
    { title: 'Version', dataIndex: 'version', key: 'version' },
    { title: 'Arch', dataIndex: 'arch', key: 'arch' },
    { title: 'Size', key: 'size', render: (_, e) => humanSize(e.size) },
    { title: 'State', dataIndex: 'state', key: 'state', render: (s: string) => <Tag>{s}</Tag> },
    { title: 'Pinned', key: 'pinned', render: (_, e) => (e.pinned ? 'Yes' : 'No') },
    { title: 'Fetched', dataIndex: 'fetchedAt', key: 'fetchedAt' },
    {
      title: 'Verified',
      key: 'verified',
      render: (_, e) => {
        if (e.verified === true) return <Tag color="green">✓</Tag>
        if (e.verified === false)
          return (
            <Tooltip title={e.verifyErr || 'verification failed'}>
              <Tag color="red">✗</Tag>
            </Tooltip>
          )
        return <Tag>—</Tag>
      },
    },
    {
      title: 'Actions',
      key: 'actions',
      render: (_, e) => (
        <Space>
          {e.pinned ? (
            <Button size="small" onClick={() => act(() => unpinCache(e.id), `Unpinned ${e.version}`)}>Unpin</Button>
          ) : (
            <Button size="small" onClick={() => act(() => pinCache(e.id), `Pinned ${e.version}`)}>Pin</Button>
          )}
          <Button size="small" onClick={() => act(() => reverifyCacheEntry(e.id), `Reverified ${e.version}`)}>Reverify</Button>
          <Tooltip title="available after authentication (P10)">
            <Button size="small" danger disabled>Delete</Button>
          </Tooltip>
        </Space>
      ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Typography.Title level={3} style={{ margin: 0 }}>Cache</Typography.Title>
        <Button onClick={scan}>Scan</Button>
      </Space>
      {error && <Alert type="error" message={error} showIcon />}
      <Table rowKey="id" loading={loading} columns={columns} dataSource={entries} pagination={false} />
    </Space>
  )
}
