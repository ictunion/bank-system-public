import type { CSSProperties } from 'react'
import { useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { type EventLogItem, fetchEventLogs } from './api/eventLogs'

const PAGE_SIZE = 100
const COLUMNS = ['Type', 'Started', 'Finished', 'Status', 'Detail', 'Fetched', 'Processed', 'Error'] as const

function fmtDateTime(iso: string | null): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

export function EventLogsPage() {
  const [offset, setOffset] = useState(0)

  const { data, isPending, isError, error, isPlaceholderData } = useQuery({
    queryKey: ['event-logs', offset],
    queryFn: () => fetchEventLogs(PAGE_SIZE, offset),
    placeholderData: keepPreviousData,
  })

  if (isPending) {
    return (
      <section>
        <h2 style={{ marginTop: 0 }}>Event logs</h2>
        <p>Loading event logs…</p>
      </section>
    )
  }
  if (isError) {
    return (
      <section>
        <h2 style={{ marginTop: 0 }}>Event logs</h2>
        <p>Failed to load event logs: {String(error)}</p>
      </section>
    )
  }

  const { total, events } = data
  const rangeFrom = total === 0 ? 0 : offset + 1
  const rangeTo = offset + events.length
  const needsPager = total > PAGE_SIZE

  const pager = needsPager ? (
    <div style={pagerStyle}>
      <button onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))} disabled={offset === 0 || isPlaceholderData}>
        ← Prev
      </button>
      <span>
        {rangeFrom}–{rangeTo} of {total}
      </span>
      <button onClick={() => setOffset((o) => o + PAGE_SIZE)} disabled={rangeTo >= total || isPlaceholderData}>
        Next →
      </button>
    </div>
  ) : (
    <p>
      {total} event{total === 1 ? '' : 's'}
    </p>
  )

  return (
    <section style={{ opacity: isPlaceholderData ? 0.6 : 1 }}>
      <h2 style={{ marginTop: 0 }}>Event logs</h2>
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
            {events.map((e) => (
              <EventLogRow key={`${e.event_type}-${e.id}`} event={e} />
            ))}
          </tbody>
        </table>
      </div>
      {needsPager && pager}
    </section>
  )
}

function EventLogRow({ event }: { event: EventLogItem }) {
  return (
    <tr>
      <td style={td}>{event.event_type === 'fio_sync' ? 'Bank sync' : 'Orca sync'}</td>
      <td style={td}>{fmtDateTime(event.started_at)}</td>
      <td style={td}>{fmtDateTime(event.finished_at)}</td>
      <td style={{ ...td, color: statusColor(event.status) }}>{event.status}</td>
      <td style={td}>{event.detail ?? '—'}</td>
      <td style={{ ...td, textAlign: 'right' }}>{event.fetched ?? '—'}</td>
      <td style={{ ...td, textAlign: 'right' }}>{event.processed ?? '—'}</td>
      <td style={{ ...td, whiteSpace: 'normal', color: '#b00' }}>{event.error_message ?? ''}</td>
    </tr>
  )
}

function statusColor(status: string): string | undefined {
  if (status === 'failed') return '#b00'
  if (status === 'running') return '#a60'
  return undefined
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
