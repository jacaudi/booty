import { useEffect, useState } from 'react'
import { Descriptions, Typography } from 'antd'
import { request } from '../api/client'

interface Info {
  booty?: { version?: string; timestamp?: string }
}

export default function AboutView() {
  const [info, setInfo] = useState<Info>({})
  useEffect(() => {
    // /api/v1/info is gated, so this MUST go through request(): a raw
    // fetch('/info') would take its 401 outside the login interceptor and the
    // view would silently render "unknown" forever.
    request<Info>('/info')
      .then((body) => setInfo(body ?? {}))
      .catch(() => setInfo({}))
  }, [])
  return (
    <Typography>
      <Typography.Title level={3}>About</Typography.Title>
      <Descriptions column={1}>
        <Descriptions.Item label="Version">{info.booty?.version ?? 'unknown'}</Descriptions.Item>
        <Descriptions.Item label="Built">{info.booty?.timestamp ?? 'unknown'}</Descriptions.Item>
      </Descriptions>
      <Typography.Paragraph>
        <a href="https://github.com/jacaudi/booty" target="_blank" rel="noreferrer">
          github.com/jacaudi/booty
        </a>
      </Typography.Paragraph>
    </Typography>
  )
}
