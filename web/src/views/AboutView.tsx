import { useEffect, useState } from 'react'
import { Descriptions, Typography } from 'antd'

interface Info {
  booty?: { version?: string; timestamp?: string }
}

export default function AboutView() {
  const [info, setInfo] = useState<Info>({})
  useEffect(() => {
    fetch('/info')
      .then((r) => r.json())
      .then(setInfo)
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
