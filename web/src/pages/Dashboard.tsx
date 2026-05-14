import { useState, useEffect } from 'react'
import { fetchStatus } from '../api/client'
import type { ServiceStatus } from '../api/types'

export default function Dashboard() {
  const [status, setStatus] = useState<ServiceStatus | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    fetchStatus().then(setStatus).catch(e => setError(e.message))
  }, [])

  if (error) return <div style={{ color: '#ff4d4f' }}>{error}</div>
  if (!status) return <p>加载中...</p>

  return (
    <div>
      <h2 style={{ marginBottom: 16 }}>仪表盘</h2>
      <div style={{ display: 'flex', gap: 16, marginBottom: 24 }}>
        <div style={{ background: '#f0f5ff', padding: 16, borderRadius: 8, flex: 1 }}>
          <div style={{ fontSize: 28, fontWeight: 700 }}>{status.provider_count}</div>
          <div style={{ color: '#666' }}>供应商数量</div>
        </div>
        <div style={{ background: '#f6ffed', padding: 16, borderRadius: 8, flex: 1 }}>
          <div style={{ fontSize: 28, fontWeight: 700 }}>{status.registered_count}</div>
          <div style={{ color: '#666' }}>已注册</div>
        </div>
      </div>
      <h3 style={{ marginBottom: 12 }}>供应商状态</h3>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))', gap: 12 }}>
        {status.providers.map(p => (
          <div key={p.name} style={{ border: '1px solid #f0f0f0', borderRadius: 8, padding: 16 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
              <strong>{p.name}</strong>
              <span style={{ fontSize: 12, padding: '2px 8px', borderRadius: 4, background: p.enabled ? '#f6ffed' : '#fff2f0', color: p.enabled ? '#52c41a' : '#ff4d4f' }}>{p.enabled ? '启用' : '禁用'}</span>
            </div>
            <div style={{ fontSize: 13, color: '#666' }}>
              <div>已发现模型：{p.discovered_models}</div>
              <div>注册状态：{p.registered ? '✅ 已注册' : '❌ 未注册'}</div>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
