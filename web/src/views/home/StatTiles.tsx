import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, Card, Col, Row, Skeleton, Statistic, Typography } from 'antd'
import { listHosts } from '../../api/client'
import { listCache } from '../../api/cache'
import { listConfigs } from '../../api/configs'
import { listClusters } from '../../api/clusters'
import { humanSize } from '../../api/cacheModel'

type Tile = { title: string; value: string | number; sub?: string; to: string; highlight?: boolean }

function TileCard({ tile }: { tile: Tile }) {
  return (
    <Col xs={24} sm={12} lg={6}>
      <Link to={tile.to}>
        <Card hoverable>
          <Statistic title={tile.title} value={tile.value} />
          {tile.sub && (
            <Typography.Text type={tile.highlight ? 'warning' : 'secondary'}>{tile.sub}</Typography.Text>
          )}
        </Card>
      </Link>
    </Col>
  )
}

export default function StatTiles() {
  const [tiles, setTiles] = useState<Tile[]>()
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let live = true
    Promise.allSettled([listHosts(), listCache(), listConfigs(), listClusters()]).then((rs) => {
      if (!live) return
      const [h, c, cf, cl] = rs
      const hosts = h.status === 'fulfilled' ? h.value : []
      const entries = c.status === 'fulfilled' ? c.value : []
      const cfgs = cf.status === 'fulfilled' ? cf.value : []
      const cls = cl.status === 'fulfilled' ? cl.value : []
      setFailed(rs.some((r) => r.status === 'rejected'))
      const pending = hosts.filter((x) => x.approved === false).length
      const bytes = entries.reduce((sum, e) => sum + (e.size ?? 0), 0)
      setTiles([
        { title: 'Hosts', value: hosts.length, to: '/hosts', highlight: pending > 0, sub: pending > 0 ? `${pending} pending approval` : 'all approved' },
        { title: 'OS Images', value: entries.length, to: '/images', sub: humanSize(bytes) },
        { title: 'Boot Configs', value: cfgs.length, to: '/boot-configs' },
        { title: 'Clusters', value: cls.length, to: '/clusters' },
      ])
    })
    return () => { live = false }
  }, [])

  if (!tiles) return <Skeleton active />
  return (
    <>
      {failed && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="Couldn't load some dashboard stats — showing what loaded."
        />
      )}
      <Row gutter={[16, 16]}>{tiles.map((t) => <TileCard key={t.title} tile={t} />)}</Row>
    </>
  )
}
