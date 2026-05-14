export interface Provider {
  id: number
  name: string
  base_url: string
  api_key: string
  protocol: 'openai' | 'anthropic'
  priority: number
  enabled: boolean
  auth_mode: string
  created_at: string
  updated_at: string
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

export interface APIKeyListItem {
  id: number
  key: string
  name: string
  enabled: boolean
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

export interface DiscoverResult {
  provider: string
  models: { id: number; model_id: string; model_name: string }[]
  count: number
}
