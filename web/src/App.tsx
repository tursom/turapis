import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation, Outlet } from 'react-router-dom'
import { ConfigProvider, Layout, Menu, Button, Typography } from 'antd'
import {
  DashboardOutlined,
  CloudServerOutlined,
  NodeIndexOutlined,
  KeyOutlined,
  GlobalOutlined,
  FileTextOutlined,
  LogoutOutlined,
  ApiOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { AuthProvider, useAuth } from './auth/AuthContext'
import Login from './pages/Login'
import Providers from './pages/Providers'
import Routing from './pages/Routing'
import Dashboard from './pages/Dashboard'
import ApiKeys from './pages/ApiKeys'
import Sites from './pages/Sites'
import AccessLogs from './pages/AccessLogs'
import Users from './pages/Users'
import CodexAccounts from './pages/CodexAccounts'

const { Sider, Content } = Layout

const menuItems = [
  { key: '/dashboard', icon: <DashboardOutlined />, label: '仪表盘' },
  { key: '/providers', icon: <CloudServerOutlined />, label: '供应商管理' },
  { key: '/routing', icon: <NodeIndexOutlined />, label: '路由配置' },
  { key: '/api-keys', icon: <KeyOutlined />, label: 'API Key 管理' },
  { key: '/sites', icon: <GlobalOutlined />, label: '站点管理' },
  { key: '/access-logs', icon: <FileTextOutlined />, label: '访问日志' },
  { key: '/users', icon: <UserOutlined />, label: '用户管理' },
  { key: '/codex-accounts', icon: <ApiOutlined />, label: 'Codex 账号' },
]

function ProtectedLayout() {
  const { isAuthenticated, isLoading, username, role, logout } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  if (isLoading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '100vh' }}>
        加载中...
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  const selectedKey = menuItems.find((item) => location.pathname.startsWith(item.key))?.key ?? '/dashboard'

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider
        width={220}
        style={{
          background: '#fff',
          borderRight: '1px solid #f0f0f0',
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '16px 20px',
            borderBottom: '1px solid #f0f0f0',
          }}
        >
          <ApiOutlined style={{ fontSize: 22, color: '#1677ff' }} />
          <Typography.Text strong style={{ fontSize: 18, color: '#1677ff' }}>
            Turapis
          </Typography.Text>
        </div>
        <Menu
          mode="inline"
          selectedKeys={[selectedKey]}
          items={menuItems}
          onClick={({ key }) => navigate(key)}
          style={{ border: 'none', marginTop: 8 }}
        />
        <div style={{ position: 'absolute', bottom: 0, left: 0, right: 0, padding: '12px 16px', borderTop: '1px solid #f0f0f0' }}>
          <div style={{ marginBottom: 8, fontSize: 13 }}>
            <UserOutlined style={{ marginRight: 6 }} />
            {username}
            <span style={{ marginLeft: 6, color: '#999', fontSize: 12 }}>({role === 'admin' ? '管理员' : '普通用户'})</span>
          </div>
          <Button
            icon={<LogoutOutlined />}
            onClick={logout}
            block
            style={{ color: '#8c8c8c' }}
          >
            退出登录
          </Button>
        </div>
      </Sider>
      <Content style={{ padding: 24, background: '#f5f5f5', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        <Outlet />
      </Content>
    </Layout>
  )
}

export default function App() {
  return (
    <ConfigProvider
      theme={{
        token: {
          colorPrimary: '#1677ff',
          borderRadius: 6,
        },
      }}
    >
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
              <Route path="/access-logs" element={<AccessLogs />} />
              <Route path="/users" element={<Users />} />
              <Route path="/codex-accounts" element={<CodexAccounts />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </AuthProvider>
      </BrowserRouter>
    </ConfigProvider>
  )
}
