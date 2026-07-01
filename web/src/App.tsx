import { Layout, Menu } from 'antd'
import { Link, Route, Routes, useLocation } from 'react-router-dom'
import { navEntries } from './nav'

const { Header, Content } = Layout

export default function App() {
  const location = useLocation()
  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ display: 'flex', alignItems: 'center' }}>
        <div style={{ color: '#fff', fontWeight: 'bold', marginRight: 24 }}>Booty</div>
        <Menu
          theme="dark"
          mode="horizontal"
          style={{ flex: 1, minWidth: 0 }}
          selectedKeys={[location.pathname]}
          items={navEntries.map((e) => ({
            key: e.path,
            label: <Link to={e.path}>{e.label}</Link>,
          }))}
        />
      </Header>
      <Content style={{ padding: 24 }}>
        <Routes>
          {navEntries.map((e) => (
            <Route key={e.path} path={e.path} element={e.element} />
          ))}
        </Routes>
      </Content>
    </Layout>
  )
}
