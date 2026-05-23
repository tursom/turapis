import { useState, useEffect } from 'react'
import { useAuth } from '../auth/AuthContext'
import Modal from '../components/Modal'
import {
  fetchCodexAccounts, fetchCodexConfig, fetchBrowserStatus,
  triggerRegister, generateAuthURL, triggerRelogin,
  refreshCodexAccount, healthCheckCodexAccount,
  fetchTaskStatus, cancelTask, fetchAllTasks,
  setEmailCredential, deleteEmailCredential, deleteCodexAccount,
  updateCodexConfig,
} from '../api/client'
import type { CodexAccount, CodexConfig, EmailCredential, AsyncTask } from '../api/types'

export default function CodexAccounts() {
  const { role } = useAuth()
  const isAdmin = role === 'admin'
  const [accounts, setAccounts] = useState<CodexAccount[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [config, setConfig] = useState<CodexConfig | null>(null)
  const [browserConnected, setBrowserConnected] = useState<boolean | null>(null)
  const [tasks, setTasks] = useState<AsyncTask[]>([])
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null)
  const [emailModalOpen, setEmailModalOpen] = useState(false)
  const [emailCred, setEmailCred] = useState<EmailCredential>({ email: '', provider: '', token: '' })
  const [emailAccountId, setEmailAccountId] = useState(0)
  const [actionLoading, setActionLoading] = useState<number | null>(null)
  const [configModalOpen, setConfigModalOpen] = useState(false)
  const [editConfig, setEditConfig] = useState<CodexConfig | null>(null)
  const [statusMessage, setStatusMessage] = useState('')

  const load = () => {
    setLoading(true)
    setError('')
    Promise.all([
      fetchCodexAccounts(),
      fetchCodexConfig(),
      fetchBrowserStatus(),
      fetchAllTasks(),
    ]).then(([accs, cfg, bs, tsks]) => {
      setAccounts(accs)
      setConfig(cfg)
      setBrowserConnected(bs.connected)
      setTasks(tsks)
    }).catch(e => setError(e.message))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Load existing tasks on mount
  useEffect(() => {
    fetchAllTasks().then(setTasks).catch(() => {})
  }, [])

  // Poll running tasks every 2.5s
  useEffect(() => {
    const activeIds = tasks.filter(t => t.status === 'running').map(t => t.id)
    if (activeIds.length === 0) return
    const interval = setInterval(async () => {
      const results = await Promise.allSettled(
        activeIds.map(id => fetchTaskStatus(id))
      )
      setTasks(prev => prev.map(t => {
        const idx = activeIds.indexOf(t.id)
        if (idx === -1) return t
        const r = results[idx]
        if (r.status === 'fulfilled') return r.value
        return t
      }))
    }, 2500)
    return () => clearInterval(interval)
  }, [tasks])

  useEffect(() => {
    if (statusMessage) {
      const t = setTimeout(() => setStatusMessage(''), 3000)
      return () => clearTimeout(t)
    }
  }, [statusMessage])

  const handleRegister = async () => {
    try {
      const r = await triggerRegister()
      const newTask: AsyncTask = {
        id: r.task_id,
        type: 'register',
        status: 'running',
        created_at: new Date().toISOString(),
        progress: '',
        result: null,
        error: undefined,
      }
      setTasks(prev => [newTask, ...prev])
      setStatusMessage('注册任务已创建')
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleGenerateLogin = async () => {
    try {
      const r = await generateAuthURL()
      window.open(r.auth_url, '_blank')
      setStatusMessage('已打开登录页面，请在浏览器中完成登录。完成后请手动刷新列表。')
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleRelogin = async (id: number) => {
    if (!confirm(`确认重新登录账号 #${id}？`)) return
    setActionLoading(id)
    try {
      const r = await triggerRelogin(id)
      const newTask: AsyncTask = {
        id: r.task_id,
        type: 'relogin',
        account_id: id,
        status: 'running',
        created_at: new Date().toISOString(),
        progress: '',
        result: null,
        error: undefined,
      }
      setTasks(prev => [newTask, ...prev])
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setActionLoading(null)
    }
  }

  const handleRefresh = async (id: number) => {
    setActionLoading(id)
    try {
      await refreshCodexAccount(id)
      setStatusMessage('刷新成功')
      load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setActionLoading(null)
    }
  }

  const handleHealthCheck = async (id: number) => {
    setActionLoading(id)
    try {
      await healthCheckCodexAccount(id)
      setStatusMessage('健康检查完成')
      load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setActionLoading(null)
    }
  }

  const handleDelete = async (id: number) => {
    if (!confirm(`确认删除账号 #${id}？此操作不可撤销！`)) return
    setActionLoading(id)
    try {
      await deleteCodexAccount(id)
      load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setActionLoading(null)
    }
  }

  const openEmailModal = (id: number, email: string) => {
    setEmailAccountId(id)
    setEmailCred({ email: email || '', provider: '', token: '' })
    setEmailModalOpen(true)
  }

  const handleSaveEmail = async () => {
    try {
      await setEmailCredential(emailAccountId, emailCred)
      setEmailModalOpen(false)
      load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleDeleteEmail = async () => {
    if (!confirm('确认删除该账号的邮箱凭证？')) return
    try {
      await deleteEmailCredential(emailAccountId)
      setEmailModalOpen(false)
      load()
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div>
      {/* Page header with browser status */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h2>Codex 账号管理</h2>
        <span style={{ fontSize: 13, color: browserConnected === null ? '#999' : browserConnected ? '#52c41a' : '#ff4d4f' }}>
          {browserConnected !== null && <span style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', marginRight: 6, background: browserConnected ? '#52c41a' : '#ff4d4f' }} />}
          {browserConnected === null ? '检查中...' : browserConnected ? '浏览器已连接' : '浏览器未连接'}
        </span>
      </div>

      {/* Status message (green success message that auto-clears after 3s) */}
      {statusMessage && <div style={{ color: '#52c41a', marginBottom: 8 }}>{statusMessage}</div>}

      {/* Error message */}
      {error && <div style={{ color: '#ff4d4f', marginBottom: 8 }}>{error}</div>}

      {/* Task Management Panel */}
      {tasks.length > 0 && (
        <div style={{ marginBottom: 16, border: '1px solid #f0f0f0', borderRadius: 6, overflow: 'hidden' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '8px 14px', background: '#fafafa', borderBottom: '1px solid #f0f0f0' }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: '#333' }}>任务管理 ({tasks.filter(t => t.status === 'running').length} 运行中)</span>
            <button onClick={() => setTasks(prev => prev.filter(t => t.status === 'running'))}
              style={{ padding: '2px 10px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 12 }}>
              清除已完成
            </button>
          </div>
          {tasks.map(task => {
            const isExpanded = expandedTaskId === task.id
            const isRunning = task.status === 'running'
            const elapsed = Math.floor((Date.now() - new Date(task.created_at).getTime()) / 1000)
            const elapsedStr = elapsed > 60 ? `${Math.floor(elapsed/60)}m${elapsed%60}s` : `${elapsed}s`
            const statusColor = task.status === 'running' ? '#1677ff' : task.status === 'done' ? '#52c41a' : '#ff4d4f'
            const statusBg = task.status === 'running' ? '#e6f7ff' : task.status === 'done' ? '#f6ffed' : '#fff2f0'
            const statusLabel = task.status === 'running' ? '运行中' : task.status === 'done' ? '已完成' : '失败'
            const typeLabel = task.type === 'register' ? '注册' : '重登录'

            return (
              <div key={task.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                <div onClick={() => setExpandedTaskId(isExpanded ? null : task.id)}
                  style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', cursor: 'pointer', background: isExpanded ? '#fafafa' : '#fff' }}>
                  <span style={{ fontSize: 11, transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)', transition: 'transform 0.2s', display: 'inline-block' }}>&#9654;</span>
                  <span style={{ fontSize: 12, color: '#333', minWidth: 40 }}>{typeLabel}</span>
                  <span style={{ display: 'inline-block', padding: '1px 8px', borderRadius: 4, fontSize: 11, color: statusColor, background: statusBg }}>{statusLabel}</span>
                  <code style={{ fontFamily: 'monospace', fontSize: 11, color: '#999', flex: 1 }}>{task.id.slice(0, 8)}...</code>
                  {task.progress && <span style={{ fontSize: 11, color: '#1677ff' }}>{task.progress}</span>}
                  <span style={{ fontSize: 11, color: '#999', whiteSpace: 'nowrap' }}>{isRunning ? elapsedStr : elapsedStr}</span>
                  {isRunning && <button onClick={e => { e.stopPropagation(); cancelTask(task.id).catch(() => {}); }}
                    style={{ padding: '1px 8px', border: '1px solid #ff4d4f', borderRadius: 3, background: '#fff', color: '#ff4d4f', cursor: 'pointer', fontSize: 11 }}>取消</button>}
                </div>
                {isExpanded && (
                  <div style={{ padding: '8px 14px 12px 34px', background: '#fafafa', fontSize: 12, color: '#666' }}>
                    <div style={{ marginBottom: 6 }}>
                      <span>类型: {typeLabel}</span>
                      {task.account_id != null && <span style={{ marginLeft: 16 }}>账号: #{task.account_id}</span>}
                      <span style={{ marginLeft: 16 }}>ID: <code style={{ fontFamily: 'monospace', fontSize: 11 }}>{task.id}</code></span>
                    </div>
                    <div style={{ marginBottom: 6 }}>创建: {new Date(task.created_at).toLocaleString('zh-CN')}</div>
                    {task.error && <div style={{ color: '#ff4d4f', marginBottom: 6 }}>错误: {task.error}</div>}
                    {task.progress_log && task.progress_log.length > 0 && (
                      <div style={{ marginTop: 4 }}>
                        <div style={{ fontWeight: 600, marginBottom: 4, color: '#333' }}>进度时间线:</div>
                        <div style={{ maxHeight: 200, overflowY: 'auto' }}>
                          {task.progress_log.map((entry: {step: string, timestamp: string}, i: number) => (
                            <div key={i} style={{ padding: '2px 0', fontFamily: 'monospace', fontSize: 11 }}>
                              <span style={{ color: '#999' }}>{new Date(entry.timestamp).toLocaleTimeString('zh-CN')}</span>
                              <span style={{ margin: '0 8px', color: '#333' }}>{entry.step}</span>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

      {/* Loading */}
      {loading && <p>加载中...</p>}

      {/* Action Bar */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        <button onClick={load} style={{ padding: '6px 16px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 13 }}>刷新列表</button>
        {isAdmin && <button onClick={handleRegister} style={{ padding: '6px 16px', border: '1px solid #1677ff', borderRadius: 4, background: '#1677ff', color: '#fff', cursor: 'pointer', fontSize: 13 }}>注册</button>}
        {isAdmin && <button onClick={handleGenerateLogin} style={{ padding: '6px 16px', border: '1px solid #1677ff', borderRadius: 4, background: '#fff', color: '#1677ff', cursor: 'pointer', fontSize: 13 }}>生成登录</button>}
      </div>

      {/* Config Section (read-only, admin only) */}
      {isAdmin && config && (
        <div style={{ marginBottom: 16, padding: 12, background: '#fafafa', borderRadius: 6, border: '1px solid #f0f0f0', fontSize: 13 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <span style={{ fontWeight: 600, color: '#333' }}>当前配置</span>
        <button onClick={() => { setEditConfig(config); setConfigModalOpen(true) }}
          style={{ padding: '2px 12px', border: '1px solid #1677ff', borderRadius: 4, background: '#fff', color: '#1677ff', cursor: 'pointer', fontSize: 12 }}>
          编辑配置
        </button>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(240px, 1fr))', gap: '4px 16px', color: '#666' }}>
        <span>自动注册: {config.auto_login_enabled ? '开启' : '关闭'} (间隔 {config.auto_login_interval})</span>
        <span>自动刷新: {config.auto_refresh_enabled ? '开启' : '关闭'} (间隔 {config.refresh_interval})</span>
        <span>健康检查: {config.auto_health_enabled ? '开启' : '关闭'} (间隔 {config.health_check_interval})</span>
        <span>最大并发: {config.max_concurrent_logins}</span>
        <span>代理: {config.proxy_url || '-'}</span>
        <span>浏览器: {config.browser_url || '未配置'}</span>
        <span>邮箱供应商: {config.default_email_provider || '未指定'}</span>
        <span>接码平台: {config.default_sms_provider || '未指定'}</span>
      </div>
      {/* Email Provider Settings */}
      {config.email_providers && Object.keys(config.email_providers).length > 0 && (
        <div style={{ marginTop: 8, padding: 8, background: '#fafafa', borderRadius: 4, fontSize: 12 }}>
          <strong style={{ color: '#333' }}>邮箱供应商配置:</strong>
          {Object.entries(config.email_providers).map(([name, settings]) => (
            <div key={name} style={{ marginTop: 4, color: '#666' }}>
              <span style={{ fontWeight: 500 }}>{name}</span>: API Key {settings.api_key ? '已配置' : '未配置'}
            </div>
          ))}
        </div>
      )}
      {/* SMS Provider Status */}
      {config.default_sms_provider && (
        <div style={{ marginTop: 8, padding: 8, background: '#fafafa', borderRadius: 4, fontSize: 12 }}>
          <strong style={{ color: '#333' }}>接码平台配置:</strong>
          <div style={{ marginTop: 4, color: '#666' }}>
            <span style={{ fontWeight: 500 }}>{config.default_sms_provider === '5sim' ? '5sim.net' : config.default_sms_provider}</span>: API Key {config.sms_provider_api_key ? '已配置' : '未配置'}
          </div>
        </div>
      )}
        </div>
      )}

      {/* Account Table */}
      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #f0f0f0', textAlign: 'left', whiteSpace: 'nowrap' }}>
              <th style={{ padding: '6px 8px' }}>ID</th>
              <th style={{ padding: '6px 8px' }}>邮箱</th>
              <th style={{ padding: '6px 8px' }}>Account ID</th>
              <th style={{ padding: '6px 8px' }}>状态</th>
              <th style={{ padding: '6px 8px' }}>计划</th>
              <th style={{ padding: '6px 8px' }}>最后健康</th>
              <th style={{ padding: '6px 8px' }}>最后刷新</th>
              <th style={{ padding: '6px 8px' }}>操作</th>
            </tr>
          </thead>
          <tbody>
            {accounts.length === 0 ? (
              <tr><td colSpan={8} style={{ padding: 24, textAlign: 'center', color: '#999' }}>暂无账号</td></tr>
            ) : accounts.map(a => (
              <tr key={a.id} style={{ borderBottom: '1px solid #f0f0f0' }}>
                <td style={{ padding: '6px 8px' }}>{a.id}</td>
                <td style={{ padding: '6px 8px', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.email}</td>
                <td style={{ padding: '6px 8px', fontFamily: 'monospace', fontSize: 12, maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.account_id}</td>
                <td style={{ padding: '6px 8px' }}>
                  <span style={{
                    display: 'inline-block', padding: '1px 6px', borderRadius: 4, fontSize: 12,
                    color: a.status === 'active' ? '#52c41a' : a.status === 'expired' ? '#faad14' : a.status === 'needs_login' ? '#fa8c16' : a.status === 'error' ? '#ff4d4f' : '#666',
                    background: a.status === 'active' ? '#f6ffed' : a.status === 'expired' ? '#fffbe6' : a.status === 'needs_login' ? '#fff7e6' : a.status === 'error' ? '#fff2f0' : '#fafafa',
                  }}>{a.status}</span>
                </td>
                <td style={{ padding: '6px 8px' }}>{a.plan_type}</td>
                <td style={{ padding: '6px 8px', fontSize: 12, color: '#666' }}>{a.last_health ? new Date(a.last_health).toLocaleString('zh-CN') : '-'}</td>
                <td style={{ padding: '6px 8px', fontSize: 12, color: '#666' }}>{a.last_refresh ? new Date(a.last_refresh).toLocaleString('zh-CN') : '-'}</td>
                <td style={{ padding: '6px 8px' }}>
                  <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                    <button onClick={() => handleRefresh(a.id)} disabled={actionLoading === a.id} style={{ padding: '2px 8px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: actionLoading === a.id ? 'not-allowed' : 'pointer', fontSize: 12, opacity: actionLoading === a.id ? 0.5 : 1 }}>刷新令牌</button>
                    <button onClick={() => handleHealthCheck(a.id)} disabled={actionLoading === a.id} style={{ padding: '2px 8px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: actionLoading === a.id ? 'not-allowed' : 'pointer', fontSize: 12, opacity: actionLoading === a.id ? 0.5 : 1 }}>健康检查</button>
                    {isAdmin && <button onClick={() => handleRelogin(a.id)} disabled={actionLoading === a.id} style={{ padding: '2px 8px', border: '1px solid #fa8c16', borderRadius: 4, background: '#fff', color: '#fa8c16', cursor: actionLoading === a.id ? 'not-allowed' : 'pointer', fontSize: 12, opacity: actionLoading === a.id ? 0.5 : 1 }}>重登录</button>}
                    {isAdmin && <button onClick={() => openEmailModal(a.id, a.email)} style={{ padding: '2px 8px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 12 }}>邮箱凭证</button>}
                    {isAdmin && <button onClick={() => handleDelete(a.id)} disabled={actionLoading === a.id} style={{ padding: '2px 8px', border: '1px solid #ff4d4f', borderRadius: 4, background: '#fff', color: '#ff4d4f', cursor: actionLoading === a.id ? 'not-allowed' : 'pointer', fontSize: 12, opacity: actionLoading === a.id ? 0.5 : 1 }}>删除</button>}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Email Credential Modal */}
      {emailModalOpen && (
        <Modal open={emailModalOpen} onClose={() => setEmailModalOpen(false)} title={`邮箱凭证 - 账号 #${emailAccountId}`}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label style={{ fontSize: 12, color: '#666' }}>
              邮箱
              <input type="email" value={emailCred.email} onChange={e => setEmailCred(prev => ({ ...prev, email: e.target.value }))}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              供应商
              <select value={emailCred.provider} onChange={e => setEmailCred(prev => ({ ...prev, provider: e.target.value }))}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }}>
                <option value="">选择供应商</option>
                <option value="tempmail">TempMail</option>
                <option value="mailtm">Mail.tm</option>
                <option value="other">其它</option>
              </select>
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              Token
              <input value={emailCred.token} onChange={e => setEmailCred(prev => ({ ...prev, token: e.target.value }))}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
              <button onClick={handleSaveEmail} style={{ padding: '6px 16px', border: '1px solid #1677ff', borderRadius: 4, background: '#1677ff', color: '#fff', cursor: 'pointer', fontSize: 13 }}>保存</button>
              <button onClick={handleDeleteEmail} style={{ padding: '6px 16px', border: '1px solid #ff4d4f', borderRadius: 4, background: '#fff', color: '#ff4d4f', cursor: 'pointer', fontSize: 13 }}>删除凭证</button>
              <button onClick={() => setEmailModalOpen(false)} style={{ padding: '6px 16px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 13 }}>取消</button>
            </div>
          </div>
        </Modal>
      )}

      {/* Config Edit Modal */}
      {configModalOpen && editConfig && (
        <Modal open={configModalOpen} onClose={() => setConfigModalOpen(false)} title="编辑 Codex 配置">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <label style={{ fontSize: 12, color: '#666', display: 'flex', alignItems: 'center', gap: 8 }}>
              <input type="checkbox" checked={editConfig.auto_login_enabled}
                onChange={e => setEditConfig(p => p ? { ...p, auto_login_enabled: e.target.checked } : null)} />
              自动注册
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              注册间隔 (如: 1h, 30m, 2h30m)
              <input value={editConfig.auto_login_interval}
                onChange={e => setEditConfig(p => p ? { ...p, auto_login_interval: e.target.value } : null)}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666', display: 'flex', alignItems: 'center', gap: 8 }}>
              <input type="checkbox" checked={editConfig.auto_refresh_enabled}
                onChange={e => setEditConfig(p => p ? { ...p, auto_refresh_enabled: e.target.checked } : null)} />
              自动刷新
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              刷新间隔 (如: 168h, 7d)
              <input value={editConfig.refresh_interval}
                onChange={e => setEditConfig(p => p ? { ...p, refresh_interval: e.target.value } : null)}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666', display: 'flex', alignItems: 'center', gap: 8 }}>
              <input type="checkbox" checked={editConfig.auto_health_enabled}
                onChange={e => setEditConfig(p => p ? { ...p, auto_health_enabled: e.target.checked } : null)} />
              健康检查
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              健康检查间隔 (如: 24h, 12h)
              <input value={editConfig.health_check_interval}
                onChange={e => setEditConfig(p => p ? { ...p, health_check_interval: e.target.value } : null)}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              最大并发注册数
              <input type="number" min={1} value={editConfig.max_concurrent_logins}
                onChange={e => setEditConfig(p => p ? { ...p, max_concurrent_logins: parseInt(e.target.value) || 1 } : null)}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              代理地址 (可选)
              <input value={editConfig.proxy_url}
                onChange={e => setEditConfig(p => p ? { ...p, proxy_url: e.target.value } : null)}
                placeholder="如: http://proxy:8080"
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              浏览器 WebSocket 地址
              <input value={editConfig.browser_url || ''}
                onChange={e => setEditConfig(p => p ? { ...p, browser_url: e.target.value } : null)}
                placeholder="ws://browserless:3000/chromium"
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label style={{ fontSize: 12, color: '#666' }}>
              默认邮箱供应商
              <select value={editConfig.default_email_provider || ''}
                onChange={e => setEditConfig(p => p ? { ...p, default_email_provider: e.target.value } : null)}
                style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }}>
                <option value="">不指定</option>
                <option value="tempmail_lol">TempMail.lol</option>
                <option value="mailondeck">MailOnDeck (API)</option>
                <option value="mailondeck_browserless">MailOnDeck (Browserless)</option>
              </select>
            </label>
            {/* Email Provider Settings */}
            <div style={{ borderTop: '1px solid #f0f0f0', paddingTop: 12, marginTop: 4 }}>
              <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 8, color: '#333' }}>邮箱供应商配置</div>
              {(['tempmail_lol', 'mailondeck'] as const).map(name => {
                const eps = editConfig?.email_providers?.[name]
                const updateField = (field: string, value: string) => {
                  setEditConfig(p => {
                    if (!p) return null
                    const providers = { ...(p.email_providers || {}) }
                    if (value || (providers[name] && Object.values({...providers[name], [field]: ''}).some(v => v !== ''))) {
                      providers[name] = { ...providers[name], [field]: value }
                    }
                    if (!value && providers[name]) {
                      // Check if all fields are empty
                      const cleaned = { ...providers[name], [field]: '' }
                      if (!cleaned.api_key && !cleaned.domain) {
                        delete providers[name]
                      } else {
                        providers[name] = cleaned
                      }
                    }
                    return { ...p, email_providers: Object.keys(providers).length > 0 ? providers : undefined }
                  })
                }
                return (
                  <div key={name} style={{ marginBottom: 12 }}>
                    <div style={{ fontSize: 12, fontWeight: 500, marginBottom: 4, color: '#444' }}>
                      {name === 'tempmail_lol' ? 'TempMail.lol' : 'MailOnDeck'}
                    </div>
                    <label style={{ fontSize: 12, color: '#666', display: 'block', marginBottom: 6 }}>
                      API Key
                      <input value={eps?.api_key || ''}
                        onChange={e => updateField('api_key', e.target.value)}
                        placeholder="tm.xxxxxxxxxxxxxxx"
                        style={{ display: 'block', marginTop: 2, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
                    </label>
                    <label style={{ fontSize: 12, color: '#666', display: 'block' }}>
                      自定义域名
                      <input value={eps?.domain || ''}
                        onChange={e => updateField('domain', e.target.value)}
                        placeholder="example.com"
                        style={{ display: 'block', marginTop: 2, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
                    </label>
                  </div>
                )
              })}
            </div>
            {/* SMS Provider Settings */}
            <div style={{ borderTop: '1px solid #f0f0f0', paddingTop: 12, marginTop: 4 }}>
              <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 8, color: '#333' }}>SMS Phone Verification (接码平台)</div>
              <label style={{ fontSize: 12, color: '#666' }}>
                接码平台
                <select value={editConfig.default_sms_provider || ''}
                  onChange={e => setEditConfig(p => {
                    if (!p) return null
                    const val = e.target.value
                    if (!val) return { ...p, default_sms_provider: '', sms_provider_api_key: undefined }
                    return { ...p, default_sms_provider: val }
                  })}
                  style={{ display: 'block', marginTop: 4, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }}>
                  <option value="">不指定</option>
                  <option value="5sim">5sim.net</option>
                </select>
              </label>
              {editConfig.default_sms_provider === '5sim' && (
                <label style={{ fontSize: 12, color: '#666', display: 'block', marginTop: 8 }}>
                  API Key
                  <input value={editConfig.sms_provider_api_key || ''}
                    onChange={e => setEditConfig(p => p ? { ...p, sms_provider_api_key: e.target.value } : null)}
                    placeholder="输入 5sim API Key"
                    style={{ display: 'block', marginTop: 2, padding: '6px 8px', border: '1px solid #d9d9d9', borderRadius: 4, fontSize: 13, width: '100%', boxSizing: 'border-box' }} />
                </label>
              )}
            </div>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
              <button onClick={async () => {
                try {
                  await updateCodexConfig(editConfig)
                  setConfigModalOpen(false)
                  load()
                } catch (e: unknown) {
                  setError(e instanceof Error ? e.message : String(e))
                }
              }} style={{ padding: '6px 16px', border: '1px solid #1677ff', borderRadius: 4, background: '#1677ff', color: '#fff', cursor: 'pointer', fontSize: 13 }}>保存</button>
              <button onClick={() => setConfigModalOpen(false)} style={{ padding: '6px 16px', border: '1px solid #d9d9d9', borderRadius: 4, background: '#fff', cursor: 'pointer', fontSize: 13 }}>取消</button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
