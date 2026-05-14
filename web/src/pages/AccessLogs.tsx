import { useState, useEffect } from 'react'
import { fetchAccessLogs } from '../api/accessLogs'
import { fetchAPIKeys } from '../api/client'
import type { AccessLog, APIKeyListItem } from '../api/types'

const STATUS_OPTIONS = [
  { value: 0, label: '全部' },
  { value: 200, label: '200' },
  { value: 400, label: '400' },
  { value: 401, label: '401' },
  { value: 500, label: '500' },
]

export default function AccessLogs() {
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
  const [filterFrom, setFilterFrom] = useState('')
  const [filterTo, setFilterTo] = useState('')

  const [apiKeys, setApiKeys] = useState<APIKeyListItem[]>([])

  const totalPages = Math.max(1, Math.ceil(total / perPage))

  useEffect(() => {
    fetchAPIKeys().then(setApiKeys).catch(() => {})
  }, [])

  const buildQuery = (p: number) => ({
    key_id: filterKeyId,
    model: filterModel || undefined,
    status: filterStatus || undefined,
    from: filterFrom || undefined,
    to: filterTo || undefined,
    page: p,
    per_page: perPage,
  })

  const load = (p: number) => {
    setLoading(true)
    setError('')
    fetchAccessLogs(buildQuery(p))
      .then(data => {
        setLogs(data.logs)
        setTotal(data.total)
        setPage(data.page)
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }

  // reload when filters change (reset to page 1)
  useEffect(() => {
    load(1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKeyId, filterModel, filterStatus, filterFrom, filterTo])

  const goPage = (p: number) => {
    if (p < 1 || p > totalPages) return
    load(p)
  }

  const formatTime = (ts: string) => {
    const d = new Date(ts)
    return d.toLocaleString('zh-CN', {
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    })
  }

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
          开始时间
          <input type="datetime-local" value={filterFrom} onChange={e => setFilterFrom(e.target.value)}
            style={{ display: 'block', marginTop: 4, padding: '4px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }} />
        </label>
        <label style={{ fontSize: 12, color: '#666' }}>
          结束时间
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
                  <th style={{ padding: '6px 8px' }}>错误</th>
                </tr>
              </thead>
              <tbody>
                {logs.length === 0 ? (
                  <tr><td colSpan={10} style={{ padding: 24, textAlign: 'center', color: '#999' }}>暂无日志记录</td></tr>
                ) : (
                  logs.map(log => (
                    <tr key={log.id} style={{ borderBottom: '1px solid #f0f0f0', verticalAlign: 'top' }}>
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
    </div>
  )
}
