import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert, Button, Card, Checkbox, Collapse, Input, Segmented, Select, Space, Statistic,
  Table, Tag, Tooltip, Typography, message, theme,
} from 'antd'
import { CheckCircleFilled, CloseCircleFilled, PushpinOutlined, SearchOutlined } from '@ant-design/icons'
import type { CacheEntry } from '../api/cache'
import { listCache, pinCache, reverifyCacheEntry, scanCache, unpinCache } from '../api/cache'
import { applyClientFilters, groupEntries, humanSize, summarize } from '../api/cacheModel'

type StateFilter = 'All' | 'In cycle' | 'Archived' | 'Pinned' | 'Failed'

// Status colors come from AntD's design TOKENS (colorSuccess/colorError), never
// hand-picked hex — they must track the active light/dark algorithm.
function VerifyIcon({ entry }: { entry: CacheEntry }) {
  const { token } = theme.useToken()
  if (entry.verified === true) return <CheckCircleFilled style={{ color: token.colorSuccess }} aria-label="verified" />
  if (entry.verified === false)
    return (
      <Tooltip title={entry.verifyErr || 'verification failed'}>
        <CloseCircleFilled style={{ color: token.colorError }} aria-label="verification failed" />
      </Tooltip>
    )
  return <Typography.Text type="secondary">—</Typography.Text>
}

export default function CacheView() {
  const { token } = theme.useToken()
  const [entries, setEntries] = useState<CacheEntry[]>([])
  // Unfiltered snapshot: the summary strip must describe the WHOLE cache, not the
  // current selection (SGE I5) — otherwise "Archived: 0" while the In-cycle filter
  // is active is simply false.
  const [allEntries, setAllEntries] = useState<CacheEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [stateFilter, setStateFilter] = useState<StateFilter>('All')
  const [os, setOs] = useState<string | undefined>(undefined)
  const [version, setVersion] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [selectedRows, setSelectedRows] = useState<Set<number>>(new Set())

  // The SERVER filter — note that 'All' and 'Failed' produce the SAME server
  // filter (Failed is client-side; the API has no failed param). load() must
  // depend on THIS, not on stateFilter, or switching to Failed triggers a
  // pointless refetch (SGE B5a). Keying on the serialized filter keeps load()'s
  // identity stable whenever the server query is unchanged.
  const serverFilter = useMemo(() => {
    const f: { os?: string; state?: 'in-cycle' | 'archived'; pinned?: boolean } = {}
    if (os) f.os = os
    if (stateFilter === 'In cycle') f.state = 'in-cycle'
    if (stateFilter === 'Archived') f.state = 'archived'
    if (stateFilter === 'Pinned') f.pinned = true
    return f
  }, [os, stateFilter])
  const serverFilterKey = JSON.stringify(serverFilter)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const filter = JSON.parse(serverFilterKey) as typeof serverFilter
      const hasFilter = Object.keys(filter).length > 0
      // One unfiltered read for the strip + one filtered read for the list. When no
      // server filter is active they are the same query, so issue it once.
      if (hasFilter) {
        const [filtered, all] = await Promise.all([listCache(filter), listCache()])
        setEntries(filtered)
        setAllEntries(all)
      } else {
        const all = await listCache()
        setEntries(all)
        setAllEntries(all)
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load cache')
    } finally {
      setLoading(false)
    }
  }, [serverFilterKey])

  useEffect(() => {
    load()
  }, [load])

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn()
      message.success(ok)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'action failed')
    }
  }

  const scan = async () => {
    try {
      const res = await scanCache()
      message.success(`Scan: ${res?.scanned ?? 0} scanned, ${res?.updated ?? 0} updated, ${res?.orphans ?? 0} orphans`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : 'scan failed')
    }
  }

  // Client-side filters layered over the server result (version substring +
  // the "Failed" pseudo-state, which has no server query param — design §4.1).
  const visible = useMemo(
    () => applyClientFilters(entries, { version: version || undefined, failedOnly: stateFilter === 'Failed' }),
    [entries, version, stateFilter],
  )
  const groups = useMemo(() => groupEntries(visible), [visible])
  // Strip + OS options come from the UNFILTERED snapshot (SGE I5): otherwise the
  // OS Select collapses to only the already-selected OS and can never be changed.
  const summary = useMemo(() => summarize(allEntries), [allEntries])
  const osOptions = useMemo(
    () => [...new Set(allEntries.map((e) => e.os))].sort().map((o) => ({ value: o, label: o })),
    [allEntries],
  )
  const selected = useMemo(() => entries.find((e) => e.id === selectedId) ?? null, [entries, selectedId])

  // Collapse must be CONTROLLED here: `defaultActiveKey` is only read on the
  // component's initial mount, but groups don't exist yet on that first render
  // (listCache() resolves asynchronously) — so an uncontrolled Collapse's panels
  // never become active and rc-collapse never mounts their content. Syncing an
  // `activeKey` state to the freshly-computed groups keeps newly-appearing
  // groups expanded by default while still letting the user collapse one.
  const [activeKeys, setActiveKeys] = useState<string[]>([])
  useEffect(() => {
    setActiveKeys(groups.map((g) => g.key))
  }, [groups])

  const bulk = async (fn: (id: number) => Promise<unknown>, ok: string) => {
    const ids = [...selectedRows]
    if (ids.length === 0) return
    // Client-side fan-out over the single-item routes: no bulk endpoint exists
    // (design §4.1 / Task 16 deferred). Await all before reload.
    await Promise.allSettled(ids.map((id) => fn(id)))
    message.success(ok)
    setSelectedRows(new Set())
    await load()
  }

  const toggleRow = (id: number, checked: boolean) => {
    setSelectedRows((prev) => {
      const next = new Set(prev)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const versionColumns = [
    {
      title: '',
      key: 'select',
      width: 40,
      render: (_: unknown, e: CacheEntry) => (
        <Checkbox
          checked={selectedRows.has(e.id)}
          onChange={(ev) => toggleRow(e.id, ev.target.checked)}
          onClick={(ev) => ev.stopPropagation()}
        />
      ),
    },
    { title: 'Version', dataIndex: 'version', key: 'version' },
    {
      title: 'State',
      key: 'state',
      render: (_: unknown, e: CacheEntry) => (
        <Space size={4}>
          <Tag>{e.state}</Tag>
          {e.pinned && <PushpinOutlined aria-label="pinned" />}
        </Space>
      ),
    },
    { title: 'Verify', key: 'verify', render: (_: unknown, e: CacheEntry) => <VerifyIcon entry={e} /> },
    { title: 'Size', key: 'size', render: (_: unknown, e: CacheEntry) => humanSize(e.size) },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Typography.Title level={3} style={{ margin: 0 }}>Cache</Typography.Title>
        <Button icon={<SearchOutlined />} onClick={scan}>Scan now</Button>
      </Space>

      {error && <Alert type="error" message={error} showIcon />}

      {/* Whole-cache summary (NOT the current selection) — see allEntries above.
          No budget denominator / Progress bar: --cacheMaxBytes is exposed by no
          endpoint (SGE C1). data-testid lets tests scope a count unambiguously,
          since "Failed"/"In cycle" also appear as Segmented option labels. */}
      <Space size="large" wrap>
        <Card size="small" data-testid="summary-used"><Statistic title="Used" value={humanSize(summary.usedBytes)} /></Card>
        <Card size="small" data-testid="summary-incycle"><Statistic title="In cycle" value={summary.inCycle} /></Card>
        <Card size="small" data-testid="summary-archived"><Statistic title="Archived" value={summary.archived} /></Card>
        <Card size="small" data-testid="summary-pinned"><Statistic title="Pinned" value={summary.pinned} /></Card>
        <Card
          size="small"
          data-testid="summary-failed"
          style={summary.failed ? { borderColor: token.colorError } : undefined}
        >
          <Statistic
            title="Failed"
            value={summary.failed}
            valueStyle={summary.failed ? { color: token.colorError } : undefined}
          />
        </Card>
      </Space>

      {summary.nothingEvictable && summary.archived > 0 && (
        <Alert type="warning" showIcon message="Every archived version is pinned — nothing is evictable." />
      )}
      {summary.failed > 0 && (
        <Alert
          type="error"
          showIcon
          message={`${summary.failed} version(s) failed verification.`}
          action={<Button size="small" onClick={() => setStateFilter('Failed')}>Show failed</Button>}
        />
      )}

      <Space wrap>
        <Segmented
          value={stateFilter}
          onChange={(v) => setStateFilter(v as StateFilter)}
          options={['All', 'In cycle', 'Archived', 'Pinned', 'Failed']}
        />
        <Select
          allowClear
          placeholder="All OS"
          style={{ width: 160 }}
          value={os}
          onChange={(v) => setOs(v)}
          options={osOptions}
        />
        <Input.Search
          allowClear
          placeholder="version"
          style={{ width: 200 }}
          value={version}
          onChange={(e) => setVersion(e.target.value)}
        />
        <Button disabled={selectedRows.size === 0} onClick={() => bulk(pinCache, 'Pinned selected')}>Pin all</Button>
        <Button disabled={selectedRows.size === 0} onClick={() => bulk(unpinCache, 'Unpinned selected')}>Unpin all</Button>
        <Button disabled={selectedRows.size === 0} onClick={() => bulk(reverifyCacheEntry, 'Re-verified selected')}>Re-verify all</Button>
      </Space>

      <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
        <div style={{ flex: 2, minWidth: 0 }}>
          <Collapse
            activeKey={activeKeys}
            onChange={(keys) => setActiveKeys(keys as string[])}
            items={groups.map((g) => ({
              key: g.key,
              label: (
                <Space>
                  <Typography.Text strong>{g.key}</Typography.Text>
                  <Typography.Text type="secondary">
                    {g.versionCount} version(s) · {humanSize(g.totalSize)}
                  </Typography.Text>
                </Space>
              ),
              children: (
                <Table
                  rowKey="id"
                  size="small"
                  loading={loading}
                  columns={versionColumns}
                  dataSource={g.entries}
                  pagination={false}
                  onRow={(e) => ({ onClick: () => setSelectedId(e.id) })}
                  rowClassName={(e) => (e.id === selectedId ? 'ant-table-row-selected' : '')}
                />
              ),
            }))}
          />
        </div>

        <Card data-testid="cache-detail" style={{ flex: 1, minWidth: 280 }} title={selected ? selected.version : 'No selection'}>
          {selected ? (
            <Space direction="vertical" style={{ width: '100%' }}>
              <Space><VerifyIcon entry={selected} /><Typography.Text>{selected.os} / {selected.arch}</Typography.Text></Space>
              <Typography.Text type="secondary">{humanSize(selected.size)} · {selected.state}</Typography.Text>
              <Space>
                {selected.pinned ? (
                  <Button onClick={() => act(() => unpinCache(selected.id), `Unpinned ${selected.version}`)}>Unpin</Button>
                ) : (
                  <Button onClick={() => act(() => pinCache(selected.id), `Pinned ${selected.version}`)}>Pin</Button>
                )}
                <Button onClick={() => act(() => reverifyCacheEntry(selected.id), `Re-verified ${selected.version}`)}>Re-verify</Button>
                <Tooltip title="available after authentication (P10)">
                  <Button danger disabled>Delete</Button>
                </Tooltip>
              </Space>
              {/* Files: per-file sha256 / verify-kind breakdown is not persisted by
                  the backend today (deferred — see issue #33). Documented stub. */}
              <Alert type="info" showIcon message="Per-file checksum breakdown is not yet available (see #33)." />
            </Space>
          ) : (
            <Typography.Text type="secondary">Select a version to see details.</Typography.Text>
          )}
        </Card>
      </div>
    </Space>
  )
}
