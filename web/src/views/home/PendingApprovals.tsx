import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button, Card, List, message, Space, Typography } from 'antd'
import { approveHost, listHosts } from '../../api/client'
import type { Host } from '../../api/types'

export default function PendingApprovals() {
  const [pending, setPending] = useState<Host[]>()
  const load = () => listHosts().then((hs) => setPending(hs.filter((h) => h.approved === false))).catch(() => setPending([]))
  useEffect(() => { load() }, [])

  const approve = async (mac: string) => {
    try {
      await approveHost(mac)
      message.success('Host approved')
      load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'approve failed')
    }
  }

  if (!pending || pending.length === 0) return null // "no pending work" → omit the panel entirely
  return (
    <Card title="Pending host approvals" extra={<Link to="/hosts">All hosts</Link>}>
      <List
        dataSource={pending}
        renderItem={(h) => (
          <List.Item actions={[<Button key="a" type="primary" size="small" onClick={() => approve(h.mac)}>Approve</Button>]}>
            <Space direction="vertical" size={0}>
              <Typography.Text strong>{h.hostname || h.mac}</Typography.Text>
              <Typography.Text type="secondary">{h.mac}{h.os ? ` · ${h.os}` : ''}</Typography.Text>
            </Space>
          </List.Item>
        )}
      />
    </Card>
  )
}
