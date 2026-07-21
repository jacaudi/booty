import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, Button, Card, List, message, Space, Typography } from 'antd'
import { approveHost } from '../../api/client'
import { isPending, type Host } from '../../api/types'

// The dashboard's single "act on this now" surface: unapproved hosts (with
// inline Approve) and cached images that failed verification. Presentational —
// HomeView owns the fetch and passes the data + a reload callback.
type Props = { hosts: Host[]; failedImages: number; onChange: () => void }

export default function NeedsAttention({ hosts, failedImages, onChange }: Props) {
  const pending = hosts.filter(isPending)
  const [busy, setBusy] = useState<string>()

  const approve = async (mac: string) => {
    setBusy(mac)
    try {
      await approveHost(mac)
      message.success('Host approved')
      onChange()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'approve failed')
    } finally {
      setBusy(undefined)
    }
  }

  const clear = pending.length === 0 && failedImages === 0

  return (
    <Card title="Needs attention" style={{ height: '100%' }} extra={<Link to="/hosts">All hosts</Link>}>
      {clear ? (
        <Typography.Text type="secondary">Nothing needs attention — hosts approved, images verified.</Typography.Text>
      ) : (
        <Space direction="vertical" size="middle" style={{ width: '100%', display: 'flex' }}>
          {failedImages > 0 && (
            <Alert
              type="warning"
              showIcon
              message={`${failedImages} cached image${failedImages === 1 ? '' : 's'} failed verification`}
              description={<Link to="/images">Review in OS Images</Link>}
            />
          )}
          {pending.length > 0 && (
            <List
              size="small"
              header={<Typography.Text strong>Pending host approvals</Typography.Text>}
              dataSource={pending}
              renderItem={(h) => (
                <List.Item
                  actions={[
                    <Button key="a" type="primary" size="small" loading={busy === h.mac} onClick={() => approve(h.mac)}>
                      Approve
                    </Button>,
                  ]}
                >
                  <Space direction="vertical" size={0}>
                    <Typography.Text strong>{h.hostname || h.mac}</Typography.Text>
                    <Typography.Text type="secondary">{h.mac}{h.os ? ` · ${h.os}` : ''}</Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
          )}
        </Space>
      )}
    </Card>
  )
}
