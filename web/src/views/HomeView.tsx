import { Typography } from 'antd'
import { Link } from 'react-router-dom'

// Minimal landing page. The full operations dashboard is a later slice (P7).
export default function HomeView() {
  return (
    <Typography>
      <Typography.Title level={3}>Booty</Typography.Title>
      <Typography.Paragraph>
        Network-boot management plane. Go to <Link to="/hosts">Hosts</Link> to
        approve and manage machines.
      </Typography.Paragraph>
    </Typography>
  )
}
