import { useState, useEffect } from 'react'
import { fetchProviders, createProvider, updateProvider, deleteProvider, fetchSites, createProviderFromSite, discoverAllModels, batchProbeQuota } from '../api/client'
import type { Provider, Site, QuotaInfo, QuotaEntry } from '../api/types'
import Modal from '../components/Modal'

function windowLabel(m: number) {
  if (m <= 400) return '5h'
  if (m <= 15000) return '7d'
  return '30d'
}

function resetLabel(seconds: number) {
  if (seconds <= 0) return ''
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  if (d > 0) return h > 0 ? `${d}d${h}h` : m > 0 ? `${d}d${m}m` : `${d}d`
  if (h > 0) return m > 0 ? `${h}h${m}m` : `${h}h`
  if (m > 0) return s > 0 ? `${m}m${s}s` : `${m}m`
  return `${s}s`
}

function QuotaBar({ entry }: { entry: QuotaEntry }) {
  const pct = entry.used_percent
  const color = pct >= 80 ? '#ff4d4f' : pct >= 50 ? '#fa8c16' : '#52c41a'
  const resetStr = entry.reset_after_seconds > 0 ? resetLabel(entry.reset_after_seconds) : ''
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 1, marginRight: 8, minWidth: 80 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
        <span style={{ fontSize: 10, color: '#999', width: 20, textAlign: 'right' }}>{windowLabel(entry.window_minutes)}</span>
        <span style={{ width: 48, height: 8, background: '#f0f0f0', borderRadius: 3, overflow: 'hidden', display: 'inline-block', verticalAlign: 'middle' }}>
          <span style={{ display: 'block', width: `${Math.min(pct, 100)}%`, height: '100%', background: color, borderRadius: 3, transition: 'width .3s' }} />
        </span>
        <span style={{ fontSize: 11, color, fontWeight: 600, minWidth: 32 }}>{String(pct)}%</span>
      </div>
      {resetStr && (
        <span style={{ fontSize: 10, color: '#999', textAlign: 'center' }}>{resetStr} 后刷新</span>
      )}
    </div>
  )
}

function formatQuota(q: QuotaInfo | undefined) {
  if (!q) return '-'
  const entries = [q.primary, q.secondary, q.tertiary].filter((e): e is QuotaEntry => e != null)
  if (entries.length === 0) return '-'
  return <span style={{ display: 'inline-flex', flexWrap: 'nowrap', alignItems: 'flex-start' }}>{entries.map((e, i) => <QuotaBar key={i} entry={e} />)}</span>
}

  const emptyForm: { name: string; base_url: string; api_key: string; protocol: 'openai' | 'anthropic'; auth_mode: string; priority: number; enabled: boolean; supported_tools: string; proxy: string } = { name: '', base_url: '', api_key: '', protocol: 'openai', auth_mode: 'api_key', priority: 100, enabled: true, supported_tools: '["web_search"]', proxy: '' }

export default function Providers() {
  const [providers, setProviders] = useState<Provider[]>([])
  const [loading, setLoading] = useState(true)
  const [showModal, setShowModal] = useState(false)
  const [editing, setEditing] = useState<Provider | null>(null)
  const [form, setForm] = useState(emptyForm)
  const [error, setError] = useState('')
  const [createMode, setCreateMode] = useState<'manual' | 'from-site'>('manual')
  const [sitesList, setSitesList] = useState<Site[]>([])
  const [selectedSiteId, setSelectedSiteId] = useState<number | null>(null)
  const [nameOverride, setNameOverride] = useState('')
  const [siteApiKey, setSiteApiKey] = useState('')
  const [siteOauthJson, setSiteOauthJson] = useState('')
  const [discovering, setDiscovering] = useState(false)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())

  const load = () => {
    setLoading(true)
    fetchProviders().then(setProviders).catch(e => setError(e.message)).finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const openCreate = () => { setEditing(null); setForm(emptyForm); setCreateMode('manual'); setShowModal(true); fetchSites().then(setSitesList).catch(() => {}) }
  const openEdit = (p: Provider) => { setEditing(p); setForm({ name: p.name, base_url: p.base_url, api_key: p.api_key, protocol: p.protocol, auth_mode: p.auth_mode, priority: p.priority, enabled: p.enabled, supported_tools: p.supported_tools || '[]', proxy: p.proxy || '' }); setShowModal(true) }

  const handleSave = async () => {
    try {
      if (editing) {
        await updateProvider({ ...form, id: editing.id, created_at: editing.created_at, updated_at: '' })
      } else {
        await createProvider(form)
      }
      setShowModal(false)
      load()
    } catch (e: any) { setError(e.message) }
  }

  const handleDiscoverAll = async () => {
    setDiscovering(true)
    setError('')
    try {
      const body = selectedIds.size > 0 ? { provider_ids: [...selectedIds] } : {}
      const res = await discoverAllModels(body)
      const ok = res.results.filter(r => !r.error).length
      const fail = res.results.filter(r => r.error).length
      alert(`发现完成: ${ok} 成功, ${fail} 失败`)
      load()
    } catch (e: any) {
      setError(e.message)
    } finally {
      setDiscovering(false)
    }
  }

  const toggleSelect = (id: number) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  const toggleSelectAll = () => {
    if (selectedIds.size === providers.length) {
      setSelectedIds(new Set())
    } else {
      setSelectedIds(new Set(providers.map(p => p.id)))
    }
  }

  const handleBatchQuota = async () => {
    setDiscovering(true)
    setError('')
    try {
      const body = selectedIds.size > 0 ? { provider_ids: [...selectedIds] } : {}
      const res = await batchProbeQuota(body)
      const ok = res.results.filter(r => !r.error).length
      const fail = res.results.filter(r => r.error).length
      alert(`额度刷新完成: ${ok} 成功, ${fail} 失败`)
      load()
    } catch (e: any) {
      setError(e.message)
    } finally {
      setDiscovering(false)
    }
  }

  const handleCreateFromSite = async () => {
    if (!selectedSiteId) return
    try {
      const data: { name_override?: string; api_key?: string; oauth?: object } = {}
      if (nameOverride) data.name_override = nameOverride
      const site = sitesList.find(s => s.id === selectedSiteId)
      if (site?.auth_mode === 'api_key') {
        if (!siteApiKey) { setError('请输入 API Key'); return }
        data.api_key = siteApiKey
      } else if (site?.auth_mode === 'oauth') {
        if (!siteOauthJson) { setError('请输入 OAuth JSON'); return }
        try { data.oauth = JSON.parse(siteOauthJson) } catch { setError('OAuth JSON 格式错误'); return }
      }
      await createProviderFromSite(selectedSiteId, data)
      setShowModal(false)
      load()
    } catch (e: any) { setError(e.message) }
  }

  const handleDelete = async (id: number) => {
    if (!confirm('确定删除此供应商吗？')) return
    await deleteProvider(id)
    load()
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <h2>供应商管理</h2>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={handleDiscoverAll} disabled={discovering} style={{ padding: '6px 16px', background: '#52c41a', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>{discovering ? '处理中...' : '批量发现'}</button>
          <button onClick={handleBatchQuota} disabled={discovering} style={{ padding: '6px 16px', background: '#fa8c16', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>{discovering ? '处理中...' : '批量刷新额度'}</button>
          <button onClick={openCreate} style={{ padding: '6px 16px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>+ 添加供应商</button>
        </div>
      </div>
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}
      {loading ? <p>加载中...</p> : (
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left' }}>
              <th style={{ padding: 8 }}><input type="checkbox" checked={selectedIds.size === providers.length && providers.length > 0} onChange={toggleSelectAll} /></th>
              <th style={{ padding: 8 }}>ID</th><th style={{ padding: 8 }}>名称</th><th style={{ padding: 8 }}>协议</th><th style={{ padding: 8 }}>认证</th><th style={{ padding: 8 }}>额度</th><th style={{ padding: 8 }}>优先级</th><th style={{ padding: 8 }}>启用</th><th style={{ padding: 8 }}>支持工具</th><th style={{ padding: 8 }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {providers.map(p => (
              <tr key={p.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                <td style={{ padding: 8 }}><input type="checkbox" checked={selectedIds.has(p.id)} onChange={() => toggleSelect(p.id)} /></td>
                <td style={{ padding: 8, color: '#999', fontSize: 11 }}>#{p.id}</td>
                <td style={{ padding: 8 }}>{p.name}</td>
                <td style={{ padding: 8 }}>{p.protocol}</td>
              <td style={{ padding: 8 }}>{p.auth_mode}</td>
              <td style={{ padding: 8, fontSize: 12, whiteSpace: 'nowrap' }}>{formatQuota(p.quota)}</td>
              <td style={{ padding: 8 }}>{p.priority}</td>
                <td style={{ padding: 8 }}>{p.enabled ? '✅' : '❌'}</td>
                <td style={{ padding: 8, fontSize: 12, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.supported_tools || '[]'}</td>
                <td style={{ padding: 8 }}>
                  <button onClick={() => openEdit(p)} style={{ marginRight: 8 }}>编辑</button>
                  <button onClick={() => handleDelete(p.id)} style={{ color: '#ff4d4f' }}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Modal open={showModal} onClose={() => setShowModal(false)} title={editing ? '编辑供应商' : '新建供应商'}>
        {!editing && (
          <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
            <button onClick={() => setCreateMode('manual')} style={{ flex: 1, padding: '6px 0', border: createMode === 'manual' ? '2px solid #1677ff' : '1px solid #d9d9d9', borderRadius: 6, cursor: 'pointer', background: createMode === 'manual' ? '#e6f4ff' : '#fff', fontWeight: createMode === 'manual' ? 600 : 400 }}>手动创建</button>
            <button onClick={() => { setCreateMode('from-site'); setSelectedSiteId(null); setNameOverride(''); setSiteApiKey(''); setSiteOauthJson('') }} style={{ flex: 1, padding: '6px 0', border: createMode === 'from-site' ? '2px solid #1677ff' : '1px solid #d9d9d9', borderRadius: 6, cursor: 'pointer', background: createMode === 'from-site' ? '#e6f4ff' : '#fff', fontWeight: createMode === 'from-site' ? 600 : 400 }}>从站点创建</button>
          </div>
        )}
        {createMode === 'manual' || editing ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label>名称 <input value={form.name} onChange={e => setForm({...form, name: e.target.value})} style={{ width: '100%' }} /></label>
            <label>接口地址 <input value={form.base_url} onChange={e => setForm({...form, base_url: e.target.value})} style={{ width: '100%' }} /></label>
            <label>API Key <input value={form.api_key} onChange={e => setForm({...form, api_key: e.target.value})} style={{ width: '100%' }} /></label>
            <label>协议 <select value={form.protocol} onChange={e => setForm({...form, protocol: e.target.value as 'openai' | 'anthropic'})}><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option></select></label>
            <label>优先级 <input type="number" value={form.priority} onChange={e => setForm({...form, priority: +e.target.value})} /></label>
            <label><input type="checkbox" checked={form.enabled} onChange={e => setForm({...form, enabled: e.target.checked})} /> 启用</label>
            <label>支持的工具 <input value={form.supported_tools} onChange={e => setForm({...form, supported_tools: e.target.value})} placeholder='JSON array, e.g. ["web_search","code_interpreter"]' style={{ width: '100%' }} /></label>
            <label>代理 <input value={form.proxy} onChange={e => setForm({...form, proxy: e.target.value})} placeholder="http://host:port 或 socks5://host:port" style={{ width: '100%' }} /></label>
            <button onClick={handleSave} style={{ padding: '8px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>保存</button>
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label>选择站点 <select value={selectedSiteId ?? ''} onChange={e => { setSelectedSiteId(+e.target.value); setSiteApiKey(''); setSiteOauthJson('') }} style={{ width: '100%' }}><option value="">-- 请选择 --</option>{sitesList.map(s => <option key={s.id} value={s.id}>{s.name} ({s.base_url})</option>)}</select></label>
            <label>供应商名称（可选）<input value={nameOverride} onChange={e => setNameOverride(e.target.value)} placeholder="留空则使用站点名称" style={{ width: '100%' }} /></label>
            {selectedSiteId && (() => {
              const site = sitesList.find(s => s.id === selectedSiteId)
              if (!site) return null
              if (site.auth_mode === 'api_key') {
                return <label>API Key <input value={siteApiKey} onChange={e => setSiteApiKey(e.target.value)} type="password" style={{ width: '100%' }} /></label>
              }
              if (site.auth_mode === 'oauth') {
                return <label>OAuth JSON <textarea value={siteOauthJson} onChange={e => setSiteOauthJson(e.target.value)} rows={4} style={{ width: '100%' }} /></label>
              }
              return null
            })()}
            <button onClick={handleCreateFromSite} style={{ padding: '8px', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, cursor: 'pointer' }}>创建</button>
          </div>
        )}
      </Modal>
    </div>
  )
}
