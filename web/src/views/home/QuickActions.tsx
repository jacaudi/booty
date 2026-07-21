import { Link } from 'react-router-dom'
import { Button, Card, Space } from 'antd'

export default function QuickActions() {
  return (
    <Card size="small" title="Quick actions" style={{ width: '100%' }}>
      <Space wrap>
        <Link to="/hosts"><Button>Approve hosts</Button></Link>
        <Link to="/images"><Button>OS Images</Button></Link>
        <Link to="/clusters"><Button>Clusters</Button></Link>
      </Space>
    </Card>
  )
}
