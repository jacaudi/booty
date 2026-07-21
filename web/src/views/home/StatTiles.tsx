import { Link } from 'react-router-dom'
import { Card, Col, Row, Statistic, Typography } from 'antd'
import { isPending, type Host } from '../../api/types'
import type { CacheEntry } from '../../api/cache'
import type { Config } from '../../api/configs'
import type { Cluster } from '../../api/clusters'
import { failedCount, humanSize } from '../../api/cacheModel'

// Presentational: HomeView owns the single data fetch and passes it down, so a
// host/cache/etc. list is fetched once for the whole dashboard, not per panel.
type Props = { hosts: Host[]; cache: CacheEntry[]; configs: Config[]; clusters: Cluster[] }
type Tile = { title: string; value: number; sub: string; to: string; highlight?: boolean }

function TileCard({ tile }: { tile: Tile }) {
  return (
    <Col xs={24} sm={12} xl={6}>
      <Link to={tile.to}>
        {/* height:100% so all four tiles align even when a subtext line wraps */}
        <Card hoverable style={{ height: '100%' }}>
          <Statistic title={tile.title} value={tile.value} />
          <Typography.Text type={tile.highlight ? 'warning' : 'secondary'}>{tile.sub}</Typography.Text>
        </Card>
      </Link>
    </Col>
  )
}

export default function StatTiles({ hosts, cache, configs, clusters }: Props) {
  const pending = hosts.filter(isPending).length
  const bytes = cache.reduce((sum, e) => sum + (e.size ?? 0), 0)
  const failed = failedCount(cache)
  const names = clusters.map((c) => c.name).filter(Boolean)

  const tiles: Tile[] = [
    {
      title: 'Hosts', value: hosts.length, to: '/hosts', highlight: pending > 0,
      sub: pending > 0 ? `${pending} pending approval` : hosts.length ? 'all approved' : 'none yet',
    },
    {
      title: 'OS Images', value: cache.length, to: '/images', highlight: failed > 0,
      sub: failed > 0 ? `${humanSize(bytes)} · ${failed} failed` : cache.length ? humanSize(bytes) : 'none cached',
    },
    {
      title: 'Boot Configs', value: configs.length, to: '/boot-configs',
      sub: configs.length ? 'ready to assign' : 'none yet',
    },
    {
      title: 'Clusters', value: clusters.length, to: '/clusters',
      sub: names.length ? names.slice(0, 2).join(', ') + (names.length > 2 ? '…' : '') : 'none yet',
    },
  ]

  return <Row gutter={[16, 16]}>{tiles.map((t) => <TileCard key={t.title} tile={t} />)}</Row>
}
