import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, errMessage } from './api/client'
import { Overlay } from './Overlay'
import {
  assignTransaction,
  type Category,
  type CoverageConflict,
  fetchTransaction,
  type MonthRef,
  type TransactionDetail,
  unassignTransaction,
} from './api/transactions'

const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

const CATEGORIES: [Category, string][] = [
  ['membership_fee', 'Membership fee'],
  ['salary', 'Salary'],
  ['other_income', 'Other income'],
  ['other_expense', 'Other expense'],
]

function monthOf(isoDate: string): MonthRef {
  const [y, m] = isoDate.split('-')
  return { year: Number(y), month: Number(m) }
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

  const [memberNumber, setMemberNumber] = useState(detail.member_number?.toString() ?? '')
  const [category, setCategory] = useState<Category>(detail.category)
  const [months, setMonths] = useState<MonthRef[]>(
    detail.covered_months.length ? detail.covered_months : [monthOf(detail.transaction_date)],
  )

  const done = () => {
    qc.invalidateQueries({ queryKey: ['transactions'] })
    qc.invalidateQueries({ queryKey: ['transaction', detail.id] })
    onClose()
  }

  const assign = useMutation({
    mutationFn: () =>
      assignTransaction(detail.id, {
        member_number: Number(memberNumber),
        category,
        covers: category === 'membership_fee' ? months : undefined,
      }),
    onSuccess: done,
  })

  const unassign = useMutation({
    mutationFn: () => unassignTransaction(detail.id),
    onSuccess: done,
  })

  const memberValid = Number.isInteger(Number(memberNumber)) && Number(memberNumber) > 0
  const monthsValid =
    category !== 'membership_fee' ||
    (months.length > 0 &&
      months.every((m) => m.month >= 1 && m.month <= 12 && m.year >= 2000))
  const canSave = memberValid && monthsValid && !assign.isPending && !unassign.isPending

  const conflicts =
    assign.error instanceof ApiError && assign.error.status === 409
      ? (assign.error.body as CoverageConflict).conflicts
      : []
  const otherError =
    assign.error && !(assign.error instanceof ApiError && assign.error.status === 409)
      ? errMessage(assign.error)
      : unassign.error
        ? errMessage(unassign.error)
        : null

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
      </div>

      <label style={field}>
        Member #
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
        Category
        <select value={category} onChange={(e) => setCategory(e.target.value as Category)}>
          {CATEGORIES.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
      </label>

      {category === 'membership_fee' && (
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
            onClick={() => setMonths([...months, months[months.length - 1] ?? monthOf(detail.transaction_date)])}
          >
            Add month
          </button>
        </fieldset>
      )}

      {conflicts.length > 0 && (
        <p style={{ color: '#b00' }}>
          Already covered by another transaction:{' '}
          {conflicts.map((c) => `${MONTH_NAMES[c.month - 1]} ${c.year}`).join(', ')}. Clear it there
          first.
        </p>
      )}
      {otherError && <p style={{ color: '#b00' }}>{otherError}</p>}

      <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem', alignItems: 'center' }}>
        <button type="submit" disabled={!canSave}>
          {assign.isPending ? 'Saving…' : 'Save'}
        </button>
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        {detail.member_number != null && (
          <button
            type="button"
            style={{ marginLeft: 'auto', color: '#b00' }}
            disabled={assign.isPending || unassign.isPending}
            onClick={() => {
              if (window.confirm('Remove member match and coverage for this transaction?')) {
                unassign.mutate()
              }
            }}
          >
            {unassign.isPending ? 'Unassigning…' : 'Unassign'}
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
