import { type CSSProperties, useEffect, useRef, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { AssignDialog } from './AssignDialog'
import { categoryLabel, fetchCategories } from './api/categories'
import { fetchTransactions, type TransactionQuery } from './api/transactions'

const PAGE_SIZE = 100
const DEBOUNCE_MS = 500
const COLUMNS = ['Date', 'Amount', 'Dir', 'Category', 'Member', 'VS', 'Counterparty', 'Message', 'Admin note', ''] as const

interface Filters {
  from: string
  to: string
  assigned: '' | 'true' | 'false'
  direction: '' | 'incoming' | 'outgoing'
  category: string
  matchedBy: '' | 'variable_symbol' | 'manual' | 'amount_heuristic'
  memberNumber: string
  search: string
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
    search: '',
  }
}

const ASSIGNED_VALUES = ['true', 'false'] as const
const DIRECTION_VALUES = ['incoming', 'outgoing'] as const
const MATCHED_BY_VALUES = ['variable_symbol', 'manual', 'amount_heuristic'] as const

function pick<T extends string>(value: string | null, allowed: readonly T[]): T | '' {
  return value != null && (allowed as readonly string[]).includes(value) ? (value as T) : ''
}

// Filters/offset live entirely in the URL's query string (react-router
// useSearchParams), not React state — that's what makes the current view
// linkable/shareable as-is, and a page reload/back-button restore it for free.
// Param names mirror the API's own query params (see api/transactions.ts) so
// the URL reads the same as the request it drives.
function filtersFromParams(params: URLSearchParams, defaults: Filters): Filters {
  return {
    from: params.get('from') ?? defaults.from,
    to: params.get('to') ?? defaults.to,
    assigned: pick(params.get('assigned'), ASSIGNED_VALUES),
    direction: pick(params.get('direction'), DIRECTION_VALUES),
    category: params.get('category') ?? '',
    matchedBy: pick(params.get('matched_by'), MATCHED_BY_VALUES),
    memberNumber: params.get('member_number') ?? '',
    search: params.get('search') ?? '',
  }
}

function offsetFromParams(params: URLSearchParams): number {
  const raw = Number(params.get('offset'))
  return Number.isInteger(raw) && raw > 0 ? raw : 0
}

function paramsFromFilters(f: Filters, offset: number): URLSearchParams {
  const params = new URLSearchParams()
  if (f.from) params.set('from', f.from)
  if (f.to) params.set('to', f.to)
  if (f.assigned) params.set('assigned', f.assigned)
  if (f.direction) params.set('direction', f.direction)
  if (f.category) params.set('category', f.category)
  if (f.matchedBy) params.set('matched_by', f.matchedBy)
  if (f.memberNumber.trim()) params.set('member_number', f.memberNumber.trim())
  if (f.search.trim()) params.set('search', f.search.trim())
  if (offset > 0) params.set('offset', String(offset))
  return params
}

// useDebouncedCommit backs a "typing" filter field (search, member number):
// the input shows every keystroke immediately (draft), but onCommit — the
// thing that actually updates the URL/query — only fires DEBOUNCE_MS after
// typing stops. If the committed value changes from elsewhere (Reset button,
// browser back/forward, or this same commit landing) the draft resyncs and
// any in-flight timer is cancelled, so a stale keystroke can't overwrite a
// reset that happened while the timer was still pending.
function useDebouncedCommit(committed: string, onCommit: (value: string) => void, delayMs: number): [string, (value: string) => void] {
  const [draft, setDraft] = useState(committed)
  const timeoutRef = useRef<ReturnType<typeof setTimeout>>()

  useEffect(() => {
    setDraft(committed)
    clearTimeout(timeoutRef.current)
  }, [committed])

  useEffect(() => () => clearTimeout(timeoutRef.current), [])

  const handleChange = (value: string) => {
    setDraft(value)
    clearTimeout(timeoutRef.current)
    timeoutRef.current = setTimeout(() => onCommit(value), delayMs)
  }

  return [draft, handleChange]
}

// DateField listens to the native "change" event, not React's onChange
// (which fires on every keystroke while typing a date, including
// empty/partial values mid-edit) — change only fires once a value is
// complete. That's still not "done editing" on its own though: scrolling the
// mouse wheel over the month/day/year spinner segment fires a genuine change
// on every tick, since each tick already lands on a complete, different
// valid date — the browser has no separate signal for "still fiddling" vs
// "settled". So the commit itself is debounced the same DEBOUNCE_MS as
// search/member#, just re-armed by change instead of by every keystroke —
// rapid scroll ticks coalesce into one commit after they stop, a single
// calendar-day pick or a fully-typed date behaves the same way with one tick
// to debounce.
// Uncontrolled (defaultValue) on purpose, mounted once — no `key`-forced
// remount on commit: recreating the <input> mid-interaction (e.g. while its
// native calendar popup is open) previously caused a spurious extra fire.
// The listener is attached once (onCommit read from a ref, not a dependency)
// so it doesn't get torn down/re-added on every parent render either. An
// external value change (Reset, browser back/forward) is applied
// imperatively to the DOM node instead, and only when it actually differs,
// so it never stomps on an interaction already in progress.
function DateField({
  label,
  value,
  min,
  max,
  onCommit,
}: {
  label: string
  value: string
  min?: string
  max?: string
  onCommit: (value: string) => void
}) {
  const ref = useRef<HTMLInputElement>(null)
  const onCommitRef = useRef(onCommit)
  onCommitRef.current = onCommit
  const timeoutRef = useRef<ReturnType<typeof setTimeout>>()

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const handleChange = () => {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = setTimeout(() => onCommitRef.current(el.value), DEBOUNCE_MS)
    }
    el.addEventListener('change', handleChange)
    return () => {
      el.removeEventListener('change', handleChange)
      clearTimeout(timeoutRef.current)
    }
  }, [])

  useEffect(() => {
    const el = ref.current
    if (el && el.value !== value) {
      el.value = value
    }
    clearTimeout(timeoutRef.current)
  }, [value])

  return (
    <label>
      {label}{' '}
      <input ref={ref} type="date" defaultValue={value} min={min} max={max} />
    </label>
  )
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
    search: f.search.trim() || undefined,
  }
}

export function TransactionsTable() {
  const [defaults] = useState(defaultFilters)
  const [searchParams, setSearchParams] = useSearchParams()
  const [editingId, setEditingId] = useState<number | null>(null)

  const filters = filtersFromParams(searchParams, defaults)
  const offset = offsetFromParams(searchParams)

  const modified = JSON.stringify(filters) !== JSON.stringify(defaults)

  // Any filter change resets to the first page. replace: true so typing in
  // the member-number box doesn't spam browser history with one entry per
  // keystroke — the URL still reflects current state either way. Reads prev
  // (not the closured `filters`) so two fields committing close together
  // (e.g. a debounced search commit landing right after a date field's)
  // can't clobber each other with a stale snapshot.
  const update = (patch: Partial<Filters>) => {
    setSearchParams((prev) => paramsFromFilters({ ...filtersFromParams(prev, defaults), ...patch }, 0), { replace: true })
  }
  const reset = () => {
    setSearchParams(new URLSearchParams(), { replace: true })
  }

  const [searchDraft, handleSearchChange] = useDebouncedCommit(filters.search, (v) => update({ search: v }), DEBOUNCE_MS)
  const [memberNumberDraft, handleMemberNumberChange] = useDebouncedCommit(
    filters.memberNumber,
    (v) => update({ memberNumber: v }),
    DEBOUNCE_MS,
  )

  const query = toQuery(filters, offset)

  const { data, isPending, isError, error, isPlaceholderData } = useQuery({
    queryKey: ['transactions', query],
    queryFn: () => fetchTransactions(query),
    placeholderData: keepPreviousData,
  })

  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })

  const filterRowEl = (
    <div style={filterRow}>
      <DateField label="From" value={filters.from} max={filters.to || undefined} onCommit={(v) => update({ from: v })} />
      <DateField label="To" value={filters.to} min={filters.from || undefined} onCommit={(v) => update({ to: v })} />

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
        onChange={(v) => update({ category: v })}
        options={(categories.data ?? []).map((c): [string, string] => [c.name, categoryLabel(c.name)])}
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
          value={memberNumberDraft}
          onChange={(e) => handleMemberNumberChange(e.target.value)}
          style={{ width: '5rem' }}
        />
      </label>
      <label>
        Search{' '}
        <input
          type="text"
          placeholder="Counterparty or message"
          value={searchDraft}
          onChange={(e) => handleSearchChange(e.target.value)}
          style={{ width: '14rem' }}
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
        onClick={() => setSearchParams(paramsFromFilters(filters, Math.max(0, offset - PAGE_SIZE)), { replace: true })}
        disabled={offset === 0 || isPlaceholderData}
      >
        ← Prev
      </button>
      <span>
        {rangeFrom}–{rangeTo} of {total}
      </span>
      <button
        onClick={() => setSearchParams(paramsFromFilters(filters, offset + PAGE_SIZE), { replace: true })}
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
              {COLUMNS.map((h, i) => (
                <th key={i} style={th}>
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
                <td style={td}>{categoryLabel(t.category)}</td>
                <td style={td}>{t.member_number ?? '—'}</td>
                <td style={td}>{t.variable_symbol ?? '—'}</td>
                <td style={td}>{t.counter_account_name ?? t.counter_account_number ?? '—'}</td>
                <td style={td}>{t.message_for_recipient ?? t.comment ?? '—'}</td>
                <td style={td}>{t.admin_comment ?? '—'}</td>
                <td style={td}>
                  <button onClick={() => setEditingId(t.id)}>Edit</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {needsPager && pager}

      {editingId != null && (
        <AssignDialog transactionId={editingId} onClose={() => setEditingId(null)} />
      )}
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
