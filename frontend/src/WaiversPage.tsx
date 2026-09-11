import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { errMessage } from './api/client'
import { createWaiver, deleteWaiver, fetchWaivers, type Waiver } from './api/payments'
import { Overlay } from './Overlay'

const COLUMNS = ['Member', 'Month', 'Reason', 'Created', ''] as const

const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
] as const

function monthLabel(year: number, month: number): string {
  return `${MONTH_NAMES[month - 1] ?? month} ${year}`
}

function waiverKey(w: Waiver): string {
  return `${w.member_number}-${w.year}-${w.month}`
}

export function WaiversPage() {
  const [adding, setAdding] = useState(false)

  const { data, isPending, isError, error } = useQuery({
    queryKey: ['waivers'],
    queryFn: fetchWaivers,
  })

  return (
    <section>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <h2 style={{ margin: 0 }}>Waivers</h2>
        <button onClick={() => setAdding(true)}>Add waiver</button>
      </div>

      {isPending ? (
        <p>Loading waivers…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : data.length === 0 ? (
        <p style={{ color: '#666' }}>No waivers yet.</p>
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
              {data.map((w) => (
                <WaiverRow key={waiverKey(w)} waiver={w} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adding && <AddWaiverDialog onClose={() => setAdding(false)} />}
    </section>
  )
}

function WaiverRow({ waiver }: { waiver: Waiver }) {
  const qc = useQueryClient()
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const remove = useMutation({
    mutationFn: () => deleteWaiver(waiver.member_number, waiver.year, waiver.month),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['waivers'] }),
    onError: (err) => setDeleteError(errMessage(err)),
  })

  return (
    <tr>
      <td style={td}>{waiver.member_number}</td>
      <td style={td}>{monthLabel(waiver.year, waiver.month)}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>{waiver.reason}</td>
      <td style={td}>{new Date(waiver.created_at).toLocaleDateString()}</td>
      <td style={{ ...td, whiteSpace: 'normal' }}>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'baseline', flexWrap: 'wrap' }}>
          <button
            disabled={remove.isPending}
            onClick={() => {
              setDeleteError(null)
              if (window.confirm(`Delete waiver for member ${waiver.member_number}, ${monthLabel(waiver.year, waiver.month)}?`)) {
                remove.mutate()
              }
            }}
          >
            {remove.isPending ? 'Deleting…' : 'Delete'}
          </button>
          {deleteError && <span style={{ color: '#b00' }}>{deleteError}</span>}
        </div>
      </td>
    </tr>
  )
}

function AddWaiverDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [memberNumber, setMemberNumber] = useState('')
  const [year, setYear] = useState('')
  const [month, setMonth] = useState('')
  const [reason, setReason] = useState('')

  const create = useMutation({
    mutationFn: () => createWaiver(Number(memberNumber), Number(year), Number(month), reason.trim()),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['waivers'] })
      onClose()
    },
  })

  const canSave =
    memberNumber.trim() !== '' &&
    year.trim() !== '' &&
    month.trim() !== '' &&
    Number(month) >= 1 &&
    Number(month) <= 12 &&
    reason.trim() !== '' &&
    !create.isPending

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>Add waiver</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) create.mutate()
        }}
      >
        <label style={field}>
          Member number
          <input type="number" value={memberNumber} onChange={(e) => setMemberNumber(e.target.value)} autoFocus />
        </label>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <label style={field}>
            Year
            <input type="number" value={year} onChange={(e) => setYear(e.target.value)} />
          </label>
          <label style={field}>
            Month
            <input type="number" min={1} max={12} value={month} onChange={(e) => setMonth(e.target.value)} />
          </label>
        </div>
        <label style={field}>
          Reason
          <textarea rows={3} value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>

        {create.error && <p style={{ color: '#b00' }}>{errMessage(create.error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem' }}>
          <button type="submit" disabled={!canSave}>
            {create.isPending ? 'Saving…' : 'Save'}
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
  maxWidth: 320,
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
