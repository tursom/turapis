import { useState, useEffect } from 'react'
import { fetchModelMappings, createModelMapping, updateModelMapping, deleteModelMapping, fetchProviders, discoverModels, fetchSettings, updateSettings } from '../api/client'
import type { ModelMapping, Provider } from '../api/types'
import Modal from '../components/Modal'

const emptyMapping = { model_name: '', provider_id: 0, priority: 100, enabled: true }

export default function Routing() {
  const [tab, setTab] = useState<'mappings' | 'chain' | 'discover'>('mappings')
  const [mappings, setMappings] = useState<ModelMapping[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
  const [showModal, setShowModal] = useState(false)
  const [editing, setEditing] = useState<ModelMapping | null>(null)
  const [form, setForm] = useState(emptyMapping)
  const [error, setError] = useState('')

  const [chainJson, setChainJson] = useState('')
  const [chainSaved, setChainSaved] = useState(false)

  const [discProvId, setDiscProvId] = useState(0)
  const [discResult, setDiscResult] = useState<any>(null)

  const loadMappings = () => fetchModelMappings().then(setMappings).catch(e => setError(e.message))
  const loadProviders = () => fetchProviders().then(setProviders)
  const loadSettings = () => fetchSettings().then(s => setChainJson(s.default_priority_chain))

  useEffect(() => { loadMappings(); loadProviders(); loadSettings() }, [])

  const openCreate = () => { setEditing(null); setForm({...emptyMapping, provider_id: providers[0]?.id || 0}); setShowModal(true) }
  const openEdit = (m: ModelMapping) => { setEditing(m); setForm({ model_name: m.model_name, provider_id: m.provider_id, priority: m.priority, enabled: m.enabled }); setShowModal(true) }

  const handleSave = async () => {
    try {
      if (editing) await updateModelMapping({ ...form, id: editing.id, created_at: '' })
      else await createModelMapping(form)
      setShowModal(false)
      loadMappings()
    } catch (e: any) { setError(e.message) }
  }

  const handleSaveChain = async () => {
    try { await updateSettings(chainJson); setChainSaved(true); setTimeout(() => setChainSaved(false), 2000) }
    catch (e: any) { setError(e.message) }
  }

  const handleDiscover = async () => {
    if (!discProvId) return
    try { const r = await discoverModels(discProvId); setDiscResult(r) }
    catch (e: any) { setError(e.message) }
  }

  return (
    <div>
      <h2 style={{ marginBottom: 16 }}>路由配置</h2>
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}

      <div style={{ display: 'flex', gap: 0, marginBottom: 16, borderBottom: '1px solid #f0f0f0' }}>
        {(['mappings', 'chain', 'discover'] as const).map(t => (
          <button key={t} onClick={() => setTab(t)} style={{ padding: '8px 16px', border: 'none', background: tab === t ? '#1677ff' : 'transparent', color: tab === t ? '#fff' : '#333', cursor: 'pointer', borderRadius: '4px 4px 0 0' }}>
            {t === 'mappings' ? '模型映射' : t === 'chain' ? '默认优先级链' : '模型发现'}
          </button>
        ))}
      </div>

      {tab === 'mappings' && (
        <div>
          <button onClick={openCreate} style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer', marginBottom: 12 }}>+ 添加映射</button>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead><tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left' }}><th style={{ padding: 8 }}>模型</th><th style={{ padding: 8 }}>供应商</th><th style={{ padding: 8 }}>优先级</th><th style={{ padding: 8 }}>启用</th><th style={{ padding: 8 }}>操作</th></tr></thead>
            <tbody>
              {mappings.map(m => (
                <tr key={m.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                  <td style={{ padding: 8 }}>{m.model_name}</td>
                  <td style={{ padding: 8 }}>{providers.find(p => p.id === m.provider_id)?.name || `#${m.provider_id}`}</td>
                  <td style={{ padding: 8 }}>{m.priority}</td>
                  <td style={{ padding: 8 }}>{m.enabled ? '✅' : '❌'}</td>
                  <td style={{ padding: 8 }}>
                    <button onClick={() => openEdit(m)} style={{ marginRight: 8 }}>编辑</button>
                    <button onClick={() => { deleteModelMapping(m.id); loadMappings() }} style={{ color: '#ff4d4f' }}>删除</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {tab === 'chain' && (
        <div>
          <p style={{ marginBottom: 8, color: '#666' }}>定义默认故障转移顺序的供应商名称 JSON 数组。未匹配到模型专属映射时生效。</p>
          <textarea value={chainJson} onChange={e => setChainJson(e.target.value)} rows={6} style={{ width: '100%', fontFamily: 'monospace', padding: 8, border: '1px solid #d9d9d9', borderRadius: 6 }} />
          <button onClick={handleSaveChain} style={{ marginTop: 8, padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>保存</button>
          {chainSaved && <span style={{ marginLeft: 8, color: '#52c41a' }}>已保存！</span>}
        </div>
      )}

      {tab === 'discover' && (
        <div>
          <label style={{ display: 'block', marginBottom: 8 }}>供应商：
            <select value={discProvId} onChange={e => setDiscProvId(+e.target.value)} style={{ marginLeft: 8 }}>
              <option value={0}>-- 请选择 --</option>
              {providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </label>
          <button onClick={handleDiscover} disabled={!discProvId} style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>发现模型</button>
          {discResult && (
            <div style={{ marginTop: 16 }}>
              <p>从 {discResult.provider} 发现了 {discResult.count} 个模型：</p>
              <ul>{discResult.models.map((m: any) => <li key={m.id}>{m.model_name} ({m.model_id})</li>)}</ul>
            </div>
          )}
        </div>
      )}

      <Modal open={showModal} onClose={() => setShowModal(false)} title={editing ? '编辑映射' : '新建映射'}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <label>模型名称 <input value={form.model_name} onChange={e => setForm({...form, model_name: e.target.value})} style={{ width: '100%' }} /></label>
          <label>供应商 <select value={form.provider_id} onChange={e => setForm({...form, provider_id: +e.target.value})}>{providers.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
          <label>优先级 <input type="number" value={form.priority} onChange={e => setForm({...form, priority: +e.target.value})} /></label>
          <label><input type="checkbox" checked={form.enabled} onChange={e => setForm({...form, enabled: e.target.checked})} /> 启用</label>
          <button onClick={handleSave} style={{ padding: '8px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>保存</button>
        </div>
      </Modal>
    </div>
  )
}
