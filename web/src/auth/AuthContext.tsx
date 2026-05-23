import { createContext, useContext, useState, useEffect, type ReactNode } from 'react'
import { login as apiLogin, logout as apiLogout, fetchStatus, fetchMe } from '../api/client'

interface AuthContextType {
  isAuthenticated: boolean
  isLoading: boolean
  username: string
  role: string
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextType | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [username, setUsername] = useState('')
  const [role, setRole] = useState('')

  useEffect(() => {
    fetchStatus()
      .then(async () => {
        setIsAuthenticated(true)
        try {
          const user = await fetchMe()
          setUsername(user.username)
          setRole(user.role)
        } catch {
          setIsAuthenticated(true)
        }
      })
      .catch(() => setIsAuthenticated(false))
      .finally(() => setIsLoading(false))
  }, [])

  const login = async (username: string, password: string) => {
    const res = await apiLogin(username, password)
    setUsername(res.username)
    setRole(res.role)
    setIsAuthenticated(true)
  }

  const logout = async () => {
    await apiLogout()
    setUsername('')
    setRole('')
    setIsAuthenticated(false)
  }

  return (
    <AuthContext.Provider value={{ isAuthenticated, isLoading, username, role, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
