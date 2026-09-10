import { apiGet, apiSend } from './client'

export type Direction = 'incoming' | 'outgoing'
// Categories are DB-backed and admin-editable (see api/categories.ts) — no
// longer a fixed set of literal values here.
export type Category = string

export interface TransactionListItem {
  id: number
  transaction_date: string
  amount: string
  currency: string
  direction: Direction
  category: Category
  member_number: number | null
  matched_by: string | null
  is_public_visible: boolean
  variable_symbol: string | null
  specific_symbol: string | null
  constant_symbol: string | null
  counter_account_number: string | null
  counter_account_name: string | null
  message_for_recipient: string | null
  user_identification: string | null
  comment: string | null
}

export interface TransactionsResponse {
  total: number
  limit: number
  offset: number
  transactions: TransactionListItem[]
}

export type MatchedBy = 'variable_symbol' | 'manual' | 'amount_heuristic'

export interface TransactionQuery {
  limit?: number
  offset?: number
  /** inclusive, YYYY-MM-DD */
  from?: string
  /** inclusive, YYYY-MM-DD */
  to?: string
  /** true = has a member, false = unassigned */
  assigned?: boolean
  direction?: Direction
  category?: Category
  matchedBy?: MatchedBy
  memberNumber?: number
}

export function fetchTransactions(q: TransactionQuery = {}): Promise<TransactionsResponse> {
  const params = new URLSearchParams()
  if (q.limit != null) params.set('limit', String(q.limit))
  if (q.offset != null) params.set('offset', String(q.offset))
  if (q.from) params.set('from', q.from)
  if (q.to) params.set('to', q.to)
  if (q.assigned != null) params.set('assigned', String(q.assigned))
  if (q.direction) params.set('direction', q.direction)
  if (q.category) params.set('category', q.category)
  if (q.matchedBy) params.set('matched_by', q.matchedBy)
  if (q.memberNumber != null) params.set('member_number', String(q.memberNumber))
  const qs = params.toString()
  return apiGet<TransactionsResponse>(`/api/transactions${qs ? `?${qs}` : ''}`)
}

export interface MonthRef {
  year: number
  month: number
}

export interface TransactionDetail extends TransactionListItem {
  covered_months: MonthRef[]
}

export interface AssignPayload {
  /** omit for a category-only edit — not every transaction has a member to match */
  member_number?: number
  category?: Category
  /** only for category === 'membership_fee' with a member_number; empty = the transaction's own month */
  covers?: MonthRef[]
}

/** 409 body from assignTransaction when a month is already covered elsewhere. */
export interface CoverageConflict {
  error: string
  conflicts: MonthRef[]
}

export function fetchTransaction(id: number): Promise<TransactionDetail> {
  return apiGet<TransactionDetail>(`/api/transactions/${id}`)
}

export function assignTransaction(id: number, payload: AssignPayload): Promise<TransactionDetail> {
  return apiSend<TransactionDetail>('PUT', `/api/transactions/${id}/assignment`, payload)
}

export function unassignTransaction(id: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/transactions/${id}/assignment`)
}
