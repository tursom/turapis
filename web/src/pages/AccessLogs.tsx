import { useState, useEffect, useRef, lazy, Suspense } from 'react'
import { fetchAccessLogs, fetchAccessLogDetail, fetchAccessLogStats } from '../api/accessLogs'
import { fetchAPIKeys } from '../api/client'
import type { AccessLog, APIKeyListItem, AttemptRecord, AccessLogStatsResponse } from '../api/types'

const LazyChart = lazy(() => import('../components/AccessLogChart'))

const STATUS_OPTIONS = [
  { value: 0, label: '全部' },
  { value: 200, label: '200' },
  { value: 400, label: '400' },
  { value: 401, label: '401' },
  { value: 500, label: '500' },
]

const formatDatetimeLocal = (d: Date) => {
  const pad = (n: number) => n.toString().padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const quickRangeValues = (hours: number) => {
  const now = new Date()
  const from = new Date(now.getTime() - hours * 60 * 60 * 1000)
  return { from: formatDatetimeLocal(from), to: formatDatetimeLocal(now) }
}

export default function AccessLogs() {
  const initialRange = useRef(quickRangeValues(1))
  const [logs, setLogs] = useState<AccessLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [perPage] = useState(20)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // filters
  const [filterKeyId, setFilterKeyId] = useState<number | undefined>(undefined)
  const [filterModel, setFilterModel] = useState('')
  const [filterStatus, setFilterStatus] = useState(0)
  const [filterFrom, setFilterFrom] = useState(initialRange.current.from)
  const [filterTo, setFilterTo] = useState(initialRange.current.to)

  const [apiKeys, setApiKeys] = useState<APIKeyListItem[]>([])

  // detail modal
  const [selectedLogId, setSelectedLogId] = useState<number | null>(null)
  const [detailData, setDetailData] = useState<AccessLog | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [hoveredRowId, setHoveredRowId] = useState<number | null>(null)
  const [expandedSections, setExpandedSections] = useState<Record<string, boolean>>({
    client_req: true,
    client_resp: true,
    upstream_req: true,
    upstream_resp: true,
  })

  // tabs & stats
  const [activeTab, setActiveTab] = useState<string>('logs')
  const [statsData, setStatsData] = useState<AccessLogStatsResponse | null>(null)
  const [statsLoading, setStatsLoading] = useState(false)
  const [statsError, setStatsError] = useState('')
  const statsCacheRef = useRef<{ key: string; data: AccessLogStatsResponse } | null>(null)
  const listAbortRef = useRef<AbortController | null>(null)

  const [quickRange, setQuickRangeState] = useState('')

  const totalPages = Math.max(1, Math.ceil(total / perPage))

  const datetimeLocalToMillis = (value: string) => {
    if (!value) return undefined
    const ms = new Date(value).getTime()
    return Number.isNaN(ms) ? undefined : ms
  }

  const setQuickRange = (hours: number) => {
    const range = quickRangeValues(hours)
    setFilterFrom(range.from)
    setFilterTo(range.to)
    setQuickRangeState('')
  }

  useEffect(() => {
    fetchAPIKeys().then(setApiKeys).catch(() => {})
  }, [])

  const buildQuery = (p: number) => {
    const from = datetimeLocalToMillis(filterFrom)
    const to = datetimeLocalToMillis(filterTo)
    return {
      key_id: filterKeyId,
      model: filterModel || undefined,
      status: filterStatus || undefined,
      from,
      to,
      page: p,
      per_page: perPage,
    }
  }

  const isAbortError = (e: unknown) => e instanceof Error && e.name === 'AbortError'

  const load = (p: number) => {
    listAbortRef.current?.abort()
    const controller = new AbortController()
    listAbortRef.current = controller
    setLoading(true)
    setError('')
    fetchAccessLogs(buildQuery(p), controller.signal)
      .then(data => {
        setLogs(data.logs)
        setTotal(data.total)
        setPage(data.page)
      })
      .catch(e => {
        if (!isAbortError(e)) setError(e.message)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
  }

  // reload when filters change (reset to page 1)
  useEffect(() => {
    const timer = window.setTimeout(() => load(1), 250)
    return () => {
      window.clearTimeout(timer)
      listAbortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKeyId, filterModel, filterStatus, filterFrom, filterTo])

  const calculateOptimalInterval = (from: number, to: number): number => {
    const durationMinutes = (to - from) / (60 * 1000)
    const rawInterval = durationMinutes / 40
    const niceValues = [1, 2, 5, 10, 15, 20, 30, 60, 120, 180, 240, 360, 720, 1440]
    for (const v of niceValues) {
      if (v >= rawInterval) return v
    }
    return niceValues[niceValues.length - 1]
  }

  // load stats when tab switches
  useEffect(() => {
    if (activeTab === 'stats' || activeTab === 'tokens') {
      if (!filterFrom || !filterTo) return
      const from = datetimeLocalToMillis(filterFrom)
      const to = datetimeLocalToMillis(filterTo)
      if (from === undefined || to === undefined) return
      const interval = calculateOptimalInterval(from, to)
      const cacheKey = `${from}:${to}:${interval}`
      if (statsCacheRef.current?.key === cacheKey) {
        setStatsData(statsCacheRef.current.data)
        setStatsError('')
        setStatsLoading(false)
        return
      }
      const controller = new AbortController()
      setStatsLoading(true)
      setStatsError('')
      fetchAccessLogStats({ from, to, interval }, controller.signal)
        .then(data => {
          statsCacheRef.current = { key: cacheKey, data }
          setStatsData(data)
        })
        .catch(e => {
          if (!isAbortError(e)) setStatsError(e.message)
        })
        .finally(() => {
          if (!controller.signal.aborted) setStatsLoading(false)
        })
      return () => controller.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, filterFrom, filterTo])

  const goPage = (p: number) => {
    if (p < 1 || p > totalPages) return
    load(p)
  }

  const formatTime = (ts: number) => {
    const d = new Date(ts)
    return d.toLocaleString('zh-CN', {
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    })
  }

  const formatJson = (raw: string): string => {
    if (!raw) return ''
    try {
      return JSON.stringify(JSON.parse(raw), null, 2)
    } catch {
      return raw
    }
  }

  const parseQuota = (raw: string): Record<string, { used_percent?: number; reset_after_seconds?: number; window_minutes?: number }> | null => {
    if (!raw) return null
    try {
      return JSON.parse(raw)
    } catch {
      return null
    }
  }

  const parseAttempts = (raw: string): AttemptRecord[] => {
    if (!raw) return []
    try {
      return JSON.parse(raw)
    } catch {
      return []
    }
  }

  const renderFailoverMini = (raw: string) => {
    const attempts = parseAttempts(raw)
    if (attempts.length === 0) return <span style={{ color: '#ccc', fontSize: 11 }}>-</span>
    const short = (name: string) => name.split('@')[0].slice(0, 10)
    const colorFor = (a: AttemptRecord) => a.success ? '#52c41a' : '#ff4d4f'
    return (
      <span style={{ fontSize: 11, fontFamily: 'monospace', whiteSpace: 'nowrap' }}>
        {attempts.map((a, i) => (
          <span key={i}>
            <span style={{ color: colorFor(a) }}>{short(a.provider)}</span>
            {a.success ? <span style={{ color: '#52c41a' }}>✓</span> : <span style={{ color: '#ff4d4f' }}>({a.status_code || 'err'})</span>}
            {i < attempts.length - 1 && <span style={{ color: '#999', margin: '0 2px' }}>→</span>}
          </span>
        ))}
      </span>
    )
  }

  const formatResetTime = (seconds: number): string => {
    if (seconds <= 0) return '已重置'
    const h = Math.floor(seconds / 3600)
    const m = Math.floor((seconds % 3600) / 60)
    const s = seconds % 60
    if (h > 0) return `${h}h${m}m 后重置`
    if (m > 0) return `${m}m${s}s 后重置`
    return `${s}s 后重置`
  }

  const renderQuotaEntry = (key: string, entry: { used_percent?: number; reset_after_seconds?: number; window_minutes?: number }) => {
    if (!entry || entry.used_percent === undefined) return null
    const color = entry.used_percent > 80 ? '#ff4d4f' : entry.used_percent > 50 ? '#faad14' : '#52c41a'
    return (
      <div key={key} style={{ marginBottom: 6 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: '#999', textTransform: 'uppercase' }}>{key}: </span>
        <span style={{ fontSize: 13, fontWeight: 600, color }}>{entry.used_percent.toFixed(1)}%</span>
        {entry.reset_after_seconds !== undefined && entry.reset_after_seconds > 0 && (
          <span style={{ fontSize: 12, color: '#999', marginLeft: 8 }}>{formatResetTime(entry.reset_after_seconds)}</span>
        )}
      </div>
    )
  }

  const renderQuotaSection = (label: string, quotaJson: string) => {
    const quota = parseQuota(quotaJson)
    if (!quota) {
      return (
        <div style={{ padding: '8px 12px', color: '#ccc', fontSize: 12, fontStyle: 'italic' }}>无数据</div>
      )
    }
    const entries = Object.entries(quota)
    if (entries.length === 0) {
      return (
        <div style={{ padding: '8px 12px', color: '#ccc', fontSize: 12, fontStyle: 'italic' }}>无数据</div>
      )
    }
    return (
      <div style={{ padding: '8px 12px', background: '#fafafa', borderRadius: 4, border: '1px solid #f0f0f0' }}>
        <div style={{ fontSize: 12, fontWeight: 600, color: '#666', marginBottom: 4 }}>{label}</div>
        {entries.map(([key, val]) => renderQuotaEntry(key, val))}
      </div>
    )
  }

  const hasQuotaData = (quotaJson: string) => {
    const quota = parseQuota(quotaJson)
    if (!quota) return false
    return Object.values(quota).some(entry => entry && entry.used_percent !== undefined)
  }

  const renderAttemptQuota = (attempt: AttemptRecord) => {
    if (!hasQuotaData(attempt.quota_before) && !hasQuotaData(attempt.quota_after)) return null
    return (
      <div style={{ marginTop: 8, paddingLeft: 30 }}>
        <div style={{ fontSize: 12, fontWeight: 600, color: '#666', marginBottom: 6 }}>额度变化</div>
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: 8 }}>
          {renderQuotaSection('尝试前', attempt.quota_before)}
          {renderQuotaSection('尝试后', attempt.quota_after)}
        </div>
      </div>
    )
  }

  const handleRowClick = (id: number) => {
    setSelectedLogId(id)
    setDetailData(null)
    setDetailError('')
    setDetailLoading(true)
    fetchAccessLogDetail(id)
      .then(data => setDetailData(data))
      .catch(e => setDetailError(e.message))
      .finally(() => setDetailLoading(false))
  }

  const closeModal = () => {
    setSelectedLogId(null)
    setDetailData(null)
    setDetailError('')
  }

  const toggleSection = (name: string) => {
    setExpandedSections(prev => ({ ...prev, [name]: !prev[name] }))
  }

  const renderAccessChart = () => (
    <Suspense fallback={<p style={{ textAlign: 'center', color: '#999', padding: 40 }}>加载图表组件...</p>}>
      <LazyChart data={statsData} mode="access" />
    </Suspense>
  )

  const renderTokenChart = () => (
    <Suspense fallback={<p style={{ textAlign: 'center', color: '#999', padding: 40 }}>加载图表组件...</p>}>
      <LazyChart data={statsData} mode="token" />
    </Suspense>
  )

  return (
    <div>
      <h2 style={{ marginBottom: 16 }}>访问日志</h2>

      {/* Filter bar */}
      <div style={{
        display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap',
        padding: 12, background: '#fafafa', borderRadius: 6, border: '1px solid #f0f0f0',
        alignItems: 'flex-end',
      }}>
        <label style={{ fontSize: 12, color: '#666' }}>
          时间范围
          <select value={quickRange} onChange={e => { const v = e.target.value; if (v) { setQuickRange(Number(v)); } }}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }}>
            <option value="">自定义</option>
            <option value="1">最近 1 小时</option>
            <option value="12">最近 12 小时</option>
            <option value="24">最近 24 小时</option>
            <option value="72">最近 3 天</option>
            <option value="168">最近 7 天</option>
          </select>
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          开始
          <input type="datetime-local" value={filterFrom} onChange={e => setFilterFrom(e.target.value)}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }} />
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          结束
          <input type="datetime-local" value={filterTo} onChange={e => setFilterTo(e.target.value)}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }} />
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          API Key
          <select value={filterKeyId ?? ''} onChange={e => setFilterKeyId(e.target.value ? Number(e.target.value) : undefined)}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }}>
            <option value="">全部</option>
            {apiKeys.map(k => (
              <option key={k.id} value={k.id}>{k.name}</option>
            ))}
          </select>
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          模型
          <input value={filterModel} onChange={e => setFilterModel(e.target.value)} placeholder="例如：gpt-4"
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }} />
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          状态码
          <select value={filterStatus} onChange={e => setFilterStatus(Number(e.target.value))}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }}>
            {STATUS_OPTIONS.map(o => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
        </label>
      </div>

      {/* Tab bar */}
      <div style={{ display: 'flex', gap: 0, marginBottom: 16, borderBottom: '1px solid #f0f0f0' }}>
        {[{ key: 'logs', label: '日志列表' }, { key: 'stats', label: '访问统计' }, { key: 'tokens', label: 'Token统计' }].map(tab => (
          <button key={tab.key} onClick={() => { if (activeTab !== tab.key) { setActiveTab(tab.key); if (tab.key !== 'logs') { setStatsError(''); } } }}
            style={{
              padding: '8px 20px', border: 'none', background: 'none', cursor: 'pointer', fontSize: 14,
              color: activeTab === tab.key ? '#1677ff' : '#666',
              borderBottom: activeTab === tab.key ? '2px solid #1677ff' : '2px solid transparent',
              marginBottom: -1, fontWeight: activeTab === tab.key ? 500 : 400,
            }}>{tab.label}</button>
        ))}
      </div>

      {/* Tab content */}
      {activeTab === 'logs' && (
        <>
          {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}

          {loading ? <p>加载中...</p> : (
            <>
              <div style={{ marginBottom: 8, fontSize: 13, color: '#666' }}>共 {total} 条记录</div>
              <div style={{ overflowX: 'auto' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left', whiteSpace: 'nowrap' }}>
                      <th style={{ padding: '6px 8px' }}>时间</th>
                      <th style={{ padding: '6px 8px' }}>API Key</th>
                      <th style={{ padding: '6px 8px' }}>方法</th>
                      <th style={{ padding: '6px 8px' }}>路径</th>
                      <th style={{ padding: '6px 8px' }}>模型</th>
                      <th style={{ padding: '6px 8px' }}>状态</th>
                      <th style={{ padding: '6px 8px' }}>Token 入/出</th>
                      <th style={{ padding: '6px 8px' }}>耗时(ms)</th>
                      <th style={{ padding: '6px 8px' }}>供应商</th>
                      <th style={{ padding: '6px 8px' }}>故障转移</th>
                      <th style={{ padding: '6px 8px' }}>错误</th>
                    </tr>
                  </thead>
                  <tbody>
                    {logs.length === 0 ? (
                      <tr><td colSpan={11} style={{ padding: 24, textAlign: 'center', color: '#999' }}>暂无日志记录</td></tr>
                    ) : (
                      logs.map(log => (
                        <tr key={log.id}
                          onClick={() => handleRowClick(log.id)}
                          onMouseEnter={() => setHoveredRowId(log.id)}
                          onMouseLeave={() => setHoveredRowId(null)}
                          style={{
                            borderBottom: '1px solid #f0f0f0',
                            verticalAlign: 'top',
                            cursor: 'pointer',
                            background: hoveredRowId === log.id ? '#f5f5f5' : 'transparent',
                            transition: 'background 0.15s',
                          }}>
                          <td style={{ padding: '6px 8px', whiteSpace: 'nowrap', fontSize: 12, color: '#666' }}>{formatTime(log.timestamp)}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 120, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{log.api_key_name || '-'}</td>
                          <td style={{ padding: '6px 8px', fontFamily: 'monospace', fontSize: 12 }}>{log.method}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontFamily: 'monospace', fontSize: 12 }}>{log.path}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 150, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{log.model || '-'}</td>
                          <td style={{ padding: '6px 8px' }}>
                            <span style={{
                              display: 'inline-block', padding: '1px 6px', borderRadius: 4, fontSize: 12,
                              color: log.status_code >= 200 && log.status_code < 300 ? '#52c41a' :
                                    log.status_code >= 400 ? '#ff4d4f' : '#666',
                              background: log.status_code >= 200 && log.status_code < 300 ? '#f6ffed' :
                                          log.status_code >= 400 ? '#fff2f0' : '#fafafa',
                            }}>{log.status_code}</span>
                          </td>
                          <td style={{ padding: '6px 8px', whiteSpace: 'nowrap', fontFamily: 'monospace', fontSize: 12 }}>{log.tokens_in}/{log.tokens_out}</td>
                          <td style={{ padding: '6px 8px', fontFamily: 'monospace', fontSize: 12, textAlign: 'right' }}>{log.duration_ms}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 100, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{log.provider_name || '-'}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 200, overflow: 'hidden' }}>{renderFailoverMini(log.attempts_json)}</td>
                          <td style={{ padding: '6px 8px', maxWidth: 150, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: '#ff4d4f' }}>{log.error_msg || ''}</td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>

              {/* Pagination */}
              <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', gap: 8, marginTop: 16 }}>
                <button onClick={() => goPage(1)} disabled={page <= 1} style={{ padding: '4px 10px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: page <= 1 ? 'not-allowed' : 'pointer', fontSize: 13, opacity: page <= 1 ? 0.5 : 1 }}>首页</button>
                <button onClick={() => goPage(page - 1)} disabled={page <= 1} style={{ padding: '4px 10px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: page <= 1 ? 'not-allowed' : 'pointer', fontSize: 13, opacity: page <= 1 ? 0.5 : 1 }}>上一页</button>
                <span style={{ fontSize: 13, color: '#666' }}>第 {page} / {totalPages} 页</span>
                <button onClick={() => goPage(page + 1)} disabled={page >= totalPages} style={{ padding: '4px 10px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: page >= totalPages ? 'not-allowed' : 'pointer', fontSize: 13, opacity: page >= totalPages ? 0.5 : 1 }}>下一页</button>
                <button onClick={() => goPage(totalPages)} disabled={page >= totalPages} style={{ padding: '4px 10px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: page >= totalPages ? 'not-allowed' : 'pointer', fontSize: 13, opacity: page >= totalPages ? 0.5 : 1 }}>末页</button>
              </div>
            </>
          )}

          {/* Detail Modal */}
          {selectedLogId !== null && (
            <div
              onClick={(e) => { if (e.target === e.currentTarget) closeModal() }}
              style={{
                position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
                background: 'rgba(0,0,0,0.45)', zIndex: 1000,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
              }}
            >
              <div style={{
                background: '#fff', borderRadius: 8, maxWidth: '80vw', maxHeight: '80vh',
                width: 900, display: 'flex', flexDirection: 'column',
                boxShadow: '0 8px 40px rgba(0,0,0,0.12)',
              }}>
                <div style={{
                  display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                  padding: '16px 20px', borderBottom: '1px solid #f0f0f0', flexShrink: 0,
                }}>
                  <h3 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>请求详情 #{selectedLogId}</h3>
                  <button onClick={closeModal} style={{
                    border: 'none', background: 'none', fontSize: 20, cursor: 'pointer',
                    color: '#999', padding: '0 4px', lineHeight: 1,
                  }} onMouseEnter={(e) => { (e.target as HTMLButtonElement).style.color = '#333' }}
                     onMouseLeave={(e) => { (e.target as HTMLButtonElement).style.color = '#999' }}
                  >&times;</button>
                </div>

                <div style={{ overflowY: 'auto', padding: '16px 20px', flex: 1 }}>
                  {detailLoading && <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>加载中...</p>}
                  {detailError && <div style={{ color: '#ff4d4f', padding: 16, textAlign: 'center' }}>{detailError}</div>}
                  {detailData && (
                    <>
                      <div style={{
                        display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))',
                        gap: '8px 16px', marginBottom: 20,
                      }}>
                        <div><span style={{ color: '#999', fontSize: 12 }}>时间</span><div style={{ fontSize: 13 }}>{formatTime(detailData.timestamp)}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>方法</span><div style={{ fontFamily: 'monospace', fontSize: 13 }}>{detailData.method}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>路径</span><div style={{ fontFamily: 'monospace', fontSize: 13, wordBreak: 'break-all' }}>{detailData.path}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>状态码</span><div style={{ fontSize: 13 }}>{detailData.status_code}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>模型</span><div style={{ fontSize: 13 }}>{detailData.model || '-'}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>供应商</span><div style={{ fontSize: 13 }}>{detailData.provider_name || '-'}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>Token (入/出)</span><div style={{ fontSize: 13 }}>{detailData.tokens_in} / {detailData.tokens_out}</div></div>
                        <div><span style={{ color: '#999', fontSize: 12 }}>耗时</span><div style={{ fontSize: 13 }}>{detailData.duration_ms} ms</div></div>
                        {detailData.error_msg && (
                          <div style={{ gridColumn: '1 / -1' }}>
                            <span style={{ color: '#999', fontSize: 12 }}>错误信息</span>
                            <div style={{
                              fontSize: 13, color: '#ff4d4f', marginTop: 2,
                              padding: '6px 10px', background: '#fff2f0',
                              borderRadius: 4, border: '1px solid #ffccc7',
                              wordBreak: 'break-all', maxHeight: 120, overflowY: 'auto',
                            }}>{detailData.error_msg}</div>
                          </div>
                        )}
                      </div>

                      {(detailData.quota_before || detailData.quota_after) && (
                        <div style={{ marginBottom: 20 }}>
                          <div style={{
                            fontSize: 13, fontWeight: 600, color: '#333', marginBottom: 8,
                            borderBottom: '1px solid #f0f0f0', paddingBottom: 4,
                          }}>额度变化</div>
                          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
                            {renderQuotaSection('调用前', detailData.quota_before)}
                            {renderQuotaSection('调用后', detailData.quota_after)}
                          </div>
                        </div>
                      )}

                      {(() => {
                        const attempts = parseAttempts(detailData.attempts_json)
                        if (attempts.length === 0) return null
                        const short = (n: string) => n.split('@')[0].slice(0, 14)
                        return (
                          <div style={{ marginBottom: 20 }}>
                            <div style={{
                              fontSize: 13, fontWeight: 600, color: '#333', marginBottom: 8,
                              borderBottom: '1px solid #f0f0f0', paddingBottom: 4,
                            }}>故障转移链 ({attempts.length} 次尝试)</div>
                            <div style={{ background: '#fafafa', borderRadius: 4, border: '1px solid #f0f0f0', overflow: 'hidden' }}>
                              {attempts.map((a, i) => (
                                <div key={i} style={{
                                  padding: '8px 12px',
                                  borderBottom: i < attempts.length - 1 ? '1px solid #f0f0f0' : 'none',
                                  background: a.success ? '#f6ffed' : '#fff2f0',
                                }}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                                    <span style={{
                                      display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                                      width: 20, height: 20, borderRadius: '50%',
                                      background: a.success ? '#52c41a' : '#ff4d4f', color: '#fff',
                                      fontSize: 11, fontWeight: 600, flexShrink: 0,
                                    }}>{a.attempt_num}</span>
                                    <span style={{ fontSize: 13, fontWeight: 500, minWidth: 120 }}>{short(a.provider)}</span>
                                    <span style={{
                                      display: 'inline-block', padding: '1px 6px', borderRadius: 3, fontSize: 11,
                                      color: a.success ? '#52c41a' : '#ff4d4f',
                                      background: a.success ? '#f6ffed' : '#fff1f0',
                                      border: `1px solid ${a.success ? '#b7eb8f' : '#ffa39e'}`,
                                    }}>{a.success ? '成功' : a.status_code || 'err'}</span>
                                    <span style={{ color: '#999', fontSize: 12, fontFamily: 'monospace' }}>{a.duration_ms}ms</span>
                                    {a.error && <span style={{ color: '#ff4d4f', fontSize: 11, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.error}</span>}
                                  </div>
                                  {renderAttemptQuota(a)}
                                </div>
                              ))}
                            </div>
                          </div>
                        )
                      })()}

                      {(['client_req', 'client_resp', 'upstream_req', 'upstream_resp'] as const).map(section => {
                        const raw: string = detailData[section]
                        const isExpanded = expandedSections[section]
                        const formatted = formatJson(raw)
                        return (
                          <div key={section} style={{ marginBottom: 12 }}>
                            <div
                              onClick={() => toggleSection(section)}
                              style={{
                                display: 'flex', alignItems: 'center', gap: 6,
                                padding: '8px 12px', background: '#fafafa',
                                border: '1px solid #f0f0f0', borderRadius: isExpanded ? '4px 4px 0 0' : 4,
                                cursor: 'pointer', fontSize: 13, fontWeight: 500,
                                userSelect: 'none',
                              }}
                            >
                              <span style={{
                                fontSize: 10, transition: 'transform 0.2s',
                                display: 'inline-block',
                                transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)',
                              }}>&#9654;</span>
                              {section}
                              {!raw && <span style={{ color: '#ccc', fontWeight: 400, marginLeft: 8, fontSize: 12 }}>无数据</span>}
                            </div>
                            {isExpanded && (
                              raw ? (
                                <pre style={{
                                  margin: 0, padding: '12px 16px', background: '#1e1e1e', color: '#d4d4d4',
                                  borderRadius: '0 0 4px 4px', fontSize: 12, lineHeight: 1.6,
                                  overflowX: 'auto', maxHeight: 400, overflowY: 'auto',
                                  fontFamily: '"SF Mono", "Fira Code", "Fira Mono", Menlo, Consolas, monospace',
                                  border: '1px solid #333', borderTop: 'none',
                                }}>{formatted}</pre>
                              ) : (
                                <div style={{
                                  padding: '12px 16px', color: '#999', fontSize: 13,
                                  border: '1px solid #f0f0f0', borderTop: 'none',
                                  borderRadius: '0 0 4px 4px',
                                }}>无数据</div>
                              )
                            )}
                          </div>
                        )
                      })}
                    </>
                  )}
                </div>
              </div>
            </div>
          )}
        </>
      )}

      {activeTab === 'stats' && (
        statsLoading ? <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>加载中...</p> :
        statsError ? <div style={{ color: '#ff4d4f', padding: 16, textAlign: 'center' }}>{statsError}</div> :
        statsData ? renderAccessChart() :
        <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>请选择时间范围后查看</p>
      )}

      {activeTab === 'tokens' && (
        statsLoading ? <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>加载中...</p> :
        statsError ? <div style={{ color: '#ff4d4f', padding: 16, textAlign: 'center' }}>{statsError}</div> :
        statsData ? renderTokenChart() :
        <p style={{ textAlign: 'center', color: '#999', padding: 40 }}>请选择时间范围后查看</p>
      )}
    </div>
  )
}
