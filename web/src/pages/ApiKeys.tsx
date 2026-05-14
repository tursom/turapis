import { useState, useEffect } from 'react'
import { fetchAPIKeys, createAPIKey, revokeAPIKey } from '../api/client'
import type { APIKeyListItem, APIKeyCreated } from '../api/types'
import Modal from '../components/Modal'

export default function ApiKeys() {
  const [keys, setKeys] = useState<APIKeyListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [showModal, setShowModal] = useState(false)
  const [newKeyName, setNewKeyName] = useState('')
  const [createdKey, setCreatedKey] = useState<APIKeyCreated | null>(null)

  const load = () => {
    setLoading(true)
    fetchAPIKeys().then(setKeys).catch(e => setError(e.message)).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleCreate = async () => {
    if (!newKeyName.trim()) return
    try {
      const created = await createAPIKey(newKeyName)
      setCreatedKey(created)
      load()
    } catch (e: any) { setError(e.message) }
  }

  const handleRevoke = async (id: number) => {
    if (!confirm('确定吊销此 API Key 吗？吊销后客户端将无法使用。')) return
    await revokeAPIKey(id)
    load()
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <h2>API Key 管理</h2>
        <button onClick={() => { setNewKeyName(''); setCreatedKey(null); setShowModal(true) }} style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>+ 创建 API Key</button>
      </div>
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}
      {loading ? <p>加载中...</p> : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left' }}>
              <th style={{ padding: 8 }}>备注名</th><th style={{ padding: 8 }}>Key</th><th style={{ padding: 8 }}>状态</th><th style={{ padding: 8 }}>创建时间</th><th style={{ padding: 8 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {keys.map(k => (
              <tr key={k.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                <td style={{ padding: 8 }}>{k.name}</td>
                <td style={{ padding: 8, fontFamily: 'monospace', fontSize: 12 }}>{k.key}</td>
                <td style={{ padding: 8 }}>{k.enabled ? '✅ 有效' : '❌ 已吊销'}</td>
                <td style={{ padding: 8, fontSize: 12, color: '#666' }}>{new Date(k.created_at).toLocaleDateString('zh-CN')}</td>
                <td style={{ padding: 8 }}>
                  {k.enabled && <button onClick={() => handleRevoke(k.id)} style={{ color: '#ff4d4f' }}>吊销</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={showModal} onClose={() => setShowModal(false)} title="创建 API Key">
        {!createdKey ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label>备注名 <input value={newKeyName} onChange={e => setNewKeyName(e.target.value)} placeholder="例如：张三的 Key" style={{ width: '100%' }} autoFocus /></label>
            <button onClick={handleCreate} disabled={!newKeyName.trim()} style={{ padding: '8px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>创建</button>
          </div>
        ) : (
          <div>
            <p style={{ color: '#52c41a', marginBottom: 12 }}>API Key 创建成功！请立即复制保存——后续将无法再次查看。</p>
            <div style={{ display: 'flex', gap: 8 }}>
              <input readOnly value={createdKey.key} style={{ flex: 1, fontFamily: 'monospace', padding: '8px', border: '1px solid #d9d9d9', borderRadius: 6 }} />
              <button onClick={() => navigator.clipboard.writeText(createdKey.key)} style={{ padding: '8px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>复制</button>
            </div>
            <button onClick={() => setShowModal(false)} style={{ marginTop: 12, padding: '6px 16px', background: '#ccc', border: 'none', borderRadius: 6, cursor: 'pointer' }}>关闭</button>
          </div>
        )}
      </Modal>
    </div>
  )
}
