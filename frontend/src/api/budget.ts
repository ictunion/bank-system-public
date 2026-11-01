import { apiGet } from './client'

export interface CategoryTotal {
  category_id: string
  /** the category's name, for display */
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
  /** true = sub-category totals reported under their parent category */
  rollup?: boolean
}

export function fetchCategorySummary(q: CategorySummaryQuery = {}): Promise<CategorySummary> {
  const params = new URLSearchParams()
  if (q.from) params.set('from', q.from)
  if (q.to) params.set('to', q.to)
  if (q.rollup) params.set('rollup', 'true')
  const qs = params.toString()
  return apiGet<CategorySummary>(`/api/transactions/summary${qs ? `?${qs}` : ''}`)
}

// --- Yearly budgets (always CZK) ---------------------------------------------

export interface BudgetSummary {
  year: number
  note: string | null
  created_at: string
}

export type BudgetLineKind = 'income' | 'expense'

export interface BudgetLine {
  id: number
  kind: BudgetLineKind
  group_name: string | null
  name: string
  /** decimal string, always positive; kind gives the sign */
  projected: string
  category_ids: string[]
  sort_order: number
}

export interface BudgetReportLine extends BudgetLine {
  /** that year's actual CZK amount in the line's categories, signed so it reads positive normally; a refund lowers it */
  real: string
}

export interface UnbudgetedTotal {
  category_id: string
  /** the category's name, for display */
  category: string
  direction: 'incoming' | 'outgoing'
  /** positive; direction gives the sign */
  real: string
}

export interface BudgetReport {
  year: number
  note: string | null
  created_at: string
  /** YYYY-MM-DD the actual figures were computed on */
  as_of: string
  lines: BudgetReportLine[]
  /** transactions in categories no line maps (internal transfers excluded) */
  unbudgeted: UnbudgetedTotal[]
  /** non-CZK transactions that year, left out of every figure */
  other_currency_count: number
}

export function fetchBudgets(): Promise<BudgetSummary[]> {
  return apiGet<BudgetSummary[]>('/api/budgets')
}

export function fetchBudget(year: number): Promise<BudgetReport> {
  return apiGet<BudgetReport>(`/api/budgets/${year}`)
}
