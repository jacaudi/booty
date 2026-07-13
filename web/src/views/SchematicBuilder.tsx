import { Button, Space, Typography } from 'antd'
import type { Config } from '../api/configs'

// TEMPORARY stub — Task 9 replaces this with the full builder.
export default function SchematicBuilder({ config, onBack }: { config: Config | null; onBack: () => void; onSaved: () => void }) {
  return (
    <Space direction="vertical">
      <Button onClick={onBack}>‹ Back to Schematics</Button>
      <Typography.Title level={4}>{config ? `Edit ${config.name}` : 'New schematic'}</Typography.Title>
      <Typography.Text type="secondary">Builder coming in Task 9.</Typography.Text>
    </Space>
  )
}
