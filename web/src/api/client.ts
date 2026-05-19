import type { Provider, ModelMapping, APIKeyListItem, APIKeyCreated, ServiceStatus, DiscoverResult, Site, SiteModel, CreateProviderFromSiteResult, QuotaInfo, User, LoginResponse } from './types'

const API_BASE = ''

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(API_BASE + path, {
    ...options,
    credentials: 'include',
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  })
  if (res.status === 401 && window.location.pathname !== '/login') {
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  if (res.status === 204) return {} as T
  return res.json()
}

// --- Auth ---
export function login(username: string, password: string) {
  return request<LoginResponse>('/admin/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  })
}

export function logout() {
  return request<{ status: string }>('/admin/logout', { method: 'POST' })
}

// --- Providers ---
export function fetchProviders() {
  return request<Provider[]>('/admin/providers')
}

export function createProvider(p: Omit<Provider, 'id' | 'created_at' | 'updated_at'>) {
  return request<Provider>('/admin/providers', {
    method: 'POST',
    body: JSON.stringify(p),
  })
}

export function updateProvider(p: Provider) {
  return request<Provider>(`/admin/providers/${p.id}`, {
    method: 'PUT',
    body: JSON.stringify(p),
  })
}

export function deleteProvider(id: number) {
  return request<{ status: string }>(`/admin/providers/${id}`, { method: 'DELETE' })
}

// --- Model Mappings ---
export function fetchModelMappings() {
  return request<ModelMapping[]>('/admin/model-mappings')
}

export function createModelMapping(m: Omit<ModelMapping, 'id' | 'created_at'>) {
  return request<ModelMapping>('/admin/model-mappings', {
    method: 'POST',
    body: JSON.stringify(m),
  })
}

export function updateModelMapping(m: ModelMapping) {
  return request<ModelMapping>(`/admin/model-mappings/${m.id}`, {
    method: 'PUT',
    body: JSON.stringify(m),
  })
}

export function deleteModelMapping(id: number) {
  return request<{ status: string }>(`/admin/model-mappings/${id}`, { method: 'DELETE' })
}

// --- Model Discovery ---
export function discoverModels(providerId: number) {
  return request<DiscoverResult>(`/admin/providers/${providerId}/discover`, { method: 'POST' })
}

export function discoverAllModels(body?: { provider_ids?: number[] }) {
  return request<{ results: Array<{ provider: string; count: number; error?: string }>; total: number }>('/admin/providers/batch-discover', {
    method: 'POST',
    body: body ? JSON.stringify(body) : undefined,
  })
}

// --- Settings ---
export function fetchSettings() {
  return request<{ default_priority_chain: string }>('/admin/settings')
}

export function updateSettings(chain: string) {
  return request<{ status: string }>('/admin/settings', {
    method: 'PUT',
    body: JSON.stringify({ default_priority_chain: chain }),
  })
}

// --- Status ---
export function fetchStatus() {
  return request<ServiceStatus>('/admin/status')
}

// --- API Keys ---
export function fetchAPIKeys() {
  return request<APIKeyListItem[]>('/admin/api-keys')
}

export function createAPIKey(name: string) {
  return request<APIKeyCreated>('/admin/api-keys', {
    method: 'POST',
    body: JSON.stringify({ name }),
  })
}

export function revokeAPIKey(id: number) {
  return request<{ status: string }>(`/admin/api-keys/${id}`, { method: 'DELETE' })
}

export function updateAPIKey(id: number, data: { name?: string; enabled?: boolean; permissions?: string }) {
  return request<{ status: string }>(`/admin/api-keys/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  })
}

// --- Sites ---
export function fetchSites() {
  return request<Site[]>('/admin/sites')
}

export function createSite(s: Omit<Site, 'id' | 'created_at' | 'updated_at' | 'model_count'>) {
  return request<Site>('/admin/sites', { method: 'POST', body: JSON.stringify(s) })
}

export function updateSite(s: Site) {
  return request<Site>(`/admin/sites/${s.id}`, { method: 'PUT', body: JSON.stringify(s) })
}

export function deleteSite(id: number) {
  return request<{ status: string }>(`/admin/sites/${id}`, { method: 'DELETE' })
}

export function fetchSiteModels(siteId: number) {
  return request<SiteModel[]>(`/admin/sites/${siteId}/models`)
}

export function addSiteModel(siteId: number, data: { model_id: string; model_name: string }) {
  return request<{ status: string }>(`/admin/sites/${siteId}/models`, { method: 'POST', body: JSON.stringify(data) })
}

export function deleteSiteModel(siteId: number, modelId: number) {
  return request<{ status: string }>(`/admin/sites/${siteId}/models/${modelId}`, { method: 'DELETE' })
}

export function probeProviderQuota(providerId: number) {
  return request<{ provider: string; status: string; quota: { primary?: { used_percent: number; reset_after_seconds: number; window_minutes: number }; secondary?: { used_percent: number; reset_after_seconds: number; window_minutes: number } } }>(`/admin/providers/${providerId}/quota`, { method: 'POST' })
}

export function batchProbeQuota(body?: { provider_ids?: number[] }) {
  return request<{ results: Array<{ provider: string; status: string; quota?: QuotaInfo; error?: string }>; total: number }>('/admin/providers/batch-quota', {
    method: 'POST',
    body: body ? JSON.stringify(body) : undefined,
  })
}

export function createProviderFromSite(siteId: number, data: { name_override?: string; api_key?: string; oauth?: object }) {
  return request<CreateProviderFromSiteResult>(`/admin/sites/${siteId}/create-provider`, { method: 'POST', body: JSON.stringify(data) })
}

// --- Users ---
export function fetchUsers() {
  return request<User[]>('/admin/users')
}

export function createUser(data: { username: string; password: string; role: string }) {
  return request<User>('/admin/users', { method: 'POST', body: JSON.stringify(data) })
}

export function updateUser(id: number, data: { username?: string; password?: string; role?: string; enabled?: boolean }) {
  return request<User>(`/admin/users/${id}`, { method: 'PUT', body: JSON.stringify(data) })
}

export function deleteUser(id: number) {
  return request<{ status: string }>(`/admin/users/${id}`, { method: 'DELETE' })
}
