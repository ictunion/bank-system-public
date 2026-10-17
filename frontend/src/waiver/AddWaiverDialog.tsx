import { type CSSProperties, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { errMessage } from '../api/client'
import { createWaiver, type WaiveResult } from '../api/payments'
import { Overlay } from '../Overlay'
import { monthLabel } from './format'

export function AddWaiverDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [memberNumber, setMemberNumber] = useState('')
  const [year, setYear] = useState('')
  const [month, setMonth] = useState('')
  const [endYear, setEndYear] = useState('')
  const [endMonth, setEndMonth] = useState('')
  const [reason, setReason] = useState('')
  const [result, setResult] = useState<WaiveResult | null>(null)

  const hasEnd = endYear.trim() !== '' || endMonth.trim() !== ''

  const create = useMutation({
    mutationFn: () =>
      createWaiver(
        Number(memberNumber),
        Number(year),
        Number(month),
        reason.trim(),
        hasEnd ? { year: Number(endYear), month: Number(endMonth) } : undefined,
      ),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['waivers'] })
      // A clean run (nothing skipped) closes the dialog same as before; if
      // something was skipped (already paid), stay open and show which
      // months so the admin isn't left guessing why the count looks short.
      if (data.skipped.length === 0) {
        onClose()
      } else {
        setResult(data)
      }
    },
  })

  const canSave =
    memberNumber.trim() !== '' &&
    year.trim() !== '' &&
    month.trim() !== '' &&
    Number(month) >= 1 &&
    Number(month) <= 12 &&
    (!hasEnd || (endYear.trim() !== '' && endMonth.trim() !== '' && Number(endMonth) >= 1 && Number(endMonth) <= 12)) &&
    reason.trim() !== '' &&
    !create.isPending

  if (result) {
    return (
      <Overlay onClose={onClose}>
        <h2 style={{ marginTop: 0 }}>Waived {result.waived.length} month(s)</h2>
        <p>
          Skipped (already covered by a payment):{' '}
          {result.skipped.map((m) => monthLabel(m.year, m.month)).join(', ')}
        </p>
        <button type="button" onClick={onClose}>
          Done
        </button>
      </Overlay>
    )
  }

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
        <p style={{ color: '#666', fontSize: '0.8rem', margin: '0 0 0.25rem' }}>
          Optional end of range — waives every month from above through here (inclusive).
          Already-paid months in between are skipped.
        </p>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <label style={field}>
            End year
            <input type="number" value={endYear} onChange={(e) => setEndYear(e.target.value)} />
          </label>
          <label style={field}>
            End month
            <input type="number" min={1} max={12} value={endMonth} onChange={(e) => setEndMonth(e.target.value)} />
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
