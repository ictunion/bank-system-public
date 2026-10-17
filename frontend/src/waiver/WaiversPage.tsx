import { type CSSProperties, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { errMessage } from '../api/client'
import { fetchWaivers, type Waiver } from '../api/payments'
import { AddWaiverDialog } from './AddWaiverDialog'
import { formatMemberNumber, shortMonthLabel } from './format'
import { WaiverDetailDialog } from './WaiverDetailDialog'

const COLUMNS = ['Member Number', 'Waived months'] as const

// Rows per page — one row is one member, not one waiver.
const PAGE_SIZE = 50

// 1-based page number from ?page=N; anything missing or invalid is page 1.
function pageFromParams(params: URLSearchParams): number {
  const raw = Number(params.get('page'))
  return Number.isInteger(raw) && raw > 1 ? raw : 1
}

function waiverKey(w: Waiver): string {
  return `${w.member_number}-${w.year}-${w.month}`
}

interface MemberWaivers {
  member_number: number
  waivers: Waiver[]
}

// One entry per member, in the order each member first appears in the API's
// newest-first list; a member's own months are sorted chronologically.
function groupByMember(waivers: Waiver[]): MemberWaivers[] {
  const groups = new Map<number, Waiver[]>()
  for (const w of waivers) {
    const existing = groups.get(w.member_number)
    if (existing) existing.push(w)
    else groups.set(w.member_number, [w])
  }
  return Array.from(groups, ([member_number, memberWaivers]) => ({
    member_number,
    waivers: memberWaivers.sort((a, b) => a.year - b.year || a.month - b.month),
  }))
}

// Case-insensitive substring match. A member-number hit keeps all of that
// member's months; otherwise only the months whose own reason matches stay.
function filterWaivers(waivers: Waiver[], search: string): Waiver[] {
  const needle = search.trim().toLowerCase()
  if (needle === '') return waivers
  return waivers.filter(
    (w) => formatMemberNumber(w.member_number).includes(needle) || w.reason.toLowerCase().includes(needle),
  )
}

export function WaiversPage() {
  const [adding, setAdding] = useState(false)
  const [selected, setSelected] = useState<Waiver | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const search = searchParams.get('search') ?? ''

  // replace: true so each keystroke doesn't add a history entry; a new search
  // also drops ?page, since the old page number means nothing for new results.
  const updateSearch = (value: string) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value !== '') next.set('search', value)
        else next.delete('search')
        next.delete('page')
        return next
      },
      { replace: true },
    )
  }

  const { data, isPending, isError, error } = useQuery({
    queryKey: ['waivers'],
    queryFn: fetchWaivers,
  })

  return (
    <section>
      <div style={{ display: 'flex', gap: '1rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>Waivers</h2>
        <input
          type="search"
          placeholder="Search member number or reason"
          value={search}
          onChange={(e) => updateSearch(e.target.value)}
          style={{ minWidth: 260 }}
        />
        <button onClick={() => setAdding(true)} style={{ marginLeft: 'auto' }}>
          Add waiver
        </button>
      </div>

      {isPending ? (
        <p>Loading waivers…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : data.length === 0 ? (
        <p style={{ color: '#666' }}>No waivers yet.</p>
      ) : (
        <WaiversTable groups={groupByMember(filterWaivers(data, search))} onSelect={setSelected} />
      )}

      {adding && <AddWaiverDialog onClose={() => setAdding(false)} />}
      {selected && <WaiverDetailDialog waiver={selected} onClose={() => setSelected(null)} />}
    </section>
  )
}

// Paged client-side: the API returns every waiver at once, and paging has to
// count grouped member rows anyway, which a server-side waiver offset couldn't.
function WaiversTable({ groups, onSelect }: { groups: MemberWaivers[]; onSelect: (waiver: Waiver) => void }) {
  const [searchParams, setSearchParams] = useSearchParams()

  const total = groups.length
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  // A page past the end (hand-edited URL, or deleting the last page's only
  // member) falls back to the last page that still exists.
  const page = Math.min(pageFromParams(searchParams), pageCount)
  const currentOffset = (page - 1) * PAGE_SIZE
  const pageGroups = groups.slice(currentOffset, currentOffset + PAGE_SIZE)
  const rangeFrom = total === 0 ? 0 : currentOffset + 1
  const rangeTo = currentOffset + pageGroups.length

  const goToPage = (target: number) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (target > 1) next.set('page', String(target))
      else next.delete('page')
      return next
    })
  }

  const pager = (
    <div style={pagerStyle}>
      <button onClick={() => goToPage(page - 1)} disabled={page <= 1}>
        ← Prev
      </button>
      <span>
        {rangeFrom}–{rangeTo} of {total}
      </span>
      <button onClick={() => goToPage(page + 1)} disabled={page >= pageCount}>
        Next →
      </button>
    </div>
  )

  return (
    <>
      {pager}
      <div style={{ overflowX: 'auto' }}>
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
          <thead>
            <tr>
              {COLUMNS.map((h, i) => (
                <th key={i} style={i === 0 ? memberNumberHeader : th}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pageGroups.map((group) => (
              <MemberWaiversRow key={group.member_number} group={group} onSelect={onSelect} />
            ))}
            {total === 0 && (
              <tr>
                <td colSpan={COLUMNS.length} style={{ ...td, color: '#666' }}>
                  No waivers match the search.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {pager}
    </>
  )
}

function MemberWaiversRow({ group, onSelect }: { group: MemberWaivers; onSelect: (waiver: Waiver) => void }) {
  return (
    <tr>
      <td style={memberNumberCell}>{formatMemberNumber(group.member_number)}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        <div style={{ display: 'flex', columnGap: '0.75rem', rowGap: '0.25rem', flexWrap: 'wrap' }}>
          {group.waivers.map((w) => (
            <button
              key={waiverKey(w)}
              type="button"
              title={w.reason}
              onClick={() => onSelect(w)}
              style={monthChip}
            >
              {shortMonthLabel(w.year, w.month)}
            </button>
          ))}
        </div>
      </td>
    </tr>
  )
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

// Rendered as a <button> for keyboard/screen-reader access, styled as a link.
const monthChip: CSSProperties = {
  background: 'none',
  border: 'none',
  padding: 0,
  font: 'inherit',
  color: '#0645ad',
  textDecoration: 'underline',
  cursor: 'pointer',
}

// width: 1% shrinks the column to its content (the single-line header, since
// th is nowrap); the extra right padding keeps it from crowding the months.
const memberNumberHeader: CSSProperties = {
  ...th,
  width: '1%',
  paddingRight: '2rem',
}

const memberNumberCell: CSSProperties = {
  ...td,
  paddingRight: '2rem',
  fontVariantNumeric: 'tabular-nums',
}
