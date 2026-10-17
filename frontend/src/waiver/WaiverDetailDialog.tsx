import type { CSSProperties } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { errMessage } from '../api/client'
import { deleteWaiver, type Waiver } from '../api/payments'
import { Overlay } from '../Overlay'
import { formatMemberNumber, monthLabel } from './format'

export function WaiverDetailDialog({ waiver, onClose }: { waiver: Waiver; onClose: () => void }) {
  const qc = useQueryClient()

  const remove = useMutation({
    mutationFn: () => deleteWaiver(waiver.member_number, waiver.year, waiver.month),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['waivers'] })
      onClose()
    },
  })

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>
        Member {formatMemberNumber(waiver.member_number)} — {monthLabel(waiver.year, waiver.month)}
      </h2>
      <dl style={{ margin: '0 0 1rem' }}>
        <dt style={detailTerm}>Reason</dt>
        <dd style={{ ...detailValue, whiteSpace: 'pre-wrap' }}>{waiver.reason}</dd>
        <dt style={detailTerm}>Created</dt>
        <dd style={detailValue}>{new Date(waiver.created_at).toLocaleString()}</dd>
      </dl>

      {remove.error && <p style={{ color: '#b00' }}>{errMessage(remove.error)}</p>}

      <div style={{ display: 'flex', gap: '0.75rem' }}>
        <button
          type="button"
          disabled={remove.isPending}
          onClick={() => {
            if (window.confirm(`Delete waiver for member ${waiver.member_number}, ${monthLabel(waiver.year, waiver.month)}?`)) {
              remove.mutate()
            }
          }}
        >
          {remove.isPending ? 'Deleting…' : 'Delete'}
        </button>
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Overlay>
  )
}

const detailTerm: CSSProperties = {
  fontWeight: 600,
  marginTop: '0.5rem',
}

const detailValue: CSSProperties = {
  margin: '0.15rem 0 0',
}
