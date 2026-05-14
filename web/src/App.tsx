import { BrowserRouter, Routes, Route, Navigate, NavLink, Outlet } from 'react-router-dom'
import { AuthProvider, useAuth } from './auth/AuthContext'
import Login from './pages/Login'
import Providers from './pages/Providers'
import Routing from './pages/Routing'
import Dashboard from './pages/Dashboard'
import ApiKeys from './pages/ApiKeys'
import Sites from './pages/Sites'

function ProtectedLayout() {
  const { isAuthenticated, isLoading, logout } = useAuth()

  if (isLoading) {
    return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '100vh' }}>加载中...</div>
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  const linkStyle = ({ isActive }: { isActive: boolean }) => ({
    display: 'block',
    padding: '10px 16px',
    color: isActive ? '#1677ff' : '#333',
    background: isActive ? '#e6f4ff' : 'transparent',
    textDecoration: 'none',
    borderRadius: 6,
    marginBottom: 4,
    fontWeight: isActive ? 600 : 400,
  })

  return (
    <div style={{ display: 'flex', minHeight: '100vh' }}>
      <nav style={{ width: 220, background: '#fafafa', borderRight: '1px solid #f0f0f0', padding: 16 }}>
        <h1 style={{ fontSize: 18, marginBottom: 24, color: '#1677ff' }}>Turapis</h1>
        <NavLink to="/dashboard" style={linkStyle}>仪表盘</NavLink>
        <NavLink to="/providers" style={linkStyle}>供应商管理</NavLink>
        <NavLink to="/routing" style={linkStyle}>路由配置</NavLink>
        <NavLink to="/api-keys" style={linkStyle}>API Key 管理</NavLink>
        <NavLink to="/sites" style={linkStyle}>站点管理</NavLink>
        <div style={{ marginTop: 24 }}>
          <button onClick={logout} style={{ width: '100%', padding: '8px 0', background: '#fff', border: '1px solid #d9d9d9', borderRadius: 6, cursor: 'pointer' }}>退出登录</button>
        </div>
      </nav>
      <main style={{ flex: 1, padding: 24, background: '#fff' }}>
        <Outlet />
      </main>
    </div>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<ProtectedLayout />}>
            <Route path="/" element={<Navigate to="/dashboard" replace />} />
            <Route path="/dashboard" element={<Dashboard />} />
            <Route path="/providers" element={<Providers />} />
            <Route path="/routing" element={<Routing />} />
            <Route path="/api-keys" element={<ApiKeys />} />
            <Route path="/sites" element={<Sites />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  )
}
