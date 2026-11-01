import { type CSSProperties, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  type BudgetLine,
  type BudgetLineKind,
  createBudgetLine,
  deleteBudgetLine,
  updateBudgetLine,
} from '../api/budget'
import { categoryOptionLabel, fetchCategories, withChildrenUnderParents } from '../api/categories'
import { errMessage } from '../api/client'
import { Overlay } from '../Overlay'

// Same shape the backend accepts: non-negative, at most two decimals.
const AMOUNT_PATTERN = /^\d{1,12}(\.\d{1,2})?$/

// Accepts the way people type amounts — spaces as thousand separators, a
// decimal comma — and returns the API form, or null if it isn't one.
function normalizeAmount(value: string): string | null {
  const normalized = value.replace(/\s/g, '').replace(',', '.')
  if (normalized === '') return '0'
  return AMOUNT_PATTERN.test(normalized) ? normalized : null
}

// line null = a new line of `kind`. `lines` is every line of the budget —
// for the group suggestions, which categories other lines already use, and
// where a new line goes in the order.
export function BudgetLineDialog({
  year,
  line,
  kind,
  lines,
  onClose,
}: {
  year: number
  line: BudgetLine | null
  kind: BudgetLineKind
  lines: BudgetLine[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })

  const [name, setName] = useState(line?.name ?? '')
  const [lineKind, setLineKind] = useState<BudgetLineKind>(line?.kind ?? kind)
  const [groupName, setGroupName] = useState(line?.group_name ?? '')
  const [budgeted, setBudgeted] = useState(line?.budgeted ?? '')
  const [categoryIds, setCategoryIds] = useState<string[]>(line?.category_ids ?? [])
  const [note, setNote] = useState(line?.note ?? '')

  const groupNames = [...new Set(lines.map((l) => l.group_name).filter((g): g is string => g !== null))]
  // category id → name of the other line that already has it
  const usedBy = new Map<string, string>()
  for (const other of lines) {
    if (other.id === line?.id) continue
    for (const id of other.category_ids) usedBy.set(id, other.name)
  }

  const done = () => {
    qc.invalidateQueries({ queryKey: ['budget', year] })
    onClose()
  }

  const normalizedBudgeted = normalizeAmount(budgeted)
  const save = useMutation({
    mutationFn: () => {
      const input = {
        kind: lineKind,
        group_name: groupName.trim() === '' ? null : groupName.trim(),
        name: name.trim(),
        budgeted: normalizedBudgeted ?? '0',
        category_ids: categoryIds,
        note: note.trim() === '' ? null : note.trim(),
        // A new line goes last; an edited one keeps its place.
        sort_order: line?.sort_order ?? Math.max(0, ...lines.map((l) => l.sort_order)) + 10,
      }
      return line === null ? createBudgetLine(year, input) : updateBudgetLine(year, line.id, input)
    },
    onSuccess: done,
  })
  const remove = useMutation({
    mutationFn: () => deleteBudgetLine(year, line?.id ?? 0),
    onSuccess: done,
  })

  const toggleCategory = (id: string, checked: boolean) =>
    setCategoryIds((previous) => (checked ? [...previous, id] : previous.filter((c) => c !== id)))

  const canSave = name.trim() !== '' && normalizedBudgeted !== null && !save.isPending && !remove.isPending
  const error = save.error ?? remove.error

  return (
    <Overlay onClose={onClose}>
      <h2 style={{ marginTop: 0 }}>{line === null ? 'Add budget line' : 'Edit budget line'}</h2>
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (canSave) save.mutate()
        }}
      >
        <label style={field}>
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Legal" autoFocus />
        </label>
        <label style={field}>
          Kind
          <select value={lineKind} onChange={(e) => setLineKind(e.target.value as BudgetLineKind)}>
            <option value="income">Income</option>
            <option value="expense">Expense</option>
          </select>
        </label>
        <label style={field}>
          Group (optional)
          <input
            value={groupName}
            onChange={(e) => setGroupName(e.target.value)}
            list="budget-group-names"
            placeholder="e.g. Employment expenses"
          />
          <datalist id="budget-group-names">
            {groupNames.map((g) => (
              <option key={g} value={g} />
            ))}
          </datalist>
        </label>
        <label style={field}>
          Budgeted (CZK)
          <input value={budgeted} onChange={(e) => setBudgeted(e.target.value)} inputMode="decimal" placeholder="60000" />
        </label>
        {normalizedBudgeted === null && (
          <p style={{ color: '#b00', margin: '-0.5rem 0 0.75rem' }}>Enter a non-negative amount, e.g. 60000 or 60000.50.</p>
        )}
        <label style={field}>
          Note (optional, shown in the table)
          <textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} maxLength={500} />
        </label>

        <fieldset style={{ border: '1px solid #ddd', borderRadius: 6, padding: '0.75rem', marginBottom: '0.75rem' }}>
          <legend>Categories counted in this line</legend>
          <p style={{ color: '#666', fontSize: '0.8125rem', margin: '0 0 0.5rem' }}>
            A category can feed only one line. A parent category doesn't include its sub-categories — tick each one
            you want here.
          </p>
          <div style={{ maxHeight: 240, overflowY: 'auto' }}>
            {withChildrenUnderParents(categories.data ?? []).map((c) => {
              const owner = usedBy.get(c.id)
              return (
                <label key={c.id} style={{ display: 'flex', gap: '0.4rem', alignItems: 'baseline', color: owner ? '#999' : undefined }}>
                  <input
                    type="checkbox"
                    checked={categoryIds.includes(c.id)}
                    disabled={owner !== undefined}
                    onChange={(e) => toggleCategory(c.id, e.target.checked)}
                  />
                  <span style={{ whiteSpace: 'pre' }}>{categoryOptionLabel(c)}</span>
                  {owner && <span style={{ fontSize: '0.75rem' }}>used by: {owner}</span>}
                </label>
              )
            })}
          </div>
        </fieldset>

        {error && <p style={{ color: '#b00' }}>{errMessage(error)}</p>}

        <div style={{ display: 'flex', gap: '0.75rem', marginTop: '1rem', alignItems: 'center' }}>
          <button type="submit" disabled={!canSave}>
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          {line !== null && (
            <button
              type="button"
              style={{ marginLeft: 'auto', color: '#b00' }}
              disabled={save.isPending || remove.isPending}
              onClick={() => {
                if (window.confirm(`Delete budget line "${line.name}"? Its categories become unbudgeted.`)) remove.mutate()
              }}
            >
              {remove.isPending ? 'Deleting…' : 'Delete'}
            </button>
          )}
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
  maxWidth: 360,
}
