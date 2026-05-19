import { useState } from 'react'
import { useAuth } from '../auth/AuthContext'
import { useNavigate } from 'react-router-dom'

export default function Login() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      await login(username, password)
      navigate('/')
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '100vh', background: '#f5f5f5' }}>
      <form onSubmit={handleSubmit} style={{ background: '#fff', padding: 40, borderRadius: 8, boxShadow: '0 2px 8px rgba(0,0,0,0.1)', width: 360 }}>
        <h1 style={{ textAlign: 'center', marginBottom: 24 }}>Turapis 管理后台</h1>
        {error && <div style={{ color: '#ff4d4f', background: '#fff2f0', padding: 8, borderRadius: 4, marginBottom: 16 }}>{error}</div>}
        <label style={{ display: 'block', marginBottom: 8, fontWeight: 500 }}>用户名</label>
        <input
          type="text"
          value={username}
          onChange={e => setUsername(e.target.value)}
          style={{ width: '100%', padding: '8px 12px', border: '1px solid #d9d9d9', borderRadius: 6, fontSize: 14, boxSizing: 'border-box', marginBottom: 16 }}
          autoFocus
        />
        <label style={{ display: 'block', marginBottom: 8, fontWeight: 500 }}>密码</label>
        <input
          type="password"
          value={password}
          onChange={e => setPassword(e.target.value)}
          style={{ width: '100%', padding: '8px 12px', border: '1px solid #d9d9d9', borderRadius: 6, fontSize: 14, boxSizing: 'border-box', marginBottom: 16 }}
        />
        <button type="submit" disabled={loading} style={{ width: '100%', padding: '10px 0', background: '#1677ff', color: '#fff', border: 'none', borderRadius: 6, fontSize: 14, cursor: 'pointer' }}>
          {loading ? '登录中...' : '登录'}
        </button>
      </form>
    </div>
  )
}
