import { useEffect, useState } from 'react'
import { Badge, Card, Space, Typography } from 'antd'
import { checkHealth, type Health } from '../../api/health'

export default function SystemStatus() {
  const [health, setHealth] = useState<Health>()
  useEffect(() => { checkHealth().then(setHealth) }, [])
  return (
    <Card size="small" title="System">
      {health === undefined ? (
        <Badge status="processing" text="Checking…" />
      ) : health.ok ? (
        <Space>
          <Badge status="success" text="Healthy" />
          {health.version && <Typography.Text type="secondary">{health.version}</Typography.Text>}
        </Space>
      ) : (
        <Badge status="error" text="Unreachable" />
      )}
    </Card>
  )
}
