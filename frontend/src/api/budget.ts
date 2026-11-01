import { apiGet, apiSend } from './client'

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
  budgeted: string
  category_ids: string[]
  sort_order: number
  note: string | null
}

export interface BudgetReportLine extends BudgetLine {
  /** that year's actual CZK amount in the line's categories, signed so it reads positive normally; a refund lowers it */
  actual: string
}

export interface UnbudgetedTotal {
  category_id: string
  /** the category's name, for display */
  category: string
  direction: 'incoming' | 'outgoing'
  /** positive; direction gives the sign */
  actual: string
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

export function createBudget(year: number, note: string | null, copyFromYear: number | null): Promise<BudgetSummary> {
  return apiSend<BudgetSummary>('POST', '/api/budgets', { year, note, copy_from_year: copyFromYear })
}

export function updateBudgetNote(year: number, note: string): Promise<BudgetSummary> {
  return apiSend<BudgetSummary>('PATCH', `/api/budgets/${year}`, { note })
}

export function deleteBudget(year: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/budgets/${year}`)
}

// Body of both create and update — update is a full overwrite, category_ids
// included. A category may feed only one line per year; the backend answers
// 409 naming the line that already has it.
export interface BudgetLineInput {
  kind: BudgetLineKind
  group_name: string | null
  name: string
  /** decimal string >= 0, at most two decimals */
  budgeted: string
  category_ids: string[]
  sort_order: number
  /** null or '' clears it */
  note: string | null
}

export function lineToInput(line: BudgetLine): BudgetLineInput {
  return {
    kind: line.kind,
    group_name: line.group_name,
    name: line.name,
    budgeted: line.budgeted,
    category_ids: line.category_ids,
    sort_order: line.sort_order,
    note: line.note,
  }
}

export function createBudgetLine(year: number, input: BudgetLineInput): Promise<BudgetLine> {
  return apiSend<BudgetLine>('POST', `/api/budgets/${year}/lines`, input)
}

export function updateBudgetLine(year: number, id: number, input: BudgetLineInput): Promise<BudgetLine> {
  return apiSend<BudgetLine>('PUT', `/api/budgets/${year}/lines/${id}`, input)
}

export function deleteBudgetLine(year: number, id: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/budgets/${year}/lines/${id}`)
}
