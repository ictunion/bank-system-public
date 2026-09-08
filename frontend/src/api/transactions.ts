import { apiGet } from './client'

export type Direction = 'incoming' | 'outgoing'
export type Category = 'membership_fee' | 'salary' | 'other_income' | 'other_expense'

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
