import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { categoryLabelById, categoryOptionLabel, fetchCategories, withChildrenUnderParents } from './api/categories'
import { errMessage } from './api/client'
import {
  type CategorizationRule,
  type CategorizationRuleInput,
  createRule,
  deleteRule,
  fetchRules,
  reapplyRule,
  ruleToInput,
  updateRule,
} from './api/rules'
import { Overlay } from './Overlay'
import { useHasRole } from './roles'

const COLUMNS = ['On', 'Priority', 'Conditions', 'Category', 'Note', ''] as const

// Default priority for a new rule: after member matching (300), before the
// seeded "mzda" salary rule (500) — where a counter-account rule like the
// lawyer's belongs.
const DEFAULT_NEW_PRIORITY = 400

function conditionsText(rule: CategorizationRule): string {
  if (rule.kind === 'own_account') return 'Counter account is one of our bank accounts'
  if (rule.kind === 'member_variable_symbol') return 'Variable symbol matches a member'

  const parts: string[] = []
  if (rule.counter_account_number) parts.push(`counter account = ${rule.counter_account_number}`)
  if (rule.variable_symbol) parts.push(`VS = ${rule.variable_symbol}`)
  if (rule.constant_symbol) parts.push(`KS = ${rule.constant_symbol}`)
  if (rule.text_contains) parts.push(`text contains "${rule.text_contains}"`)
  if (rule.transaction_type_contains) parts.push(`type contains "${rule.transaction_type_contains}"`)
  if (rule.direction) parts.push(rule.direction)
  return parts.join(' and ')
}

export function RulesPage() {
  // view-rules alone sees the table read-only; manage-rules edits and re-applies.
  const canManage = useHasRole('manage-rules')
  // null = closed, 'new' = add dialog, a rule = edit dialog
  const [editing, setEditing] = useState<CategorizationRule | 'new' | null>(null)
  const [reapplying, setReapplying] = useState<CategorizationRule | null>(null)

  const { data, isPending, isError, error } = useQuery({
    queryKey: ['categorization-rules'],
    queryFn: fetchRules,
  })

  const memberRule = data?.find((r) => r.kind === 'member_variable_symbol')

  return (
    <section>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
        <h2 style={{ margin: 0 }}>Rules</h2>
        {canManage && <button onClick={() => setEditing('new')}>Add rule</button>}
      </div>
      <p style={{ margin: '0 0 0.75rem', color: '#666', fontSize: '0.875rem' }}>
        Evaluated top to bottom for every new transaction; the first matching rule decides the category. A new or
        changed rule doesn't affect already-processed transactions until you click "Re-apply" on its row, which
        applies only that rule to history. Categories themselves are managed on the Categories page.
      </p>

      {isPending ? (
        <p>Loading rules…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
            <thead>
              <tr>
                {COLUMNS.map((h, i) => (
                  <th key={i} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.map((rule) => (
                <RuleRow
                  key={rule.id}
                  rule={rule}
                  canManage={canManage}
                  onEdit={() => setEditing(rule)}
                  onReapply={() => setReapplying(rule)}
                />
              ))}
              <tr style={{ color: '#666' }}>
                <td style={td} />
                <td style={td}>last</td>
                <td style={{ ...td, whiteSpace: 'normal' }}>Everything else (fallback, always on)</td>
                <td style={td}>Other income / Other expense by direction</td>
                <td style={td} />
                <td style={td} />
              </tr>
            </tbody>
          </table>
        </div>
      )}

      {editing !== null && (
        <RuleDialog
          rule={editing === 'new' ? null : editing}
          memberRulePriority={memberRule?.priority}
          onClose={() => setEditing(null)}
        />
      )}
      {reapplying !== null && <ReapplyDialog rule={reapplying} onClose={() => setReapplying(null)} />}
    </section>
  )
}

function RuleRow({
  rule,
  canManage,
  onEdit,
  onReapply,
}: {
  rule: CategorizationRule
  canManage: boolean
  onEdit: () => void
  onReapply: () => void
}) {
  const qc = useQueryClient()
  // Same cached query as the rest of the page; only for category labels.
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })
  const [rowError, setRowError] = useState<string | null>(null)
  const isMemberRule = rule.kind === 'member_variable_symbol'

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => updateRule(rule.id, { ...ruleToInput(rule), enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['categorization-rules'] }),
    onError: (err) => setRowError(errMessage(err)),
  })
  const remove = useMutation({
    mutationFn: () => deleteRule(rule.id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['categorization-rules'] }),
    onError: (err) => setRowError(errMessage(err)),
  })

  const onToggle = (enabled: boolean) => {
    setRowError(null)
    if (
      !enabled &&
      isMemberRule &&
      !window.confirm(
        'With member matching off, no membership fees get matched to members and every member will show missed payments. Disable it?',
      )
    ) {
      return
    }
    toggle.mutate(enabled)
  }

  // The member-match row is emphasized so it's clear which rules run before it.
  const rowStyle: CSSProperties = {
    opacity: rule.enabled ? 1 : 0.45,
    background: isMemberRule ? '#f3f6fb' : undefined,
    fontWeight: isMemberRule ? 600 : undefined,
  }

  return (
    <tr style={rowStyle}>
      <td style={td}>
        <input
          type="checkbox"
          checked={rule.enabled}
          disabled={!canManage || toggle.isPending}
          onChange={(e) => onToggle(e.target.checked)}
          title={rule.enabled ? 'Enabled' : 'Disabled'}
        />
      </td>
      <td style={td}>{rule.priority}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        {conditionsText(rule)}
        {rule.kind !== 'pattern' && <span style={{ color: '#999', fontWeight: 400 }}> (built-in)</span>}
      </td>
      <td style={td}>{categoryLabelById(categories.data, rule.category_id)}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>{rule.note}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        {canManage && (
          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
            <button onClick={onEdit}>Edit</button>
            <button
              disabled={!rule.enabled}
              title={
                rule.enabled
                  ? 'Apply only this rule to already-processed transactions it matches (manual edits and earlier rules are kept)'
                  : 'Enable the rule first'
              }
              onClick={onReapply}
            >
              Re-apply
            </button>
            {rule.kind === 'pattern' && (
              <button
                disabled={remove.isPending}
                onClick={() => {
                  setRowError(null)
                  if (window.confirm(`Delete rule "${conditionsText(rule)}"?`)) remove.mutate()
                }}
              >
                {remove.isPending ? 'Deleting…' : 'Delete'}
              </button>
            )}
            {rowError && <span style={{ color: '#b00', fontWeight: 400 }}>{rowError}</span>}
          </div>
        )}
      </td>
    </tr>
  )
}

// YYYY-MM-DD of 1 January this year (local time) — the earliest date a rule
// can be re-applied from; the backend refuses anything earlier too, so a
// closed year's categorization never changes.
function startOfCurrentYear(): string {
  return `${new Date().getFullYear()}-01-01`
}

function ReapplyDialog({ rule, onClose }: { rule: CategorizationRule; onClose: () => void }) {
  const qc = useQueryClient()
  // Same cached query as the rest of the page; only for category labels.
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })
  // override-rules (superadmins) may pick any date, closed years included.
  const canOverride = useHasRole('override-rules')
  const yearStart = startOfCurrentYear()
  const minimum = canOverride ? undefined : yearStart
  const [from, setFrom] = useState(yearStart)

  const reapply = useMutation({
    mutationFn: () => reapplyRule(rule.id, from),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['transactions'] }),
  })

  // ISO dates compare correctly as strings.
  const fromValid = /^\d{4}-\d{2}-\d{2}$/.test(from) && (minimum === undefined || from >= minimum)

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Re-apply rule</h2>
      <p>
        {conditionsText(rule)} → {categoryLabelById(categories.data, rule.category_id)}
      </p>
      <p style={{ color: '#666', fontSize: '0.875rem' }}>
        Applies only this rule to already-processed transactions it matches. Other rules are not applied; manual
        edits and transactions decided by an earlier rule are kept. Transactions before the date below stay as they
        are.{' '}
        {minimum !== undefined
          ? `It can't go before ${minimum}, so earlier years never change.`
          : 'You can pick a date in a closed year — budgets already reported for it may change.'}
      </p>

      {reapply.data ? (
        <>
          <p style={{ color: '#080' }}>
            Changed {reapply.data.changed}
            {reapply.data.failed > 0 ? ` (${reapply.data.failed} failed)` : ''}
          </p>
          <button onClick={onClose}>Close</button>
        </>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (fromValid && !reapply.isPending) reapply.mutate()
          }}
        >
          <label style={field}>
            From (inclusive)
            <input type="date" value={from} min={minimum} onChange={(e) => setFrom(e.target.value)} />
          </label>
          {!fromValid && (
            <p style={{ color: '#b00' }}>{minimum !== undefined ? `Choose a date on or after ${minimum}.` : 'Choose a date.'}</p>
          )}
          {reapply.error && <p style={{ color: '#b00' }}>{errMessage(reapply.error)}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
            <button type="submit" disabled={!fromValid || reapply.isPending}>
              {reapply.isPending ? 'Re-applying…' : 'Re-apply'}
            </button>
            <button type="button" onClick={onClose}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </Overlay>
  )
}

// Form state keeps every text field as a string; blanks are sent as null.
interface RuleForm {
  priority: string
  categoryId: string
  enabled: boolean
  counterAccountNumber: string
  variableSymbol: string
  constantSymbol: string
  textContains: string
  transactionTypeContains: string
  direction: '' | 'incoming' | 'outgoing'
  note: string
}

function formFromRule(rule: CategorizationRule | null): RuleForm {
  return {
    priority: String(rule?.priority ?? DEFAULT_NEW_PRIORITY),
    categoryId: rule?.category_id ?? '',
    enabled: rule?.enabled ?? true,
    counterAccountNumber: rule?.counter_account_number ?? '',
    variableSymbol: rule?.variable_symbol ?? '',
    constantSymbol: rule?.constant_symbol ?? '',
    textContains: rule?.text_contains ?? '',
    transactionTypeContains: rule?.transaction_type_contains ?? '',
    direction: rule?.direction ?? '',
    note: rule?.note ?? '',
  }
}

function blankToNull(value: string): string | null {
  const trimmed = value.trim()
  return trimmed === '' ? null : trimmed
}

function inputFromForm(form: RuleForm): CategorizationRuleInput {
  return {
    priority: Number(form.priority),
    category_id: form.categoryId,
    enabled: form.enabled,
    counter_account_number: blankToNull(form.counterAccountNumber),
    variable_symbol: blankToNull(form.variableSymbol),
    constant_symbol: blankToNull(form.constantSymbol),
    text_contains: blankToNull(form.textContains),
    transaction_type_contains: blankToNull(form.transactionTypeContains),
    direction: form.direction === '' ? null : form.direction,
    note: blankToNull(form.note),
  }
}

function RuleDialog({
  rule,
  memberRulePriority,
  onClose,
}: {
  rule: CategorizationRule | null
  memberRulePriority: number | undefined
  onClose: () => void
}) {
  const qc = useQueryClient()
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })
  const [form, setForm] = useState<RuleForm>(() => formFromRule(rule))
  const set = (patch: Partial<RuleForm>) => setForm((previous) => ({ ...previous, ...patch }))

  const builtIn = rule !== null && rule.kind !== 'pattern'

  const save = useMutation({
    mutationFn: () => {
      const input = inputFromForm(form)
      return rule === null ? createRule(input) : updateRule(rule.id, input)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['categorization-rules'] })
      onClose()
    },
  })

  const priority = Number(form.priority)
  const priorityValid = form.priority.trim() !== '' && Number.isInteger(priority)
  const hasCondition = [
    form.counterAccountNumber,
    form.variableSymbol,
    form.constantSymbol,
    form.textContains,
    form.transactionTypeContains,
  ].some((value) => value.trim() !== '')
  const canSave = priorityValid && (builtIn || (form.categoryId !== '' && hasCondition)) && !save.isPending

  const beforeMemberMatch =
    !builtIn && priorityValid && memberRulePriority !== undefined && priority < memberRulePriority

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>{rule === null ? 'Add rule' : builtIn ? 'Edit built-in rule' : 'Edit rule'}</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) save.mutate()
        }}
      >
        <label style={field}>
          Priority (lower runs first)
          <input
            type="number"
            step="1"
            value={form.priority}
            onChange={(e) => set({ priority: e.target.value })}
            autoFocus
          />
        </label>
        {beforeMemberMatch && (
          <p style={hint}>
            Runs before member matching (priority {memberRulePriority}) — a matching transaction never becomes a
            membership fee. Meant for a specific VS/KS that collides with a member number; a text rule here can
            steal real dues.
          </p>
        )}

        {builtIn && rule !== null ? (
          <p style={{ color: '#666' }}>
            {conditionsText(rule)} → {categoryLabelById(categories.data, rule.category_id)}. Built-in: only priority, on/off and note can
            change.
          </p>
        ) : (
          <>
            <label style={field}>
              Category
              <select value={form.categoryId} onChange={(e) => set({ categoryId: e.target.value })}>
                <option value="">— choose —</option>
                {withChildrenUnderParents(categories.data ?? []).map((c) => (
                  <option key={c.id} value={c.id}>
                    {categoryOptionLabel(c)}
                  </option>
                ))}
              </select>
            </label>

            <fieldset style={{ border: '1px solid #ddd', borderRadius: 6, padding: '0.75rem', marginBottom: '0.75rem' }}>
              <legend>Conditions (all filled ones must match, at least one besides direction)</legend>
              <label style={field}>
                Counter account number (exact)
                <input value={form.counterAccountNumber} onChange={(e) => set({ counterAccountNumber: e.target.value })} />
              </label>
              <label style={field}>
                Variable symbol (exact, leading zeroes ignored)
                <input value={form.variableSymbol} onChange={(e) => set({ variableSymbol: e.target.value })} />
              </label>
              <label style={field}>
                Constant symbol (exact)
                <input value={form.constantSymbol} onChange={(e) => set({ constantSymbol: e.target.value })} />
              </label>
              <label style={field}>
                Text contains (comment, user identification or message; case-insensitive)
                <input value={form.textContains} onChange={(e) => set({ textContains: e.target.value })} />
              </label>
              <label style={field}>
                Transaction type contains (case-insensitive)
                <input
                  value={form.transactionTypeContains}
                  onChange={(e) => set({ transactionTypeContains: e.target.value })}
                />
              </label>
              <label style={field}>
                Direction
                <select
                  value={form.direction}
                  onChange={(e) => set({ direction: e.target.value as RuleForm['direction'] })}
                >
                  <option value="">Any</option>
                  <option value="incoming">Incoming</option>
                  <option value="outgoing">Outgoing</option>
                </select>
              </label>
            </fieldset>
          </>
        )}

        <label style={field}>
          Note
          <input value={form.note} placeholder="e.g. Advokátní kancelář XY" onChange={(e) => set({ note: e.target.value })} />
        </label>
        <label style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginBottom: '0.75rem' }}>
          <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          Enabled
        </label>

        {save.error && <p style={{ color: '#b00' }}>{errMessage(save.error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canSave}>
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Overlay>
  )
}

const field: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '0.25rem',
  marginBottom: '0.75rem',
  maxWidth: 420,
}

const hint: CSSProperties = {
  color: '#8a5a00',
  background: '#fff6e0',
  borderRadius: 4,
  padding: '0.4rem 0.6rem',
  fontSize: '0.8125rem',
  maxWidth: 420,
}

const th: CSSProperties = {
  textAlign: 'left',
  borderBottom: '2px solid #ccc',
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}

const td: CSSProperties = {
  borderBottom: '1px solid #eee',
  padding: '0.35rem 0.6rem',
  whiteSpace: 'nowrap',
}
