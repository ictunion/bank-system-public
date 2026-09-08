import { type CSSProperties, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { fetchTransactions, type TransactionQuery } from './api/transactions'

const PAGE_SIZE = 100
const COLUMNS = ['Date', 'Amount', 'Dir', 'Category', 'Member', 'VS', 'Counterparty', 'Message'] as const

interface Filters {
  from: string
  to: string
  assigned: '' | 'true' | 'false'
  direction: '' | 'incoming' | 'outgoing'
  category: '' | 'membership_fee' | 'salary' | 'other_income' | 'other_expense'
  matchedBy: '' | 'variable_symbol' | 'manual' | 'amount_heuristic'
  memberNumber: string
}

function ymd(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// Defaults: date filter = last 30 days ending today, everything else unset.
function defaultFilters(): Filters {
  const to = new Date()
  const from = new Date()
  from.setDate(from.getDate() - 30)
  return {
    from: ymd(from),
    to: ymd(to),
    assigned: '',
    direction: '',
    category: '',
    matchedBy: '',
    memberNumber: '',
  }
}

function toQuery(f: Filters, offset: number): TransactionQuery {
  const mn = Number(f.memberNumber)
  return {
    limit: PAGE_SIZE,
    offset,
    from: f.from || undefined,
    to: f.to || undefined,
    assigned: f.assigned ? f.assigned === 'true' : undefined,
    direction: f.direction || undefined,
    category: f.category || undefined,
    matchedBy: f.matchedBy || undefined,
    memberNumber: f.memberNumber.trim() && Number.isInteger(mn) && mn > 0 ? mn : undefined,
  }
}

export function TransactionsTable() {
  const [defaults] = useState(defaultFilters)
  const [filters, setFilters] = useState<Filters>(defaults)
  const [offset, setOffset] = useState(0)

  const modified = JSON.stringify(filters) !== JSON.stringify(defaults)

  // Any filter change resets to the first page.
  const update = (patch: Partial<Filters>) => {
    setFilters((f) => ({ ...f, ...patch }))
    setOffset(0)
  }
  const reset = () => {
    setFilters(defaults)
    setOffset(0)
  }

  const query = toQuery(filters, offset)

  const { data, isPending, isError, error, isPlaceholderData } = useQuery({
    queryKey: ['transactions', query],
    queryFn: () => fetchTransactions(query),
    placeholderData: keepPreviousData,
  })

  const filterRowEl = (
    <div style={filterRow}>
      <label>
        From{' '}
        <input
          type="date"
          value={filters.from}
          max={filters.to || undefined}
          onChange={(e) => update({ from: e.target.value })}
        />
      </label>
      <label>
        To{' '}
        <input
          type="date"
          value={filters.to}
          min={filters.from || undefined}
          onChange={(e) => update({ to: e.target.value })}
        />
      </label>

      <Select
        label="Assigned"
        value={filters.assigned}
        onChange={(v) => update({ assigned: v as Filters['assigned'] })}
        options={[
          ['true', 'Assigned'],
          ['false', 'Unassigned'],
        ]}
      />
      <Select
        label="Direction"
        value={filters.direction}
        onChange={(v) => update({ direction: v as Filters['direction'] })}
        options={[
          ['incoming', 'Incoming'],
          ['outgoing', 'Outgoing'],
        ]}
      />
      <Select
        label="Category"
        value={filters.category}
        onChange={(v) => update({ category: v as Filters['category'] })}
        options={[
          ['membership_fee', 'Membership fee'],
          ['salary', 'Salary'],
          ['other_income', 'Other income'],
          ['other_expense', 'Other expense'],
        ]}
      />
      <Select
        label="Matched by"
        value={filters.matchedBy}
        onChange={(v) => update({ matchedBy: v as Filters['matchedBy'] })}
        options={[
          ['variable_symbol', 'Variable symbol'],
          ['manual', 'Manual'],
          ['amount_heuristic', 'Amount heuristic'],
        ]}
      />
      <label>
        Member #{' '}
        <input
          type="number"
          min="1"
          step="1"
          value={filters.memberNumber}
          onChange={(e) => update({ memberNumber: e.target.value })}
          style={{ width: '5rem' }}
        />
      </label>

      {modified && <button onClick={reset}>Reset filters</button>}
    </div>
  )

  if (isPending) {
    return (
      <section>
        {filterRowEl}
        <p>Loading transactions…</p>
      </section>
    )
  }
  if (isError) {
    return (
      <section>
        {filterRowEl}
        <p>Failed to load transactions: {String(error)}</p>
      </section>
    )
  }

  const { total, transactions } = data
  const rangeFrom = total === 0 ? 0 : offset + 1
  const rangeTo = offset + transactions.length
  const needsPager = total > PAGE_SIZE

  const pager = needsPager ? (
    <div style={pagerStyle}>
      <button
        onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
        disabled={offset === 0 || isPlaceholderData}
      >
        ← Prev
      </button>
      <span>
        {rangeFrom}–{rangeTo} of {total}
      </span>
      <button
        onClick={() => setOffset((o) => o + PAGE_SIZE)}
        disabled={rangeTo >= total || isPlaceholderData}
      >
        Next →
      </button>
    </div>
  ) : (
    <p>
      {total} transaction{total === 1 ? '' : 's'}
    </p>
  )

  return (
    <section style={{ opacity: isPlaceholderData ? 0.6 : 1 }}>
      {filterRowEl}
      {pager}
      <div style={{ overflowX: 'auto' }}>
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
          <thead>
            <tr>
              {COLUMNS.map((h) => (
                <th key={h} style={th}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {transactions.map((t) => (
              <tr key={t.id}>
                <td style={td}>{t.transaction_date}</td>
                <td style={{ ...td, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>
                  {t.amount} {t.currency}
                </td>
                <td style={td}>{t.direction === 'incoming' ? 'in' : 'out'}</td>
                <td style={td}>{t.category}</td>
                <td style={td}>{t.member_number ?? '—'}</td>
                <td style={td}>{t.variable_symbol ?? '—'}</td>
                <td style={td}>{t.counter_account_name ?? t.counter_account_number ?? '—'}</td>
                <td style={td}>{t.message_for_recipient ?? t.comment ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {needsPager && pager}
    </section>
  )
}

function Select({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: [string, string][]
}) {
  return (
    <label>
      {label}{' '}
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Any</option>
        {options.map(([v, l]) => (
          <option key={v} value={v}>
            {l}
          </option>
        ))}
      </select>
    </label>
  )
}

const filterRow: CSSProperties = {
  display: 'flex',
  gap: '1rem',
  alignItems: 'center',
  flexWrap: 'wrap',
  marginBottom: '0.75rem',
}

const pagerStyle: CSSProperties = {
  display: 'flex',
  gap: '1rem',
  alignItems: 'center',
  margin: '0.75rem 0',
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
