import React, { useState, useEffect } from 'react'
import { fetchSites, createSite, updateSite, deleteSite, fetchSiteModels, addSiteModel, deleteSiteModel } from '../api/client'
import type { Site, SiteModel } from '../api/types'
import Modal from '../components/Modal'

const emptyForm: { name: string; base_url: string; protocol: 'openai' | 'anthropic' | 'codex'; auth_mode: string; enabled: boolean } = { name: '', base_url: '', protocol: 'openai', auth_mode: 'api_key', enabled: true }

export default function Sites() {
  const [sites, setSites] = useState<Site[]>([])
  const [loading, setLoading] = useState(true)
  const [showModal, setShowModal] = useState(false)
  const [editing, setEditing] = useState<Site | null>(null)
  const [form, setForm] = useState(emptyForm)
  const [error, setError] = useState('')
  const [expandedSite, setExpandedSite] = useState<number | null>(null)
  const [siteModels, setSiteModels] = useState<Record<number, SiteModel[]>>({})
  const [newModelId, setNewModelId] = useState('')
  const [newModelName, setNewModelName] = useState('')

  const load = () => {
    setLoading(true)
    fetchSites().then(setSites).catch(e => setError(e.message)).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const openCreate = () => { setEditing(null); setForm(emptyForm); setShowModal(true) }
  const openEdit = (s: Site) => { setEditing(s); setForm({ name: s.name, base_url: s.base_url, protocol: s.protocol, auth_mode: s.auth_mode, enabled: s.enabled }); setShowModal(true) }

  const handleSave = async () => {
    try {
      if (editing) {
        await updateSite({ ...form, id: editing.id })
      } else {
        await createSite(form)
      }
      setShowModal(false)
      load()
    } catch (e: any) { setError(e.message) }
  }

  const handleDelete = async (id: number) => {
    if (!confirm('确定删除此站点吗？')) return
    try {
      await deleteSite(id)
      if (expandedSite === id) setExpandedSite(null)
      load()
    } catch (e: any) { setError(e.message) }
  }

  const toggleExpand = async (siteId: number) => {
    if (expandedSite === siteId) {
      setExpandedSite(null)
      return
    }
    setExpandedSite(siteId)
    if (!siteModels[siteId]) {
      try {
        const models = await fetchSiteModels(siteId)
        setSiteModels(prev => ({ ...prev, [siteId]: models }))
      } catch (e: any) { setError(e.message) }
    }
  }

  const handleAddModel = async (siteId: number) => {
    if (!newModelId || !newModelName) return
    try {
      await addSiteModel(siteId, { model_id: newModelId, model_name: newModelName })
      const models = await fetchSiteModels(siteId)
      setSiteModels(prev => ({ ...prev, [siteId]: models }))
      setNewModelId('')
      setNewModelName('')
    } catch (e: any) { setError(e.message) }
  }

  const handleDeleteModel = async (siteId: number, modelId: number) => {
    try {
      await deleteSiteModel(siteId, modelId)
      const models = await fetchSiteModels(siteId)
      setSiteModels(prev => ({ ...prev, [siteId]: models }))
    } catch (e: any) { setError(e.message) }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <h2>站点管理</h2>
        <button onClick={openCreate} style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>+ 创建站点</button>
      </div>
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}
      {loading ? <p>加载中...</p> : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left' }}>
              <th style={{ padding: 8 }}>名称</th><th style={{ padding: 8 }}>base_url</th><th style={{ padding: 8 }}>协议</th><th style={{ padding: 8 }}>认证方式</th><th style={{ padding: 8 }}>预设模型数</th><th style={{ padding: 8 }}>启用</th><th style={{ padding: 8 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {sites.map(s => (
              <React.Fragment key={s.id}>
                <tr style={{ borderBottom: '1px solid #f0f0f0' }}>
                  <td style={{ padding: 8 }}>{s.name}</td>
                  <td style={{ padding: 8, fontSize: 13 }}>{s.base_url}</td>
                  <td style={{ padding: 8 }}>{s.protocol}</td>
                  <td style={{ padding: 8 }}>{s.auth_mode}</td>
                  <td style={{ padding: 8 }}>{s.model_count ?? 0}</td>
                  <td style={{ padding: 8 }}>{s.enabled ? '✅' : '❌'}</td>
                  <td style={{ padding: 8 }}>
                    <button onClick={() => toggleExpand(s.id)} style={{ marginRight: 8 }}>查看模型</button>
                    <button onClick={() => openEdit(s)} style={{ marginRight: 8 }}>编辑</button>
                    <button onClick={() => handleDelete(s.id)} style={{ color: '#ff4d4f' }}>删除</button>
                  </td>
                </tr>
                {expandedSite === s.id && (
                  <tr key={`models-${s.id}`}>
                    <td colSpan={7} style={{ padding: '8px 16px', background: '#fafafa' }}>
                      <div style={{ marginBottom: 8, fontWeight: 600 }}>预设模型</div>
                      {(siteModels[s.id] ?? []).length === 0 ? (
                        <p style={{ margin: '4px 0', fontSize: 13, color: '#888' }}>暂无预设模型</p>
                      ) : (
                        <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: 8, fontSize: 13 }}>
                          <thead>
                            <tr style={{ borderBottom: '1px solid #e0e0e0' }}>
                              <th style={{ padding: '4px 8px', textAlign: 'left' }}>模型 ID</th>
                              <th style={{ padding: '4px 8px', textAlign: 'left' }}>模型名称</th>
                              <th style={{ padding: '4px 8px' }}>操作</th>
                            </tr>
                          </thead>
                          <tbody>
                            {(siteModels[s.id] ?? []).map(m => (
                              <tr key={m.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                                <td style={{ padding: '4px 8px' }}>{m.model_id}</td>
                                <td style={{ padding: '4px 8px' }}>{m.model_name}</td>
                                <td style={{ padding: '4px 8px' }}>
                                  <button onClick={() => handleDeleteModel(s.id, m.id)} style={{ color: '#ff4d4f', fontSize: 12, border: 'none', background: 'none', cursor: 'pointer' }}>删除</button>
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      )}
                      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                        <input placeholder="模型 ID" value={newModelId} onChange={e => setNewModelId(e.target.value)} style={{ padding: '4px 8px', fontSize: 13, width: 200 }} />
                        <input placeholder="模型名称" value={newModelName} onChange={e => setNewModelName(e.target.value)} style={{ padding: '4px 8px', fontSize: 13, width: 200 }} />
                        <button onClick={() => handleAddModel(s.id)} style={{ padding: '4px 12px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 4, cursor: 'pointer', fontSize: 13 }}>添加</button>
                      </div>
                    </td>
                  </tr>
                )}
              </React.Fragment>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={showModal} onClose={() => setShowModal(false)} title={editing ? '编辑站点' : '创建站点'}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <label>名称 <input value={form.name} onChange={e => setForm({...form, name: e.target.value})} style={{ width: '100%' }} /></label>
          <label>base_url <input value={form.base_url} onChange={e => setForm({...form, base_url: e.target.value})} style={{ width: '100%' }} /></label>
          <label>协议 <select value={form.protocol} onChange={e => setForm({...form, protocol: e.target.value as 'openai' | 'anthropic' | 'codex'})}><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="codex">Codex</option></select></label>
          <label>认证方式 <select value={form.auth_mode} onChange={e => setForm({...form, auth_mode: e.target.value})}><option value="api_key">API Key</option><option value="oauth">OAuth</option></select></label>
          <label><input type="checkbox" checked={form.enabled} onChange={e => setForm({...form, enabled: e.target.checked})} /> 启用</label>
          <button onClick={handleSave} style={{ padding: '8px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>保存</button>
        </div>
      </Modal>
    </div>
  )
}
