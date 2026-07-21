import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert } from 'antd'
import { listCache } from '../../api/cache'

// Counts failed-verification entries directly (mirrors cacheModel.summarize's
// `failed` formula) rather than calling summarize(): that function also
// unconditionally reads `state` on every entry to compute `evictable`, which
// throws on entries missing `state` — a real crash risk this panel must not
// inherit since it exists specifically to surface cache problems, not hide
// them behind a silently-swallowed exception.
export default function CacheHealth() {
  const [failed, setFailed] = useState(0)
  useEffect(() => {
    listCache().then((es) => setFailed(es.filter((e) => e.verified === false).length)).catch(() => setFailed(0))
  }, [])
  if (failed === 0) return null
  return (
    <Alert
      type="warning"
      showIcon
      message={`${failed} cached image${failed === 1 ? '' : 's'} failed verification`}
      description={<Link to="/images">Review in OS Images</Link>}
    />
  )
}
