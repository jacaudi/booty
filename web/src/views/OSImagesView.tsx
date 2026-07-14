import { Space, Tabs, Typography } from 'antd'
import CacheView from './CacheView'
import SchematicsView from './SchematicsView'

// OS Images owns "which image a machine boots": the versions we have cached, and
// the Talos schematics that define them. A schematic is NOT a boot config — no
// serving path can render one (it appears in neither renderConfig nor
// familyAllowsKind, pkg/http/render.go); it is POSTed to the Talos Image Factory,
// which returns a content-addressed derived id that the cache is keyed by. The
// schematic IS the image identity, which is why it lives beside the cache.
//
// Thin router element: both tabs keep their own files and responsibilities.
export default function OSImagesView() {
  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ margin: 0 }}>OS Images</Typography.Title>
      <Tabs
        items={[
          { key: 'cached', label: 'Cached versions', children: <CacheView /> },
          { key: 'schematics', label: 'Schematics', children: <SchematicsView /> },
        ]}
      />
    </Space>
  )
}
