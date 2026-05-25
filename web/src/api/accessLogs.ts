import type { AccessLog, AccessLogResponse, AccessLogStatsResponse } from './types'

export async function fetchAccessLogs(params: {
  key_id?: number; model?: string; status?: number;
  from?: string; to?: string; page?: number; per_page?: number;
}): Promise<AccessLogResponse> {
  const searchParams = new URLSearchParams()
  if (params.key_id !== undefined) searchParams.set('key_id', String(params.key_id))
  if (params.model) searchParams.set('model', params.model)
  if (params.status !== undefined) searchParams.set('status', String(params.status))
  if (params.from) searchParams.set('from', params.from)
  if (params.to) searchParams.set('to', params.to)
  if (params.page !== undefined) searchParams.set('page', String(params.page))
  if (params.per_page !== undefined) searchParams.set('per_page', String(params.per_page))
  const qs = searchParams.toString()
  const res = await fetch(`/admin/access-logs${qs ? '?' + qs : ''}`, { credentials: 'include' })
  if (res.status === 401 && window.location.pathname !== '/login') {
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function fetchAccessLogStats(params: {
  from: string
  to: string
  interval?: number
}): Promise<AccessLogStatsResponse> {
  const searchParams = new URLSearchParams()
  if (params.from) searchParams.set('from', params.from)
  if (params.to) searchParams.set('to', params.to)
  if (params.interval !== undefined) searchParams.set('interval', String(params.interval))
  const qs = searchParams.toString()
  const res = await fetch(`/admin/access-logs/stats${qs ? '?' + qs : ''}`, { credentials: 'include' })
  if (res.status === 401 && window.location.pathname !== '/login') {
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function fetchAccessLogDetail(id: number): Promise<AccessLog> {
  const res = await fetch(`/admin/access-logs/${id}`, { credentials: 'include' })
  if (res.status === 401 && window.location.pathname !== '/login') {
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  return res.json()
}
