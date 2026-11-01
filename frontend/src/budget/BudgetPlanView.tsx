import { createContext, type CSSProperties, type ReactNode, useContext, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  type BudgetLine,
  type BudgetLineKind,
  type BudgetReport,
  type BudgetReportLine,
  createBudget,
  fetchBudget,
  fetchBudgets,
  lineToInput,
  type UnbudgetedTotal,
  updateBudgetLine,
  updateBudgetNote,
} from '../api/budget'
import { categoryLabel, categoryLabelById, fetchCategories } from '../api/categories'
import { errMessage } from '../api/client'
import { useHasRole } from '../roles'
import { BudgetLineDialog } from './BudgetLineDialog'

const CURRENT_YEAR = new Date().getFullYear()

// Amounts arrive as decimal strings; all arithmetic here is in whole
// hellers so sums and differences don't pick up float noise.
function toCents(amount: string): number {
  return Math.round(Number(amount) * 100)
}

const czk = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'CZK' })

function formatCents(cents: number): string {
  return czk.format(cents / 100)
}

interface Figures {
  projected: number
  real: number
}

function sumFigures(lines: BudgetReportLine[]): Figures {
  return lines.reduce(
    (sum, line) => ({ projected: sum.projected + toCents(line.projected), real: sum.real + toCents(line.real) }),
    { projected: 0, real: 0 },
  )
}

// Lines of one kind as the table shows them: grouped lines collected under
// their group (a group sits where its first line is — lines arrive in
// display order), ungrouped lines on their own.
type Block = { group: null; line: BudgetReportLine } | { group: string; lines: BudgetReportLine[] }

function toBlocks(lines: BudgetReportLine[]): Block[] {
  const blocks: Block[] = []
  const groups = new Map<string, BudgetReportLine[]>()
  for (const line of lines) {
    if (line.group_name === null) {
      blocks.push({ group: null, line })
      continue
    }
    const existing = groups.get(line.group_name)
    if (existing) {
      existing.push(line)
    } else {
      const groupLines = [line]
      groups.set(line.group_name, groupLines)
      blocks.push({ group: line.group_name, lines: groupLines })
    }
  }
  return blocks
}

// What edit mode adds to the table: per-line edit/reorder buttons and an
// "Add line" button per section. null outside edit mode.
interface EditActions {
  editLine: (line: BudgetReportLine) => void
  addLine: (kind: BudgetLineKind) => void
  moveLine: (line: BudgetReportLine, delta: -1 | 1) => void
  isFirst: (line: BudgetReportLine) => boolean
  isLast: (line: BudgetReportLine) => boolean
  moving: boolean
}

const EditContext = createContext<EditActions | null>(null)

export function BudgetPlanView() {
  const canManage = useHasRole('manage-budget')
  const budgets = useQuery({ queryKey: ['budgets'], queryFn: fetchBudgets })
  const [selectedYear, setSelectedYear] = useState<number | null>(null)
  const [editing, setEditing] = useState(false)

  const budgetYears = (budgets.data ?? []).map((b) => b.year)
  // Default: this year if it has a budget, else the newest one there is.
  const year = selectedYear ?? (budgetYears.includes(CURRENT_YEAR) ? CURRENT_YEAR : (budgetYears[0] ?? CURRENT_YEAR))
  // Admins can also start next year's budget while it's being prepared.
  const yearOptions = [...new Set([CURRENT_YEAR, ...(canManage ? [CURRENT_YEAR + 1] : []), ...budgetYears])].sort(
    (a, b) => b - a,
  )
  const hasBudget = budgetYears.includes(year)
  const isEditing = canManage && editing && hasBudget

  const report = useQuery({
    queryKey: ['budget', year],
    queryFn: () => fetchBudget(year),
    enabled: hasBudget,
  })

  return (
    <section>
      <div style={{ display: 'flex', gap: '1rem', alignItems: 'center', marginBottom: '1rem' }}>
        <label>
          Year{' '}
          <select value={year} onChange={(e) => setSelectedYear(Number(e.target.value))}>
            {yearOptions.map((y) => (
              <option key={y} value={y}>
                {y}
                {budgetYears.includes(y) ? '' : ' (no budget)'}
              </option>
            ))}
          </select>
        </label>
        {!isEditing && report.data?.note && <span style={{ color: '#666' }}>{report.data.note}</span>}
        {canManage && hasBudget && (
          <button onClick={() => setEditing((value) => !value)}>{isEditing ? 'Done editing' : 'Edit'}</button>
        )}
      </div>

      {isEditing && report.data && (
        <BudgetSettings key={year} year={year} note={report.data.note} />
      )}

      {budgets.isPending ? (
        <p>Loading…</p>
      ) : budgets.isError ? (
        <p>Failed to load: {errMessage(budgets.error)}</p>
      ) : !hasBudget ? (
        canManage ? (
          <CreateBudgetForm
            key={year}
            year={year}
            // The newest budget before this year, offered as a starting point.
            sourceYear={budgetYears.filter((y) => y < year).sort((a, b) => b - a)[0] ?? null}
            onCreated={() => setEditing(true)}
          />
        ) : (
          <p style={{ color: '#666' }}>There is no budget for {year} yet.</p>
        )
      ) : report.isPending ? (
        <p>Loading…</p>
      ) : report.isError ? (
        <p>Failed to load: {errMessage(report.error)}</p>
      ) : (
        <BudgetTable report={report.data} editing={isEditing} />
      )}
    </section>
  )
}

function CreateBudgetForm({
  year,
  sourceYear,
  onCreated,
}: {
  year: number
  sourceYear: number | null
  onCreated: () => void
}) {
  const qc = useQueryClient()
  const [note, setNote] = useState('')
  const [copy, setCopy] = useState(sourceYear !== null)

  const create = useMutation({
    mutationFn: () => createBudget(year, note.trim() === '' ? null : note.trim(), copy ? sourceYear : null),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['budgets'] })
      onCreated()
    },
  })

  return (
    <form
      style={{ border: '1px solid #ddd', borderRadius: 6, padding: '1rem', maxWidth: 480 }}
      onSubmit={(e) => {
        e.preventDefault()
        if (!create.isPending) create.mutate()
      }}
    >
      <p style={{ marginTop: 0 }}>There is no budget for {year} yet.</p>
      <label style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem', marginBottom: '0.75rem' }}>
        Note (optional)
        <input value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. approved at members' meeting 2026-03-14" />
      </label>
      {sourceYear !== null && (
        <label style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginBottom: '0.75rem' }}>
          <input type="checkbox" checked={copy} onChange={(e) => setCopy(e.target.checked)} />
          Copy lines, amounts and categories from {sourceYear}
        </label>
      )}
      {create.error && <p style={{ color: '#b00' }}>{errMessage(create.error)}</p>}
      <button type="submit" disabled={create.isPending}>
        {create.isPending ? 'Creating…' : `Create budget for ${year}`}
      </button>
    </form>
  )
}

// Edit mode's budget-level control: the note. Deleting a whole budget is
// deliberately not offered here (DELETE /budgets/{year} exists in the API
// only) — a voted budget is never meant to disappear.
function BudgetSettings({ year, note }: { year: number; note: string | null }) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState(note ?? '')

  const saveNote = useMutation({
    mutationFn: () => updateBudgetNote(year, draft.trim()),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['budget', year] })
      qc.invalidateQueries({ queryKey: ['budgets'] })
    },
  })

  return (
    <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center', flexWrap: 'wrap', marginBottom: '1rem' }}>
      <label>
        Note{' '}
        <input value={draft} onChange={(e) => setDraft(e.target.value)} style={{ width: '22rem' }} />
      </label>
      <button disabled={saveNote.isPending || draft.trim() === (note ?? '')} onClick={() => saveNote.mutate()}>
        {saveNote.isPending ? 'Saving…' : 'Save note'}
      </button>
      {saveNote.error && <span style={{ color: '#b00' }}>{errMessage(saveNote.error)}</span>}
    </div>
  )
}

function BudgetTable({ report, editing }: { report: BudgetReport; editing: boolean }) {
  const qc = useQueryClient()
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })
  // null = closed; otherwise the line being edited, or a new line's kind.
  const [dialog, setDialog] = useState<{ line: BudgetLine | null; kind: BudgetLineKind } | null>(null)

  // Swaps a line with its neighbor in its section, then renumbers that
  // section's sort_order in steps of 10 and saves the lines whose number
  // changed (lines that never had an explicit order all start at 0).
  const move = useMutation({
    mutationFn: async ({ line, delta }: { line: BudgetReportLine; delta: -1 | 1 }) => {
      const section = report.lines.filter((l) => l.kind === line.kind)
      const index = section.findIndex((l) => l.id === line.id)
      const target = index + delta
      if (target < 0 || target >= section.length) return
      const reordered = [...section]
      ;[reordered[index], reordered[target]] = [reordered[target], reordered[index]]
      for (const [position, l] of reordered.entries()) {
        const sortOrder = (position + 1) * 10
        if (l.sort_order !== sortOrder) {
          await updateBudgetLine(report.year, l.id, { ...lineToInput(l), sort_order: sortOrder })
        }
      }
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ['budget', report.year] }),
  })

  const sectionOf = (line: BudgetReportLine) => report.lines.filter((l) => l.kind === line.kind)
  const editActions: EditActions | null = editing
    ? {
        editLine: (line) => setDialog({ line, kind: line.kind }),
        addLine: (kind) => setDialog({ line: null, kind }),
        moveLine: (line, delta) => move.mutate({ line, delta }),
        isFirst: (line) => sectionOf(line)[0]?.id === line.id,
        isLast: (line) => {
          const section = sectionOf(line)
          return section[section.length - 1]?.id === line.id
        },
        moving: move.isPending,
      }
    : null

  const incomeLines = report.lines.filter((line) => line.kind === 'income')
  const expenseLines = report.lines.filter((line) => line.kind === 'expense')
  const incomes = sumFigures(incomeLines)
  const expenses = sumFigures(expenseLines)

  const unbudgetedIncoming = report.unbudgeted.filter((u) => u.direction === 'incoming')
  const unbudgetedOutgoing = report.unbudgeted.filter((u) => u.direction === 'outgoing')
  const unbudgetedNet =
    unbudgetedIncoming.reduce((sum, u) => sum + toCents(u.real), 0) -
    unbudgetedOutgoing.reduce((sum, u) => sum + toCents(u.real), 0)

  const categoryNames = (line: BudgetReportLine) =>
    line.category_ids.map((id) => categoryLabelById(categories.data, id)).join(', ')

  return (
    <>
      {move.error && <p style={{ color: '#b00' }}>Reordering failed: {errMessage(move.error)}</p>}
      {report.other_currency_count > 0 && (
        <p style={warning}>
          {report.other_currency_count} non-CZK transaction(s) in {report.year} are not included in any figure.
        </p>
      )}

      <EditContext.Provider value={editActions}>
        <div style={{ overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
            <thead>
              <tr>
                <th style={th} />
                <th style={th}>Budgeted</th>
                <th style={th}>Actual</th>
                <th style={th}>Note</th>
                <th style={th}>
                  <span title={PERCENT_EXPLANATION} style={{ cursor: 'help', borderBottom: '1px dotted #666' }}>
                    % of budgeted
                  </span>
                </th>
                {editing && <th style={th} />}
              </tr>
            </thead>
            <tbody>
              <SectionRows title="Incomes" kind="income" lines={incomeLines} categoryNames={categoryNames} />
              <SpacerRow />
              <FigureRow label="Incomes sum" kind="income" figures={incomes} emphasis="sum" />

              <SectionRows title="Expenses" kind="expense" lines={expenseLines} categoryNames={categoryNames} />
              <SpacerRow />
              <FigureRow label="Expenses sum" kind="expense" figures={expenses} emphasis="sum" />
              <SpacerRow />

              <FigureRow
                label="Difference (incomes − expenses)"
                kind="income"
                figures={{ projected: incomes.projected - expenses.projected, real: incomes.real - expenses.real }}
                emphasis="total"
              />

              {report.unbudgeted.length > 0 && (
                <>
                  <HeaderRow>
                    <span style={{ color: WARNING_COLOR }}>
                      Unbudgeted — transactions in categories no budget line covers
                    </span>
                  </HeaderRow>
                  {[...unbudgetedIncoming, ...unbudgetedOutgoing].map((u) => (
                    <UnbudgetedRow key={`${u.category_id}-${u.direction}`} total={u} />
                  ))}
                  <tr>
                    <td style={{ ...td, fontWeight: 600, textAlign: 'center' }}>Difference incl. unbudgeted</td>
                    <td style={tdNumber}>—</td>
                    <td style={{ ...tdNumber, fontWeight: 600 }}>{formatCents(incomes.real - expenses.real + unbudgetedNet)}</td>
                    <td style={td} />
                    <td style={tdNumber} />
                    {editing && <td style={td} />}
                  </tr>
                </>
              )}
            </tbody>
          </table>
        </div>
      </EditContext.Provider>

      {dialog !== null && (
        <BudgetLineDialog
          year={report.year}
          line={dialog.line}
          kind={dialog.kind}
          lines={report.lines}
          onClose={() => setDialog(null)}
        />
      )}
    </>
  )
}

function SectionRows({
  title,
  kind,
  lines,
  categoryNames,
}: {
  title: string
  kind: BudgetLineKind
  lines: BudgetReportLine[]
  categoryNames: (line: BudgetReportLine) => string
}) {
  const edit = useContext(EditContext)
  return (
    <>
      <HeaderRow>
        <SectionTitle title={title} kind={kind} />
      </HeaderRow>
      {lines.length === 0 && (
        <tr>
          <td style={{ ...td, color: '#999' }} colSpan={edit ? 6 : 5}>
            No lines.
          </td>
        </tr>
      )}
      {toBlocks(lines).map((block) =>
        block.group === null ? (
          <LineRow key={block.line.id} line={block.line} kind={kind} categories={categoryNames(block.line)} />
        ) : (
          <GroupRows key={`group-${block.group}`} group={block.group} lines={block.lines} kind={kind} categoryNames={categoryNames} />
        ),
      )}
    </>
  )
}

function GroupRows({
  group,
  lines,
  kind,
  categoryNames,
}: {
  group: string
  lines: BudgetReportLine[]
  kind: BudgetLineKind
  categoryNames: (line: BudgetReportLine) => string
}) {
  return (
    <>
      {lines.map((line) => (
        <LineRow key={line.id} line={line} kind={kind} categories={categoryNames(line)} />
      ))}
      {/* The group's own row comes after its lines, as their sum. */}
      <FigureRow label={group} kind={kind} figures={sumFigures(lines)} emphasis="group" />
    </>
  )
}

function LineRow({
  line,
  kind,
  categories,
}: {
  line: BudgetReportLine
  kind: BudgetLineKind
  categories: string
}) {
  // The mapped categories are an editing aid — shown only in edit mode.
  const edit = useContext(EditContext)
  return (
    <FigureRow
      label={
        <>
          {line.name}
          {edit && (
            <span style={{ display: 'block', color: '#666', fontSize: '0.75rem' }}>
              {categories === '' ? 'no categories mapped' : categories}
            </span>
          )}
        </>
      }
      kind={kind}
      figures={{ projected: toCents(line.projected), real: toCents(line.real) }}
      line={line}
    />
  )
}

function SectionTitle({ title, kind }: { title: string; kind: BudgetLineKind }) {
  const edit = useContext(EditContext)
  return (
    <span style={{ display: 'flex', alignItems: 'baseline', gap: '0.75rem' }}>
      {title}
      {edit && (
        <button style={{ fontWeight: 400 }} onClick={() => edit.addLine(kind)}>
          Add line
        </button>
      )}
    </span>
  )
}

function LineActions({ line }: { line: BudgetReportLine }) {
  const edit = useContext(EditContext)
  if (!edit) return null
  return (
    <span style={{ display: 'flex', gap: '0.25rem', justifyContent: 'flex-end' }}>
      <button title="Edit line" onClick={() => edit.editLine(line)}>
        ✎
      </button>
      <button title="Move up" disabled={edit.moving || edit.isFirst(line)} onClick={() => edit.moveLine(line, -1)}>
        ↑
      </button>
      <button title="Move down" disabled={edit.moving || edit.isLast(line)} onClick={() => edit.moveLine(line, 1)}>
        ↓
      </button>
    </span>
  )
}

// % = Real / Projected. Colors: a line's name cell carries its kind's
// color; group and sum rows are colored across the whole row.
function FigureRow({
  label,
  kind,
  figures,
  emphasis,
  line,
}: {
  label: ReactNode
  kind: BudgetLineKind
  figures: Figures
  emphasis?: 'group' | 'sum' | 'total'
  // Set for a single line's row, which gets the edit buttons in edit mode.
  line?: BudgetReportLine
}) {
  const edit = useContext(EditContext)
  const percent = figures.projected === 0 ? '—' : `${Math.round((figures.real / figures.projected) * 100)} %`

  const kindColor = KIND_COLORS[kind]
  const rowStyle: CSSProperties = {
    fontWeight: emphasis === undefined ? undefined : emphasis === 'group' ? 600 : 700,
    fontStyle: emphasis === 'group' ? 'italic' : undefined,
    background: emphasis === 'total' ? '#f3f6fb' : emphasis !== undefined ? kindColor : undefined,
  }
  // Lines are left-aligned with no indent; group, sum and total rows are
  // titles, centered.
  const labelStyle: CSSProperties = {
    ...td,
    textAlign: emphasis === undefined ? 'left' : 'center',
    background: emphasis === undefined ? kindColor : undefined,
  }
  return (
    <tr style={rowStyle}>
      <td style={labelStyle}>{label}</td>
      <td style={tdNumber}>{formatCents(figures.projected)}</td>
      <td style={tdNumber}>{formatCents(figures.real)}</td>
      <td style={tdNote}>{line?.note}</td>
      <td style={tdNumber}>{percent}</td>
      {edit && <td style={tdNumber}>{line && <LineActions line={line} />}</td>}
    </tr>
  )
}

function UnbudgetedRow({ total }: { total: UnbudgetedTotal }) {
  const edit = useContext(EditContext)
  const cents = toCents(total.real)
  return (
    <tr style={{ color: WARNING_COLOR }}>
      <td style={td}>
        {categoryLabel(total.category)} ({total.direction === 'incoming' ? 'income' : 'expense'})
      </td>
      <td style={tdNumber}>—</td>
      <td style={tdNumber}>{formatCents(total.direction === 'incoming' ? cents : -cents)}</td>
      <td style={td} />
      <td style={tdNumber} />
      {edit && <td style={td} />}
    </tr>
  )
}

// An empty row of normal line height, no background or borders, separating
// a section's lines from its sum row, and the sums from the difference.
function SpacerRow() {
  const edit = useContext(EditContext)
  return (
    <tr aria-hidden="true">
      <td colSpan={edit ? 6 : 5} style={{ ...td, border: 'none' }}>
        &nbsp;
      </td>
    </tr>
  )
}

function HeaderRow({ children }: { children: ReactNode }) {
  const edit = useContext(EditContext)
  return (
    <tr>
      <td colSpan={edit ? 6 : 5} style={{ ...td, fontWeight: 700, paddingTop: '0.9rem', borderBottom: '2px solid #ddd' }}>
        {children}
      </td>
    </tr>
  )
}

const WARNING_COLOR = '#8a5a00'

const KIND_COLORS: Record<BudgetLineKind, string> = {
  income: '#d7e4bd',
  expense: '#e6b9b8',
}

const warning: CSSProperties = {
  color: WARNING_COLOR,
  background: '#fff6e0',
  borderRadius: 4,
  padding: '0.4rem 0.6rem',
  fontSize: '0.875rem',
  margin: '0 0 0.75rem',
}

// Every cell is bordered, so the table reads as a grid like the voted sheet.
const GRID_BORDER = '1px solid #d4d4d4'

const PERCENT_EXPLANATION =
  "Actual as a percentage of this line's budgeted amount, for the whole year"

const th: CSSProperties = {
  textAlign: 'center',
  border: GRID_BORDER,
  borderBottom: '2px solid #bbb',
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}


const td: CSSProperties = {
  border: GRID_BORDER,
  padding: '0.35rem 0.6rem',
}

const tdNumber: CSSProperties = { ...td, textAlign: 'right', whiteSpace: 'nowrap' }

const tdNote: CSSProperties = { ...td, color: '#555', fontSize: '0.8125rem', maxWidth: 320 }
