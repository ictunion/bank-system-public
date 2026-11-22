import { createContext, type CSSProperties, type ReactNode, useContext, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import {
  type BudgetLine,
  type BudgetLineKind,
  type BudgetReport,
  type BudgetReportLine,
  createBudget,
  downloadAuditorReport,
  fetchBudget,
  fetchBudgets,
  lineToInput,
  type UnbudgetedTotal,
  updateBudgetLine,
  updateBudgetNote,
  uploadAuditorReport,
} from '../api/budget'
import { categoryLabel, categoryLabelById, fetchCategories } from '../api/categories'
import { errMessage } from '../api/client'
import { useHasRole } from '../roles'
import { BudgetLineDialog } from './BudgetLineDialog'
import { useIsNarrow } from './useIsNarrow'

const CURRENT_YEAR = new Date().getFullYear()

// Amounts arrive as decimal strings; all arithmetic here is in whole
// hellers so sums and differences don't pick up float noise.
function toCents(amount: string): number {
  return Math.round(Number(amount) * 100)
}

const czk = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'CZK' })
// Phones leave out the currency symbol (the column titles carry it) so large
// amounts fit the screen width.
const czkPlain = new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })

function formatCents(cents: number, plain = false): string {
  return (plain ? czkPlain : czk).format(cents / 100)
}

interface Figures {
  budgeted: number
  actual: number
}

function sumFigures(lines: BudgetReportLine[]): Figures {
  return lines.reduce(
    (sum, line) => ({ budgeted: sum.budgeted + toCents(line.budgeted), actual: sum.actual + toCents(line.actual) }),
    { budgeted: 0, actual: 0 },
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

// Phones show each row as two table rows — the name (with its note and edit
// buttons) spanning the full width, then Budgeted / Actual / % below it — so
// the table fits the screen without scrolling sideways.
function useColumnCount(): number {
  const edit = useContext(EditContext)
  return useIsNarrow() ? 3 : 5 + (edit ? 1 : 0)
}

export function BudgetPlanView() {
  const canManage = useHasRole('manage-budget')
  const budgets = useQuery({ queryKey: ['budgets'], queryFn: fetchBudgets })
  // The chosen year lives in the URL (?year=2026) so a link opens the same budget.
  const [searchParams, setSearchParams] = useSearchParams()
  const yearParam = Number(searchParams.get('year'))
  const selectedYear = Number.isInteger(yearParam) && yearParam > 0 ? yearParam : null
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
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem 1rem', alignItems: 'center', marginBottom: '1rem' }}>
        <label>
          Year{' '}
          <select value={year} onChange={(e) => setSearchParams({ year: e.target.value }, { replace: true })}>
            {yearOptions.map((y) => (
              <option key={y} value={y}>
                {y}
                {budgetYears.includes(y) ? '' : ' (no budget)'}
              </option>
            ))}
          </select>
        </label>
        {!isEditing && report.data?.note && <span style={{ color: '#666' }}>{report.data.note}</span>}
        {!isEditing && report.data?.auditor_report_uploaded_at && <AuditorReportLink year={year} />}
        {canManage && hasBudget && (
          <button onClick={() => setEditing((value) => !value)}>{isEditing ? 'Done editing' : 'Edit'}</button>
        )}
      </div>

      {isEditing && report.data && (
        <BudgetSettings
          key={year}
          year={year}
          note={report.data.note}
          auditorReportUploadedAt={report.data.auditor_report_uploaded_at}
        />
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
function BudgetSettings({
  year,
  note,
  auditorReportUploadedAt,
}: {
  year: number
  note: string | null
  auditorReportUploadedAt: string | null
}) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState(note ?? '')

  const upload = useMutation({
    mutationFn: (file: File) => uploadAuditorReport(year, file),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['budget', year] }),
  })

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
        <input value={draft} onChange={(e) => setDraft(e.target.value)} style={{ width: '22rem', maxWidth: '100%' }} />
      </label>
      <button disabled={saveNote.isPending || draft.trim() === (note ?? '')} onClick={() => saveNote.mutate()}>
        {saveNote.isPending ? 'Saving…' : 'Save note'}
      </button>
      {saveNote.error && <span style={{ color: '#b00' }}>{errMessage(saveNote.error)}</span>}
      <label>
        Auditor report (PDF){' '}
        <input
          type="file"
          accept="application/pdf"
          disabled={upload.isPending}
          onChange={(e) => {
            const file = e.target.files?.[0]
            if (file) upload.mutate(file)
            e.target.value = ''
          }}
        />
      </label>
      <span style={{ color: '#666' }}>
        {upload.isPending
          ? 'Uploading…'
          : auditorReportUploadedAt
            ? `uploaded ${new Date(auditorReportUploadedAt).toLocaleDateString()}, a new upload replaces it`
            : 'none uploaded yet'}
      </span>
      {upload.error && <span style={{ color: '#b00' }}>{errMessage(upload.error)}</span>}
    </div>
  )
}

function AuditorReportLink({ year }: { year: number }) {
  const download = useMutation({ mutationFn: () => downloadAuditorReport(year) })
  return (
    <>
      <a
        href={`/api/budgets/${year}/auditor-report`}
        onClick={(e) => {
          e.preventDefault()
          if (!download.isPending) download.mutate()
        }}
      >
        Auditor report (PDF)
      </a>
      {download.error && <span style={{ color: '#b00' }}>{errMessage(download.error)}</span>}
    </>
  )
}

function BudgetTable({ report, editing }: { report: BudgetReport; editing: boolean }) {
  const qc = useQueryClient()
  const narrow = useIsNarrow()
  // Category names show only in edit mode; viewers holding just view-budget
  // can't read GET /categories.
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories, enabled: editing })
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
    unbudgetedIncoming.reduce((sum, u) => sum + toCents(u.actual), 0) -
    unbudgetedOutgoing.reduce((sum, u) => sum + toCents(u.actual), 0)

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
        {/* A scroll container would pin the sticky header to itself instead of
            the page, so phones (whose layout fits the width) get none. */}
        <div style={narrow ? undefined : { overflowX: 'auto' }}>
          <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: narrow ? '0.8125rem' : '0.875rem' }}>
            <thead>
              <tr>
                {!narrow && <th style={th} />}
                <th style={th}>{narrow ? 'Budgeted (CZK)' : 'Budgeted'}</th>
                <th style={th}>{narrow ? 'Actual (CZK)' : 'Actual'}</th>
                {!narrow && <th style={th}>Note</th>}
                <th style={th}>
                  <span title={PERCENT_EXPLANATION} style={{ cursor: 'help', borderBottom: '1px dotted #666' }}>
                    {narrow ? '%' : '% of budgeted'}
                  </span>
                </th>
                {editing && !narrow && <th style={th} />}
              </tr>
            </thead>
            <tbody>
              <SectionRows title="Incomes" kind="income" lines={incomeLines} categoryNames={categoryNames} />
              <SpacerRow />
              <FigureRow label="Incomes sum" kind="income" figures={incomes} emphasis="sum" />
              <SpacerRow />

              <SectionRows title="Expenses" kind="expense" lines={expenseLines} categoryNames={categoryNames} />
              <SpacerRow />
              <FigureRow label="Expenses sum" kind="expense" figures={expenses} emphasis="sum" />
              <SpacerRow />

              <FigureRow
                label="Difference"
                kind="income"
                figures={{ budgeted: incomes.budgeted - expenses.budgeted, actual: incomes.actual - expenses.actual }}
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
                  <TableRow
                    label="Difference incl. unbudgeted"
                    labelStyle={{ ...td, textAlign: 'center' }}
                    rowStyle={{ fontWeight: 600 }}
                    budgeted={null}
                    actual={incomes.actual - expenses.actual + unbudgetedNet}
                    percent=""
                  />
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
  const columns = useColumnCount()
  return (
    <>
      <HeaderRow>
        <SectionTitle title={title} kind={kind} />
      </HeaderRow>
      {lines.length === 0 && (
        <tr>
          <td style={{ ...td, color: '#999' }} colSpan={columns}>
            No lines.
          </td>
        </tr>
      )}
      {toBlocks(lines).map((block) =>
        block.group === null ? (
          <LineRow key={block.line.id} line={block.line} kind={kind} categories={categoryNames(block.line)} />
        ) : (
          <GroupRows
            key={`group-${block.group}`}
            group={block.group}
            lines={block.lines}
            kind={kind}
            categoryNames={categoryNames}
          />
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
      figures={{ budgeted: toCents(line.budgeted), actual: toCents(line.actual) }}
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

// % = Actual / Budgeted. Colors: a line's name cell carries its kind's
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
  // The Difference row compares two balances, not two amounts of the same
  // kind — with a near-zero or negative budgeted balance the ratio reads
  // misleadingly (a good result as a negative %), so it shows none.
  const percent =
    emphasis === 'total'
      ? ''
      : figures.budgeted === 0
        ? '—'
        : `${Math.round((figures.actual / figures.budgeted) * 100)} %`

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
    <TableRow
      label={label}
      labelStyle={labelStyle}
      rowStyle={rowStyle}
      budgeted={figures.budgeted}
      actual={figures.actual}
      note={line?.note}
      percent={percent}
      actions={line && <LineActions line={line} />}
    />
  )
}

function UnbudgetedRow({ total }: { total: UnbudgetedTotal }) {
  const cents = toCents(total.actual)
  return (
    <TableRow
      label={`${categoryLabel(total.category)} (${total.direction === 'incoming' ? 'income' : 'expense'})`}
      labelStyle={td}
      rowStyle={{ color: WARNING_COLOR }}
      budgeted={null}
      actual={total.direction === 'incoming' ? cents : -cents}
      percent=""
    />
  )
}

// One row of the table. Wide screens: a single <tr> with name, Budgeted,
// Actual, Note, % (and the edit buttons in edit mode). Phones: the name row
// spanning every column — note under the name, edit buttons on its right —
// then the figures row.
function TableRow({
  label,
  labelStyle,
  rowStyle,
  budgeted,
  actual,
  note,
  percent,
  actions,
}: {
  label: ReactNode
  labelStyle: CSSProperties
  rowStyle?: CSSProperties
  // Amounts in hellers; budgeted null = no budget (unbudgeted rows), shown as —.
  budgeted: number | null
  actual: number
  note?: string | null
  percent: ReactNode
  actions?: ReactNode
}) {
  const edit = useContext(EditContext)
  const narrow = useIsNarrow()
  const budgetedText = budgeted === null ? '—' : formatCents(budgeted, narrow)
  const actualText = formatCents(actual, narrow)

  if (!narrow) {
    // The key makes React build fresh cells when the layout switches (e.g. a
    // phone rotated past 600px) instead of reusing the phone layout's first
    // cell, whose removed borderLeft/borderBottom would wipe those sides of
    // the `border` shorthand.
    return (
      <tr key="wide" style={rowStyle}>
        <td style={labelStyle}>{label}</td>
        <td style={tdNumber}>{budgetedText}</td>
        <td style={tdNumber}>{actualText}</td>
        <td style={tdNote}>{note}</td>
        <td style={tdNumber}>{percent}</td>
        {edit && <td style={tdNumber}>{actions}</td>}
      </tr>
    )
  }

  const showActions = edit !== null && actions
  // A line's name cell carries its kind's color on wide screens; spanning the
  // full width here it would look like a group/sum row, so phones show that
  // color as a stripe down the entry's left edge instead. Colored group/sum
  // rows get the same-width stripe in their own color, so every entry's text
  // starts at the same offset.
  const stripeColor = labelStyle.background ?? rowStyle?.background
  const stripe = stripeColor ? `6px solid ${stripeColor}` : undefined
  return (
    <>
      <tr style={rowStyle}>
        <td
          colSpan={3}
          style={{
            ...labelStyle,
            background: undefined,
            borderLeft: stripe,
            borderBottom: 'none',
            paddingTop: '0.6rem',
          }}
        >
          <div style={showActions ? { display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', gap: '0.5rem' } : undefined}>
            <div>
              {label}
              {note && <span style={{ display: 'block', color: '#555', fontSize: '0.75rem', fontWeight: 400 }}>{note}</span>}
            </div>
            {showActions && actions}
          </div>
        </td>
      </tr>
      <tr style={rowStyle}>
        <td style={stripe ? { ...narrowFigure, borderLeft: stripe } : narrowFigure}>{budgetedText}</td>
        <td style={narrowFigure}>{actualText}</td>
        <td style={narrowFigure}>{percent}</td>
      </tr>
    </>
  )
}

// An empty row of normal line height, no background or borders, separating
// a section's lines from its sum row, and the sums from the difference.
function SpacerRow() {
  const columns = useColumnCount()
  return (
    <tr aria-hidden="true">
      <td colSpan={columns} style={{ ...td, border: 'none' }}>
        &nbsp;
      </td>
    </tr>
  )
}

function HeaderRow({ children }: { children: ReactNode }) {
  const columns = useColumnCount()
  return (
    <tr>
      <td colSpan={columns} style={{ ...td, fontWeight: 700, paddingTop: '0.9rem', borderBottom: '2px solid #ddd' }}>
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

// Sticky so the column titles stay visible while scrolling a long budget.
// With collapsed borders a stuck cell loses its own borders, hence the
// background and the shadow standing in for the bottom border.
const th: CSSProperties = {
  position: 'sticky',
  top: 0,
  zIndex: 1,
  background: '#fff',
  boxShadow: 'inset 0 -2px 0 #bbb',
  textAlign: 'center',
  border: GRID_BORDER,
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}


const td: CSSProperties = {
  border: GRID_BORDER,
  padding: '0.35rem 0.6rem',
}

const tdNumber: CSSProperties = { ...td, textAlign: 'right', whiteSpace: 'nowrap' }

// Phones: the extra top padding on the name row and bottom padding here
// space one entry from the next.
// Narrower side padding than tdNumber so three amounts fit a phone's width.
const narrowFigure: CSSProperties = {
  ...tdNumber,
  borderTop: 'none',
  paddingLeft: '0.35rem',
  paddingRight: '0.35rem',
  paddingBottom: '0.6rem',
}

const tdNote: CSSProperties = { ...td, color: '#555', fontSize: '0.8125rem', maxWidth: 320 }
