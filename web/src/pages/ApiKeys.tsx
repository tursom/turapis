import { useState, useEffect } from 'react'
import { fetchAPIKeys, createAPIKey, revokeAPIKey, updateAPIKey, fetchModelMappings, fetchProviders } from '../api/client'
import type { APIKeyListItem, APIKeyCreated, ModelMapping, Provider } from '../api/types'
import Modal from '../components/Modal'

export default function ApiKeys() {
  const [keys, setKeys] = useState<APIKeyListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [newKeyName, setNewKeyName] = useState('')
  const [createdKey, setCreatedKey] = useState<APIKeyCreated | null>(null)

  const [allModels, setAllModels] = useState<string[]>([])
  const [allProviders, setAllProviders] = useState<string[]>([])

  const [editKey, setEditKey] = useState<APIKeyListItem | null>(null)
  const [editName, setEditName] = useState('')
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set())
  const [selectedProviders, setSelectedProviders] = useState<Set<string>>(new Set())
  const [editSaving, setEditSaving] = useState(false)

  const load = () => {
    setLoading(true)
    Promise.all([
      fetchAPIKeys(),
      fetchModelMappings(),
      fetchProviders(),
    ]).then(([k, mappings, providers]) => {
      setKeys(k)
      setAllModels([...new Set(mappings.map((m: ModelMapping) => m.model_name))].sort())
      setAllProviders(providers.map((p: Provider) => p.name).sort())
    }).catch(e => setError(e.message))
    .finally(() => setLoading(false))
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

  const openEdit = (k: APIKeyListItem) => {
    setEditKey(k)
    setEditName(k.name)
    const perms = parsePermissions(k.permissions)
    setSelectedModels(new Set(perms.allowed_models || []))
    setSelectedProviders(new Set(perms.allowed_providers || []))
  }

  const closeEdit = () => {
    setEditKey(null)
  }

  const toggleModel = (m: string) => {
    setSelectedModels(prev => {
      const next = new Set(prev)
      if (next.has(m)) { next.delete(m) } else { next.add(m) }
      return next
    })
  }

  const toggleProvider = (p: string) => {
    setSelectedProviders(prev => {
      const next = new Set(prev)
      if (next.has(p)) { next.delete(p) } else { next.add(p) }
      return next
    })
  }

  const saveEdit = async () => {
    if (!editKey) return
    setEditSaving(true)
    try {
      const perms: Record<string, string[]> = {}
      const models = [...selectedModels]
      const providers = [...selectedProviders]
      if (models.length > 0) perms.allowed_models = models
      if (providers.length > 0) perms.allowed_providers = providers
      await updateAPIKey(editKey.id, {
        name: editName || undefined,
        permissions: JSON.stringify(perms),
      })
      closeEdit()
      load()
    } catch (e: any) { setError(e.message) }
    finally { setEditSaving(false) }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <h2>API Key 管理</h2>
        <button onClick={() => { setNewKeyName(''); setCreatedKey(null); setShowCreate(true) }}
          style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>
          + 创建 API Key
        </button>
      </div>
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}
      {loading ? <p>加载中...</p> : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left' }}>
              <th style={{ padding: 8 }}>备注名</th>
              <th style={{ padding: 8 }}>Key</th>
              <th style={{ padding: 8 }}>状态</th>
              <th style={{ padding: 8 }}>模型限制</th>
              <th style={{ padding: 8 }}>供应商限制</th>
              <th style={{ padding: 8 }}>创建时间</th>
              <th style={{ padding: 8 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {keys.map(k => {
              const perms = parsePermissions(k.permissions)
              return (
                <tr key={k.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                  <td style={{ padding: 8 }}>{k.name}</td>
                  <td style={{ padding: 8, fontFamily: 'monospace', fontSize: 12 }}>{k.key}</td>
                  <td style={{ padding: 8 }}>{k.enabled ? '✅ 有效' : '❌ 已吊销'}</td>
                  <td style={{ padding: 8 }}>
                    {perms.allowed_models?.length ? (
                      <span style={{ fontSize: 12, color: '#1677ff' }}>{perms.allowed_models.join(', ')}</span>
                    ) : (
                      <span style={{ fontSize: 12, color: '#999' }}>全部</span>
                    )}
                  </td>
                  <td style={{ padding: 8 }}>
                    {perms.allowed_providers?.length ? (
                      <span style={{ fontSize: 12, color: '#52c41a' }}>{perms.allowed_providers.join(', ')}</span>
                    ) : (
                      <span style={{ fontSize: 12, color: '#999' }}>全部</span>
                    )}
                  </td>
                  <td style={{ padding: 8, fontSize: 12, color: '#666' }}>{new Date(k.created_at).toLocaleDateString('zh-CN')}</td>
                  <td style={{ padding: 8, whiteSpace: 'nowrap' }}>
                    {k.enabled && <button onClick={() => openEdit(k)} style={{ color: '#1677ff', marginRight: 8, border: 'none', background: 'none', cursor: 'pointer', fontSize: 13 }}>编辑</button>}
                    {k.enabled && <button onClick={() => handleRevoke(k.id)} style={{ color: '#ff4d4f', border: 'none', background: 'none', cursor: 'pointer', fontSize: 13 }}>吊销</button>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}

      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="创建 API Key">
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
            <button onClick={() => setShowCreate(false)} style={{ marginTop: 12, padding: '6px 16px', background: '#ccc', border: 'none', borderRadius: 6, cursor: 'pointer' }}>关闭</button>
          </div>
        )}
      </Modal>

      <Modal open={editKey !== null} onClose={closeEdit} title={`编辑 API Key — ${editKey?.name || ''}`}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <label>
            <div style={{ fontSize: 12, color: '#999', marginBottom: 4 }}>备注名</div>
            <input value={editName} onChange={e => setEditName(e.target.value)}
              style={{ width: '100%', padding: '6px 10px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13 }} />
          </label>

          <div>
            <div style={{ fontSize: 12, color: '#999', marginBottom: 6 }}>模型限制（不选 = 全部允许）</div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '4px 12px', maxHeight: 200, overflowY: 'auto', padding: '8px', border: '1px solid #f0f0f0', borderRadius: 4 }}>
              {allModels.map(m => (
                <label key={m} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer', padding: '2px 0' }}>
                  <input type="checkbox" checked={selectedModels.has(m)} onChange={() => toggleModel(m)}
                    style={{ cursor: 'pointer' }} />
                  {m}
                </label>
              ))}
            </div>
          </div>

          <div>
            <div style={{ fontSize: 12, color: '#999', marginBottom: 6 }}>供应商限制（不选 = 全部允许）</div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '4px 12px', maxHeight: 200, overflowY: 'auto', padding: '8px', border: '1px solid #f0f0f0', borderRadius: 4 }}>
              {allProviders.map(p => (
                <label key={p} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer', padding: '2px 0' }}>
                  <input type="checkbox" checked={selectedProviders.has(p)} onChange={() => toggleProvider(p)}
                    style={{ cursor: 'pointer' }} />
                  {p}
                </label>
              ))}
            </div>
          </div>

          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button onClick={closeEdit}
              style={{ padding: '6px 16px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 13 }}>取消</button>
            <button onClick={saveEdit} disabled={editSaving}
              style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 4, cursor: 'pointer', fontSize: 13 }}>
              {editSaving ? '保存中...' : '保存'}
            </button>
          </div>
        </div>
      </Modal>
    </div>
  )
}

function parsePermissions(raw: string): { allowed_models?: string[]; allowed_providers?: string[] } {
  if (!raw) return {}
  try {
    return JSON.parse(raw)
  } catch {
    return {}
  }
}
