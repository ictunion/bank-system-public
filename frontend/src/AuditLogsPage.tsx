import type { CSSProperties } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { AUDIT_CATEGORIES, type AuditCategory, type AuditLogItem, type AuditLogQuery, fetchAuditLogs } from './api/auditLogs'
import { useDebouncedCommit } from './useDebouncedCommit'

const PAGE_SIZE = 100
const DEBOUNCE_MS = 500
const COLUMNS = ['When', 'User', 'Category', 'Action', 'Path', 'Status'] as const

const CATEGORY_LABELS: Record<AuditCategory, string> = {
  bank_account: 'Bank account',
  processing: 'Processing',
  transaction: 'Transaction',
  category: 'Category',
  waiver: 'Waiver',
  other: 'Other',
}

interface Filters {
  category: AuditCategory | ''
  actorSub: string
  from: string
  to: string
}

// Filters/offset live in the URL's query string, same as TransactionsTable,
// so a filtered view is linkable and survives reload/back. Param names
// mirror the API's own (see api/auditLogs.ts).
function filtersFromParams(params: URLSearchParams): Filters {
  const category = params.get('category')
  return {
    category: category != null && (AUDIT_CATEGORIES as readonly string[]).includes(category) ? (category as AuditCategory) : '',
    actorSub: params.get('actor_sub') ?? '',
    from: params.get('from') ?? '',
    to: params.get('to') ?? '',
  }
}

function offsetFromParams(params: URLSearchParams): number {
  const raw = Number(params.get('offset'))
  return Number.isInteger(raw) && raw > 0 ? raw : 0
}

function paramsFromFilters(f: Filters, offset: number): URLSearchParams {
  const params = new URLSearchParams()
  if (f.category) params.set('category', f.category)
  if (f.actorSub) params.set('actor_sub', f.actorSub)
  if (f.from) params.set('from', f.from)
  if (f.to) params.set('to', f.to)
  if (offset > 0) params.set('offset', String(offset))
  return params
}

function toQuery(f: Filters, offset: number): AuditLogQuery {
  return {
    limit: PAGE_SIZE,
    offset,
    category: f.category || undefined,
    actorSub: f.actorSub || undefined,
    from: f.from || undefined,
    to: f.to || undefined,
  }
}

// "create_bank_account" -> "Create bank account"
function actionLabel(action: string): string {
  const words = action.replace(/_/g, ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}

function actorLabel(log: Pick<AuditLogItem, 'actor_email' | 'actor_sub'>): string {
  return log.actor_email ?? log.actor_sub
}

export function AuditLogsPage() {
  const [searchParams, setSearchParams] = useSearchParams()

  const filters = filtersFromParams(searchParams)
  const offset = offsetFromParams(searchParams)
  const modified = filters.category !== '' || filters.actorSub !== '' || filters.from !== '' || filters.to !== ''

  // Any filter change resets to the first page; replace: true keeps filter
  // tweaks out of browser history. Reads prev so two debounced commits
  // landing close together can't clobber each other.
  const update = (patch: Partial<Filters>) => {
    setSearchParams((prev) => paramsFromFilters({ ...filtersFromParams(prev), ...patch }, 0), { replace: true })
  }
  const reset = () => {
    setSearchParams(new URLSearchParams(), { replace: true })
  }

  const [fromDraft, handleFromChange] = useDebouncedCommit(filters.from, (v) => update({ from: v }), DEBOUNCE_MS)
  const [toDraft, handleToChange] = useDebouncedCommit(filters.to, (v) => update({ to: v }), DEBOUNCE_MS)

  const query = toQuery(filters, offset)

  const { data, isPending, isError, error, isPlaceholderData } = useQuery({
    queryKey: ['audit-logs', query],
    queryFn: () => fetchAuditLogs(query),
    placeholderData: keepPreviousData,
  })

  // The URL only carries the actor's sub; show their email when any loaded
  // row has it (every row does while the actor filter is active).
  const filteredActor = filters.actorSub ? data?.logs.find((l) => l.actor_sub === filters.actorSub) : undefined

  const filterRowEl = (
    <div style={filterRow}>
      <label>
        Category{' '}
        <select value={filters.category} onChange={(e) => update({ category: e.target.value as Filters['category'] })}>
          <option value="">Any</option>
          {AUDIT_CATEGORIES.map((c) => (
            <option key={c} value={c}>
              {CATEGORY_LABELS[c]}
            </option>
          ))}
        </select>
      </label>
      <label>
        From{' '}
        <input type="date" value={fromDraft} max={filters.to || undefined} onChange={(e) => handleFromChange(e.target.value)} />
      </label>
      <label>
        To{' '}
        <input type="date" value={toDraft} min={filters.from || undefined} onChange={(e) => handleToChange(e.target.value)} />
      </label>
      {filters.actorSub ? (
        <span style={chip}>
          User: {filteredActor ? actorLabel(filteredActor) : filters.actorSub}{' '}
          <button onClick={() => update({ actorSub: '' })} title="Clear user filter" style={chipButton}>
            ✕
          </button>
        </span>
      ) : (
        <span style={{ color: '#666' }}>Click a user to filter by them</span>
      )}

      {modified && <button onClick={reset}>Reset filters</button>}
    </div>
  )

  if (isPending) {
    return (
      <section>
        <h2 style={{ marginTop: 0 }}>Audit log</h2>
        {filterRowEl}
        <p>Loading audit log…</p>
      </section>
    )
  }
  if (isError) {
    return (
      <section>
        <h2 style={{ marginTop: 0 }}>Audit log</h2>
        {filterRowEl}
        <p>Failed to load audit log: {String(error)}</p>
      </section>
    )
  }

  const { total, logs } = data
  const rangeFrom = total === 0 ? 0 : offset + 1
  const rangeTo = offset + logs.length
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
      {total} entr{total === 1 ? 'y' : 'ies'}
    </p>
  )

  return (
    <section style={{ opacity: isPlaceholderData ? 0.6 : 1 }}>
      <h2 style={{ marginTop: 0 }}>Audit log</h2>
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
            {logs.map((log) => (
              <AuditLogRow key={log.id} log={log} onActorClick={() => update({ actorSub: log.actor_sub })} />
            ))}
          </tbody>
        </table>
      </div>
      {needsPager && pager}
    </section>
  )
}

function AuditLogRow({ log, onActorClick }: { log: AuditLogItem; onActorClick: () => void }) {
  const succeeded = log.status_code >= 200 && log.status_code < 300
  return (
    <tr>
      <td style={td}>{new Date(log.created_at).toLocaleString()}</td>
      <td style={td}>
        <button onClick={onActorClick} title={`Filter by this user (${log.actor_sub})`} style={linkButton}>
          {actorLabel(log)}
        </button>
      </td>
      <td style={td}>{CATEGORY_LABELS[log.category] ?? log.category}</td>
      <td style={td}>{actionLabel(log.action)}</td>
      <td style={{ ...td, fontFamily: 'monospace' }} title={log.route}>
        {log.method} {log.path}
      </td>
      <td style={{ ...td, color: succeeded ? undefined : '#b00' }} title={succeeded ? undefined : 'Rejected — no change was made'}>
        {log.status_code}
      </td>
    </tr>
  )
}

const filterRow: CSSProperties = {
  display: 'flex',
  gap: '1rem',
  alignItems: 'center',
  flexWrap: 'wrap',
  marginBottom: '0.75rem',
}

const chip: CSSProperties = {
  background: '#eef',
  borderRadius: '4px',
  padding: '0.15rem 0.4rem',
}

const chipButton: CSSProperties = {
  border: 'none',
  background: 'none',
  cursor: 'pointer',
  padding: 0,
}

const linkButton: CSSProperties = {
  border: 'none',
  background: 'none',
  padding: 0,
  color: '#0645ad',
  cursor: 'pointer',
  textDecoration: 'underline',
  font: 'inherit',
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
