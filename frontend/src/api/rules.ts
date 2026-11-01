import { apiGet, apiSend } from './client'

// pattern rules are plain data and fully editable; the two built-in kinds
// (seeded by migration) only allow priority/enabled/note edits.
export type RuleKind = 'pattern' | 'own_account' | 'member_variable_symbol'

export interface CategorizationRule {
  id: number
  priority: number
  kind: RuleKind
  category: string
  enabled: boolean
  counter_account_number: string | null
  variable_symbol: string | null
  constant_symbol: string | null
  text_contains: string | null
  transaction_type_contains: string | null
  direction: 'incoming' | 'outgoing' | null
  note: string | null
  created_at: string
}

// Body of both create and update. Update is a full overwrite, so every field
// is always sent; null (or a blank string) clears it.
export interface CategorizationRuleInput {
  priority: number
  category: string
  enabled: boolean
  counter_account_number: string | null
  variable_symbol: string | null
  constant_symbol: string | null
  text_contains: string | null
  transaction_type_contains: string | null
  direction: 'incoming' | 'outgoing' | null
  note: string | null
}

export function ruleToInput(rule: CategorizationRule): CategorizationRuleInput {
  return {
    priority: rule.priority,
    category: rule.category,
    enabled: rule.enabled,
    counter_account_number: rule.counter_account_number,
    variable_symbol: rule.variable_symbol,
    constant_symbol: rule.constant_symbol,
    text_contains: rule.text_contains,
    transaction_type_contains: rule.transaction_type_contains,
    direction: rule.direction,
    note: rule.note,
  }
}

export function fetchRules(): Promise<CategorizationRule[]> {
  return apiGet<CategorizationRule[]>('/api/categorization-rules')
}

export function createRule(input: CategorizationRuleInput): Promise<CategorizationRule> {
  return apiSend<CategorizationRule>('POST', '/api/categorization-rules', input)
}

export function updateRule(id: number, input: CategorizationRuleInput): Promise<CategorizationRule> {
  return apiSend<CategorizationRule>('PUT', `/api/categorization-rules/${id}`, input)
}

export function deleteRule(id: number): Promise<void> {
  return apiSend<void>('DELETE', `/api/categorization-rules/${id}`)
}

export interface ReapplyRuleResult {
  changed: number
  failed: number
}

// Applies only this rule to every already-processed transaction it matches
// dated on or after from (YYYY-MM-DD, inclusive; the backend refuses a date
// before 1 January of the current year) — no other rule is evaluated. Manual
// edits and transactions decided by an earlier rule are kept; 400 for a
// disabled rule. Needs manage-transactions.
export async function reapplyRule(id: number, from: string): Promise<ReapplyRuleResult> {
  const result = await apiSend<{ succeeded: number; failed: number }>(
    'POST',
    `/api/categorization-rules/${id}/reapply`,
    { from },
  )
  return { changed: result.succeeded, failed: result.failed }
}
