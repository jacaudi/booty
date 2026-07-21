import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, Skeleton, Space, Typography } from 'antd'
import { listHosts } from '../api/client'
import { listCache } from '../api/cache'
import { listConfigs } from '../api/configs'
import { listClusters } from '../api/clusters'
import StatTiles from './home/StatTiles'
import SystemStatus from './home/SystemStatus'
import PendingApprovals from './home/PendingApprovals'
import CacheHealth from './home/CacheHealth'
import QuickActions from './home/QuickActions'

export default function HomeView() {
  const [fresh, setFresh] = useState<boolean>()
  useEffect(() => {
    Promise.allSettled([listHosts(), listCache(), listConfigs(), listClusters()]).then((rs) => {
      const total = rs.reduce((n, r) => n + (r.status === 'fulfilled' ? r.value.length : 0), 0)
      setFresh(total === 0)
    })
  }, [])

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ marginBottom: 0 }}>Booty</Typography.Title>
      {fresh === undefined ? (
        <Skeleton active />
      ) : fresh ? (
        <Card title="Get started">
          <Space direction="vertical">
            <Typography.Text>No hosts, configs, images, or clusters yet.</Typography.Text>
            <Link to="/hosts">Approve your first host</Link>
            <Link to="/images">Add an OS image target</Link>
          </Space>
        </Card>
      ) : (
        <>
          <StatTiles />
          <PendingApprovals />
          <CacheHealth />
          <Space align="start" wrap size="large">
            <SystemStatus />
            <QuickActions />
          </Space>
        </>
      )}
    </Space>
  )
}
