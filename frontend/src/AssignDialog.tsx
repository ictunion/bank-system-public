import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { categoryLabel, categoryOptionLabel, fetchCategories, withChildrenUnderParents } from './api/categories'
import { errMessage } from './api/client'
import { createRule } from './api/rules'
import { Overlay } from './Overlay'
import { useHasRole } from './roles'
import {
  assignTransaction,
  type Category,
  fetchTransaction,
  type MonthRef,
  type TransactionDetail,
  unassignTransaction,
} from './api/transactions'

const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

// membership_fee is one of the four mandatory categories — this specific
// name is hardcoded on the backend too (internal/handler/transactions.go),
// not just here, so it's safe to check for literally.
const MEMBERSHIP_FEE = 'membership_fee'

// Priority of a rule created from this dialog: after member matching (300),
// before the seeded "mzda" salary rule (500).
const RULE_PRIORITY = 400

function monthOf(isoDate: string): MonthRef {
  const [y, m] = isoDate.split('-')
  return { year: Number(y), month: Number(m) }
}

// Default coverage target: the month before the transaction's own — dues
// are paid a month in arrears, same convention the backend uses for 
// default single-month coverage.
function prevMonthOf(isoDate: string): MonthRef {
  const { year, month } = monthOf(isoDate)
  return month === 1 ? { year: year - 1, month: 12 } : { year, month: month - 1 }
}

export function AssignDialog({
  transactionId,
  onClose,
}: {
  transactionId: number
  onClose: () => void
}) {
  const detail = useQuery({
    queryKey: ['transaction', transactionId],
    queryFn: () => fetchTransaction(transactionId),
  })

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Assign transaction</h2>
      {detail.isPending ? (
        <p>Loading…</p>
      ) : detail.isError ? (
        <p>Failed to load: {String(detail.error)}</p>
      ) : (
        <AssignForm key={detail.data.id} detail={detail.data} onClose={onClose} />
      )}
    </Overlay>
  )
}

function AssignForm({ detail, onClose }: { detail: TransactionDetail; onClose: () => void }) {
  const qc = useQueryClient()

  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })

  const [memberNumber, setMemberNumber] = useState(detail.member_number?.toString() ?? '')
  const [category, setCategory] = useState<Category>(detail.category)
  const [months, setMonths] = useState<MonthRef[]>(
    detail.covered_months.length ? detail.covered_months : [prevMonthOf(detail.transaction_date)],
  )
  const [adminComment, setAdminComment] = useState(detail.admin_comment ?? '')
  const [alsoCreateRule, setAlsoCreateRule] = useState(false)

  const done = () => {
    qc.invalidateQueries({ queryKey: ['transactions'] })
    qc.invalidateQueries({ queryKey: ['transaction', detail.id] })
    onClose()
  }

  const hasMember = memberNumber.trim() !== ''
  const coverable = category === MEMBERSHIP_FEE && hasMember

  // Shortcut for a recurring counterparty (e.g. the lawyer): also create a
  // pattern rule so future payments to/from this account get this category
  // automatically. Not offered for membership_fee — a pattern rule can't
  // match a member, only the built-in member rule can.
  const canManageRules = useHasRole('manage-rules')
  const counterAccountNumber = detail.counter_account_number
  const ruleOffered = canManageRules && counterAccountNumber !== null && category !== MEMBERSHIP_FEE
  const createsRule = ruleOffered && alsoCreateRule

  const assign = useMutation({
    mutationFn: async () => {
      await assignTransaction(detail.id, {
        member_number: hasMember ? Number(memberNumber) : undefined,
        category,
        covers: coverable ? months : undefined,
        admin_comment: adminComment,
      })
      if (!createsRule || counterAccountNumber === null) return
      try {
        await createRule({
          priority: RULE_PRIORITY,
          category,
          enabled: true,
          counter_account_number: counterAccountNumber,
          variable_symbol: null,
          constant_symbol: null,
          text_contains: null,
          transaction_type_contains: null,
          direction: detail.direction,
          note: detail.counter_account_name,
        })
      } catch (err) {
        // The assignment itself already went through.
        qc.invalidateQueries({ queryKey: ['transactions'] })
        throw new Error(`Transaction saved, but creating the rule failed: ${errMessage(err)}`)
      }
      qc.invalidateQueries({ queryKey: ['categorization-rules'] })
    },
    onSuccess: done,
  })

  const unassign = useMutation({
    mutationFn: () => unassignTransaction(detail.id),
    onSuccess: done,
  })

  // Member # is optional — a lot of transactions (other_income/other_expense,
  // even some salary rows) aren't tied to any member. Left blank, this is a
  // category-only edit. Filled in, it must be a real positive number.
  const memberValid = !hasMember || (Number.isInteger(Number(memberNumber)) && Number(memberNumber) > 0)
  const monthsValid =
    !coverable ||
    (months.length > 0 && months.every((m) => m.month >= 1 && m.month <= 12 && m.year >= 2000))
  const canSave = memberValid && monthsValid && !assign.isPending && !unassign.isPending

  const errorMessage = assign.error ? errMessage(assign.error) : unassign.error ? errMessage(unassign.error) : null

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (canSave) assign.mutate()
      }}
    >
      <div style={summary}>
        <span style={{ color: '#666' }}>Date</span>
        <span>{detail.transaction_date}</span>
        <span style={{ color: '#666' }}>Amount</span>
        <span>
          {detail.amount} {detail.currency} ({detail.direction})
        </span>
        <span style={{ color: '#666' }}>Counterparty</span>
        <span>{detail.counter_account_name ?? detail.counter_account_number ?? '—'}</span>
        <span style={{ color: '#666' }}>VS</span>
        <span>{detail.variable_symbol ?? '—'}</span>
        <span style={{ color: '#666' }}>Message</span>
        <span>{detail.message_for_recipient ?? detail.comment ?? '—'}</span>
        <span style={{ color: '#666' }}>Categorized</span>
        <span>{detail.category_set_manually ? 'Manually — rules leave it alone' : 'Automatically'}</span>
      </div>

      <label style={field}>
        Member # (optional)
        <input
          type="number"
          min="1"
          step="1"
          value={memberNumber}
          onChange={(e) => setMemberNumber(e.target.value)}
          autoFocus
        />
      </label>

      <label style={field}>
        Admin note (optional)
        <input
          type="text"
          value={adminComment}
          placeholder="e.g. missing VS"
          onChange={(e) => setAdminComment(e.target.value)}
        />
      </label>

      <label style={field}>
        Category
        <select value={category} onChange={(e) => setCategory(e.target.value)}>
          {withChildrenUnderParents(categories.data ?? []).map((c) => (
            <option key={c.name} value={c.name}>
              {categoryOptionLabel(c)}
            </option>
          ))}
        </select>
      </label>

      {coverable && (
        <fieldset style={{ border: '1px solid #ddd', borderRadius: 6, padding: '0.75rem' }}>
          <legend>Covers months</legend>
          {months.map((m, i) => (
            <div key={i} style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.4rem' }}>
              <select
                value={m.month}
                onChange={(e) => setMonths(replace(months, i, { ...m, month: Number(e.target.value) }))}
              >
                {MONTH_NAMES.map((name, idx) => (
                  <option key={idx} value={idx + 1}>
                    {name}
                  </option>
                ))}
              </select>
              <input
                type="number"
                min="2000"
                step="1"
                value={m.year}
                style={{ width: '6rem' }}
                onChange={(e) => setMonths(replace(months, i, { ...m, year: Number(e.target.value) }))}
              />
              <button
                type="button"
                onClick={() => setMonths(months.filter((_, j) => j !== i))}
                disabled={months.length === 1}
              >
                Remove
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={() => setMonths([...months, months[months.length - 1] ?? prevMonthOf(detail.transaction_date)])}
          >
            Add month
          </button>
        </fieldset>
      )}

      {ruleOffered && (
        <label style={{ display: 'flex', gap: '0.4rem', alignItems: 'flex-start', marginBottom: '0.75rem' }}>
          <input type="checkbox" checked={alsoCreateRule} onChange={(e) => setAlsoCreateRule(e.target.checked)} />
          <span>
            Always categorize {detail.direction} payments {detail.direction === 'incoming' ? 'from' : 'to'} account{' '}
            {counterAccountNumber} as {categoryLabel(category)}
            <span style={{ display: 'block', color: '#666', fontSize: '0.8125rem' }}>
              Creates a rule (priority {RULE_PRIORITY}) for new transactions. Existing ones aren't changed — use
              "Re-apply" on the Rules page for that.
            </span>
          </span>
        </label>
      )}

      {errorMessage && <p style={{ color: '#b00' }}>{errorMessage}</p>}

      <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem', alignItems: 'center' }}>
        <button type="submit" disabled={!canSave}>
          {assign.isPending ? 'Saving…' : 'Save'}
        </button>
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        {(detail.member_number != null || detail.category_set_manually) && (
          <button
            type="button"
            style={{ marginLeft: 'auto', color: '#b00' }}
            disabled={assign.isPending || unassign.isPending}
            title="Clears the member match and coverage, resets the category to other income/expense and lets rules change it again"
            onClick={() => {
              if (
                window.confirm(
                  'Reset this transaction to automatic? The member match and coverage are removed and the category goes back to other income / other expense. Rules are not re-run — re-apply a rule to categorize it again.',
                )
              ) {
                unassign.mutate()
              }
            }}
          >
            {unassign.isPending ? 'Resetting…' : 'Reset to automatic'}
          </button>
        )}
      </div>
    </form>
  )
}

function replace<T>(array: T[], i: number, value: T): T[] {
  const next = array.slice()
  next[i] = value
  return next
}

const summary: CSSProperties = {
  display: 'grid',
  gridTemplateColumns: 'max-content 1fr',
  columnGap: '1rem',
  rowGap: '0.25rem',
  margin: '0 0 1rem',
  fontSize: '0.875rem',
}

const field: CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '0.25rem',
  marginBottom: '0.75rem',
  maxWidth: 200,
}
