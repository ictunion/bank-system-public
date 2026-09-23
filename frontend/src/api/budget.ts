import { apiGet } from './client'

export interface CategoryTotal {
  category: string
  currency: string
  total: string
}

export interface CurrencyTotal {
  currency: string
  total: string
}

export interface CategorySummary {
  incoming: CategoryTotal[]
  outgoing: CategoryTotal[]
  /** Live total across every bank account, grouped by currency — not scoped by the from/to query, there's no "balance as of a date range". */
  current_balance: CurrencyTotal[]
}

export interface CategorySummaryQuery {
  /** inclusive, YYYY-MM-DD */
  from?: string
  /** inclusive, YYYY-MM-DD */
  to?: string
}

export function fetchCategorySummary(q: CategorySummaryQuery = {}): Promise<CategorySummary> {
  const params = new URLSearchParams()
  if (q.from) params.set('from', q.from)
  if (q.to) params.set('to', q.to)
  const qs = params.toString()
  return apiGet<CategorySummary>(`/api/transactions/summary${qs ? `?${qs}` : ''}`)
}
