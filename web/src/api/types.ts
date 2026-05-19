export interface Provider {
  id: number
  name: string
  base_url: string
  api_key: string
  protocol: 'openai' | 'anthropic'
  priority: number
  enabled: boolean
  auth_mode: string
  supported_tools: string
  proxy: string
  quota?: QuotaInfo
  created_at: string
  updated_at: string
}

export interface QuotaInfo {
  primary?: QuotaEntry
  secondary?: QuotaEntry
  tertiary?: QuotaEntry
}

export interface QuotaEntry {
  used_percent: number
  reset_after_seconds: number
  window_minutes: number
}

export interface Site {
  id: number
  name: string
  base_url: string
  protocol: 'openai' | 'anthropic'
  auth_mode: string
  enabled: boolean
  created_at: string
  updated_at: string
  model_count?: number
}

export interface SiteModel {
  id: number
  site_id: number
  model_id: string
  model_name: string
}

export interface CreateProviderFromSiteResult {
  provider: Provider
  mappings_created: number
}

export interface ModelMapping {
  id: number
  model_name: string
  provider_id: number
  priority: number
  enabled: boolean
  created_at: string
}

export interface APIKeyPermissions {
  allowed_models?: string[]
  allowed_providers?: string[]
}

export interface APIKeyListItem {
  id: number
  key: string
  name: string
  enabled: boolean
  permissions: string
  created_at: string
}

export interface APIKeyCreated {
  id: number
  key: string
  name: string
  created_at: string
}

export interface ProviderStatus {
  name: string
  enabled: boolean
  discovered_models: number
  registered: boolean
}

export interface ServiceStatus {
  status: string
  provider_count: number
  registered_count: number
  providers: ProviderStatus[]
}

export interface AttemptRecord {
  provider: string
  status_code: number
  error: string
  duration_ms: number
  quota_before: string
  quota_after: string
  success: boolean
  attempt_num: number
}

export interface AccessLog {
  id: number
  timestamp: string
  api_key_id: number | null
  api_key_name: string
  method: string
  path: string
  model: string
  status_code: number
  tokens_in: number
  tokens_out: number
  duration_ms: number
  remote_ip: string
  request_id: string
  provider_name: string
  error_msg: string
  client_req: string
  client_resp: string
  upstream_req: string
  upstream_resp: string
  quota_before: string
  quota_after: string
  attempts_json: string
}

export interface AccessLogResponse {
  logs: AccessLog[]
  total: number
  page: number
  per_page: number
}

export interface DiscoverResult {
  provider: string
  models: { id: number; model_id: string; model_name: string }[]
  count: number
}

export interface User {
  id: number
  username: string
  role: 'admin' | 'user'
  enabled: boolean
  created_at: string
}

export interface LoginResponse {
  status: string
  username: string
  role: 'admin' | 'user'
}
