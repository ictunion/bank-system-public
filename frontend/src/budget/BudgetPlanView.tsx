import { type CSSProperties, type ReactNode, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  type BudgetLineKind,
  type BudgetReport,
  type BudgetReportLine,
  fetchBudget,
  fetchBudgets,
  type UnbudgetedTotal,
} from '../api/budget'
import { categoryLabel, categoryLabelById, fetchCategories } from '../api/categories'
import { errMessage } from '../api/client'

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

export function BudgetPlanView() {
  const budgets = useQuery({ queryKey: ['budgets'], queryFn: fetchBudgets })
  const [selectedYear, setSelectedYear] = useState<number | null>(null)

  const budgetYears = (budgets.data ?? []).map((b) => b.year)
  // Default: this year if it has a budget, else the newest one there is.
  const year = selectedYear ?? (budgetYears.includes(CURRENT_YEAR) ? CURRENT_YEAR : (budgetYears[0] ?? CURRENT_YEAR))
  const yearOptions = [...new Set([CURRENT_YEAR, ...budgetYears])].sort((a, b) => b - a)
  const hasBudget = budgetYears.includes(year)

  const report = useQuery({
    queryKey: ['budget', year],
    queryFn: () => fetchBudget(year),
    enabled: hasBudget,
  })

  return (
    <section>
      <h2 style={{ marginTop: 0 }}>Budget {year}</h2>
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
        {report.data?.note && <span style={{ color: '#666' }}>{report.data.note}</span>}
      </div>

      {budgets.isPending ? (
        <p>Loading…</p>
      ) : budgets.isError ? (
        <p>Failed to load: {errMessage(budgets.error)}</p>
      ) : !hasBudget ? (
        <p style={{ color: '#666' }}>There is no budget for {year} yet.</p>
      ) : report.isPending ? (
        <p>Loading…</p>
      ) : report.isError ? (
        <p>Failed to load: {errMessage(report.error)}</p>
      ) : (
        <BudgetTable report={report.data} />
      )}
    </section>
  )
}

function BudgetTable({ report }: { report: BudgetReport }) {
  const categories = useQuery({ queryKey: ['categories'], queryFn: fetchCategories })

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
      {report.year === CURRENT_YEAR && (
        <p style={note}>
          Real as of {report.as_of}. Compared against the full-year projected amounts.
        </p>
      )}
      {report.other_currency_count > 0 && (
        <p style={warning}>
          {report.other_currency_count} non-CZK transaction(s) in {report.year} are not included in any figure.
        </p>
      )}

      <div style={{ overflowX: 'auto' }}>
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.875rem' }}>
          <thead>
            <tr>
              <th style={th} />
              <th style={thNumber}>Projected</th>
              <th style={thNumber}>Real</th>
              <th style={thNumber}>Difference</th>
              <th style={thNumber}>%</th>
            </tr>
          </thead>
          <tbody>
            <SectionRows title="Incomes" kind="income" lines={incomeLines} categoryNames={categoryNames} />
            <FigureRow label="Incomes sum" kind="income" figures={incomes} emphasis="sum" />

            <SectionRows title="Expenses" kind="expense" lines={expenseLines} categoryNames={categoryNames} />
            <FigureRow label="Expenses sum" kind="expense" figures={expenses} emphasis="sum" />

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
                  <td style={{ ...td, fontWeight: 600 }}>Difference incl. unbudgeted</td>
                  <td style={tdNumber}>—</td>
                  <td style={{ ...tdNumber, fontWeight: 600 }}>{formatCents(incomes.real - expenses.real + unbudgetedNet)}</td>
                  <td style={tdNumber} />
                  <td style={tdNumber} />
                </tr>
              </>
            )}
          </tbody>
        </table>
      </div>
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
  return (
    <>
      <HeaderRow>{title}</HeaderRow>
      {lines.length === 0 && (
        <tr>
          <td style={{ ...td, color: '#999' }} colSpan={5}>
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
      <FigureRow label={group} kind={kind} figures={sumFigures(lines)} emphasis="group" />
      {lines.map((line) => (
        <LineRow key={line.id} line={line} kind={kind} categories={categoryNames(line)} indent />
      ))}
    </>
  )
}

function LineRow({
  line,
  kind,
  categories,
  indent = false,
}: {
  line: BudgetReportLine
  kind: BudgetLineKind
  categories: string
  indent?: boolean
}) {
  return (
    <FigureRow
      label={
        <>
          {line.name}
          <span style={{ display: 'block', color: '#999', fontSize: '0.75rem' }}>
            {categories === '' ? 'no categories mapped' : categories}
          </span>
        </>
      }
      kind={kind}
      figures={{ projected: toCents(line.projected), real: toCents(line.real) }}
      indent={indent}
    />
  )
}

// Difference = Real − Projected. Bad news is red: spending more than an
// expense line's budget, or receiving less than an income line projects.
function FigureRow({
  label,
  kind,
  figures,
  emphasis,
  indent = false,
}: {
  label: ReactNode
  kind: BudgetLineKind
  figures: Figures
  emphasis?: 'group' | 'sum' | 'total'
  indent?: boolean
}) {
  const difference = figures.real - figures.projected
  const bad = kind === 'expense' ? difference > 0 : difference < 0
  const percent = figures.projected === 0 ? '—' : `${Math.round((figures.real / figures.projected) * 100)} %`

  const rowStyle: CSSProperties = {
    fontWeight: emphasis === undefined ? undefined : emphasis === 'group' ? 600 : 700,
    fontStyle: emphasis === 'group' ? 'italic' : undefined,
    background: emphasis === 'total' ? '#f3f6fb' : undefined,
  }
  return (
    <tr style={rowStyle}>
      <td style={indent ? { ...td, paddingLeft: '1.75rem' } : td}>{label}</td>
      <td style={tdNumber}>{formatCents(figures.projected)}</td>
      <td style={tdNumber}>{formatCents(figures.real)}</td>
      <td style={{ ...tdNumber, color: difference === 0 ? undefined : bad ? BAD_COLOR : GOOD_COLOR }}>
        {difference > 0 ? '+' : ''}
        {formatCents(difference)}
      </td>
      <td style={tdNumber}>{percent}</td>
    </tr>
  )
}

function UnbudgetedRow({ total }: { total: UnbudgetedTotal }) {
  const cents = toCents(total.real)
  return (
    <tr style={{ color: WARNING_COLOR }}>
      <td style={{ ...td, paddingLeft: '1.75rem' }}>
        {categoryLabel(total.category)} ({total.direction === 'incoming' ? 'income' : 'expense'})
      </td>
      <td style={tdNumber}>—</td>
      <td style={tdNumber}>{formatCents(total.direction === 'incoming' ? cents : -cents)}</td>
      <td style={tdNumber} />
      <td style={tdNumber} />
    </tr>
  )
}

function HeaderRow({ children }: { children: ReactNode }) {
  return (
    <tr>
      <td colSpan={5} style={{ ...td, fontWeight: 700, paddingTop: '0.9rem', borderBottom: '2px solid #ddd' }}>
        {children}
      </td>
    </tr>
  )
}

const BAD_COLOR = '#b00'
const GOOD_COLOR = '#080'
const WARNING_COLOR = '#8a5a00'

const note: CSSProperties = { color: '#666', fontSize: '0.875rem', margin: '0 0 0.75rem' }

const warning: CSSProperties = {
  color: WARNING_COLOR,
  background: '#fff6e0',
  borderRadius: 4,
  padding: '0.4rem 0.6rem',
  fontSize: '0.875rem',
  margin: '0 0 0.75rem',
}

const th: CSSProperties = {
  textAlign: 'left',
  borderBottom: '2px solid #ccc',
  padding: '0.4rem 0.6rem',
  whiteSpace: 'nowrap',
}

const thNumber: CSSProperties = { ...th, textAlign: 'right' }

const td: CSSProperties = {
  borderBottom: '1px solid #eee',
  padding: '0.35rem 0.6rem',
}

const tdNumber: CSSProperties = { ...td, textAlign: 'right', whiteSpace: 'nowrap' }
