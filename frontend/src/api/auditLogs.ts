import { apiGet } from './client'

export const AUDIT_CATEGORIES = ['bank_account', 'processing', 'transaction', 'category', 'waiver', 'other'] as const
export type AuditCategory = (typeof AUDIT_CATEGORIES)[number]

export interface AuditLogItem {
  id: number
  created_at: string
  /** Keycloak subject of the caller — the stable identity, used by the actorSub filter */
  actor_sub: string
  actor_email: string | null
  category: AuditCategory
  /** e.g. create_bank_account, assign_transaction */
  action: string
  method: string
  /** matched route pattern, e.g. "PUT /transactions/{id}/assignment" */
  route: string
  /** concrete request path, e.g. "/transactions/42/assignment" */
  path: string
  /** response status — non-2xx entries are rejected attempts, not changes */
  status_code: number
}

export interface AuditLogsResponse {
  total: number
  limit: number
  offset: number
  logs: AuditLogItem[]
}

export interface AuditLogQuery {
  limit?: number
  offset?: number
  category?: AuditCategory
  actorSub?: string
  /** inclusive, YYYY-MM-DD */
  from?: string
  /** inclusive, YYYY-MM-DD */
  to?: string
}

export function fetchAuditLogs(q: AuditLogQuery = {}): Promise<AuditLogsResponse> {
  const params = new URLSearchParams()
  if (q.limit != null) params.set('limit', String(q.limit))
  if (q.offset != null) params.set('offset', String(q.offset))
  if (q.category) params.set('category', q.category)
  if (q.actorSub) params.set('actor_sub', q.actorSub)
  if (q.from) params.set('from', q.from)
  if (q.to) params.set('to', q.to)
  const qs = params.toString()
  return apiGet<AuditLogsResponse>(`/api/audit-logs${qs ? `?${qs}` : ''}`)
}
