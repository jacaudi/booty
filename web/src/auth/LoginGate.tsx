import { useEffect, useState } from 'react'
import { Alert, Button, Card, Form, Input, Layout, Typography } from 'antd'
import { login, onUnauthorized } from '../api/client'

// LoginGate wraps the app shell. It renders its children until any API call
// reports a 401, then swaps in a token prompt. The token is posted to /login
// and exchanged for an httpOnly cookie; it is never stored in JS.
export default function LoginGate({ children }: { children: React.ReactNode }) {
  const [locked, setLocked] = useState(false)
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    onUnauthorized(() => setLocked(true))
  }, [])

  if (!locked) return <>{children}</>

  const unlock = async () => {
    setBusy(true)
    setError('')
    try {
      await login(token)
      setToken('')
      setLocked(false)
    } catch {
      setError('That token was not accepted. Check the value booty logged on first run.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Layout style={{ minHeight: '100vh', alignItems: 'center', justifyContent: 'center' }}>
      <Card style={{ maxWidth: 480, width: '100%' }}>
        <Typography.Title level={4}>Unlock Booty</Typography.Title>
        <Typography.Paragraph type="secondary">
          Paste the API token booty logged on first run, or the value in
          <Typography.Text code>&lt;dataDir&gt;/api-token</Typography.Text>.
        </Typography.Paragraph>
        {/* aria-label on the input below is set EXPLICITLY rather than relying
            on Form.Item's htmlFor wiring up getByLabelText, which is
            version-dependent. Alert's own role="alert" IS reliable on the
            installed antd 5.x (verified against the rendered DOM), so it is
            used directly rather than wrapped in a second role="alert" node,
            which would give getByRole('alert') two matches instead of one. */}
        {error ? <Alert type="error" message={error} style={{ marginBottom: 16 }} /> : null}
        <Form layout="vertical" onFinish={unlock}>
          <Form.Item label="API token" htmlFor="api-token">
            <Input.Password
              id="api-token"
              aria-label="API token"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={busy} disabled={!token}>
            Unlock
          </Button>
        </Form>
      </Card>
    </Layout>
  )
}
