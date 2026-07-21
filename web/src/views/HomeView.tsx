import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, Card, Col, Row, Skeleton, Space, Typography } from 'antd'
import { listHosts } from '../api/client'
import { listCache } from '../api/cache'
import { listConfigs } from '../api/configs'
import { listClusters } from '../api/clusters'
import type { Host } from '../api/types'
import type { CacheEntry } from '../api/cache'
import type { Config } from '../api/configs'
import type { Cluster } from '../api/clusters'
import { failedCount } from '../api/cacheModel'
import StatTiles from './home/StatTiles'
import NeedsAttention from './home/NeedsAttention'
import SystemStatus from './home/SystemStatus'
import QuickActions from './home/QuickActions'

type DashData = { hosts: Host[]; cache: CacheEntry[]; configs: Config[]; clusters: Cluster[]; partial: boolean }

// The dashboard fetches all four lists ONCE here and passes them down, so the
// tiles and the needs-attention panel share one load (no per-panel refetch) and
// `load()` can refresh everything after an inline approve. allSettled tolerates
// a single failed endpoint: that list falls back to empty and a banner shows.
export default function HomeView() {
  const [data, setData] = useState<DashData>()

  const load = useCallback(() => {
    Promise.allSettled([listHosts(), listCache(), listConfigs(), listClusters()]).then((rs) => {
      const [h, c, cf, cl] = rs
      setData({
        hosts: h.status === 'fulfilled' ? h.value : [],
        cache: c.status === 'fulfilled' ? c.value : [],
        configs: cf.status === 'fulfilled' ? cf.value : [],
        clusters: cl.status === 'fulfilled' ? cl.value : [],
        partial: rs.some((r) => r.status === 'rejected'),
      })
    })
  }, [])
  useEffect(() => { load() }, [load])

  // A fresh install (everything empty, nothing failed to load) gets a focused
  // getting-started card instead of a board of zeros.
  const total = data ? data.hosts.length + data.cache.length + data.configs.length + data.clusters.length : 0
  const fresh = data !== undefined && total === 0 && !data.partial

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ marginBottom: 0 }}>Booty</Typography.Title>
      {data === undefined ? (
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
          {data.partial && (
            <Alert type="warning" showIcon message="Couldn't load some dashboard stats — showing what loaded." />
          )}
          <StatTiles hosts={data.hosts} cache={data.cache} configs={data.configs} clusters={data.clusters} />
          <Row gutter={[16, 16]}>
            <Col xs={24} lg={16}>
              <NeedsAttention hosts={data.hosts} failedImages={failedCount(data.cache)} onChange={load} />
            </Col>
            <Col xs={24} lg={8}>
              <Space direction="vertical" size="large" style={{ width: '100%', display: 'flex' }}>
                <QuickActions />
                <SystemStatus />
              </Space>
            </Col>
          </Row>
        </>
      )}
    </Space>
  )
}
