import { type CSSProperties, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Cell, Legend, Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts'
import { categoryLabel } from './api/categories'
import { type CategoryTotal, fetchCategorySummary } from './api/budget'
import { errMessage } from './api/client'
import { BudgetPlanView } from './budget/BudgetPlanView'

const MONTH_NAMES = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]

const CURRENT_YEAR = new Date().getFullYear()
const YEARS = Array.from({ length: 6 }, (_, i) => CURRENT_YEAR - i)

// Validated (see the dataviz skill's palette validator) for a form where any
// two slices can sit next to each other, not just neighbors in data order —
// only the first 3 categorical hues clear the colorblind/normal-vision
// separation checks together. A 4th+ category folds into Other (gray, not a
// 4th hue) rather than pushing a 4th color into the mix.
const SLICE_COLORS = ['#2a78d6', '#eb6834', '#1baf7a']
const OTHER_COLOR = '#898781'
const MAX_SLICES = 3
const SURFACE = '#fcfcfb'

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

// null month = whole year.
function dateRange(year: number, month: number | null): { from: string; to: string } {
  if (month == null) return { from: `${year}-01-01`, to: `${year}-12-31` }
  const lastDay = new Date(year, month, 0).getDate()
  return { from: `${year}-${pad(month)}-01`, to: `${year}-${pad(month)}-${pad(lastDay)}` }
}

function formatAmount(total: string, currency: string): string {
  const n = Number(total)
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(n)
  } catch {
    return `${n.toLocaleString()} ${currency}`
  }
}

function groupByCurrency(totals: CategoryTotal[]): Map<string, CategoryTotal[]> {
  const groups = new Map<string, CategoryTotal[]>()
  for (const t of totals) {
    const group = groups.get(t.currency)
    if (group) group.push(t)
    else groups.set(t.currency, [t])
  }
  return groups
}

// Two views of the same page: Budget vs. actual (the voted yearly budget
// against real figures, the default) and Overview (category pie charts for any
// month or year). Each view has its own path (/budget/plan, /budget/overview)
// so a link opens the same view.
export function BudgetPage({ view }: { view: 'plan' | 'overview' }) {
  const navigate = useNavigate()
  const choose = (next: 'overview' | 'plan') => navigate(`/budget/${next}`)

  return (
    <>
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
        <button style={view === 'plan' ? activeTab : tab} onClick={() => choose('plan')}>
          Budget
        </button>
        <button style={view === 'overview' ? activeTab : tab} onClick={() => choose('overview')}>
          Categories overview
        </button>
      </div>
      {view === 'plan' ? <BudgetPlanView /> : <BudgetOverview />}
    </>
  )
}

function BudgetOverview() {
  // Year and month live in the URL (?year=2026&month=3, no month = whole year)
  // so a link opens the same period.
  const [searchParams, setSearchParams] = useSearchParams()
  const yearParam = Number(searchParams.get('year'))
  const year = YEARS.includes(yearParam) ? yearParam : CURRENT_YEAR
  const monthParam = Number(searchParams.get('month'))
  const month = Number.isInteger(monthParam) && monthParam >= 1 && monthParam <= 12 ? monthParam : null
  const setPeriod = (nextYear: number, nextMonth: number | null) =>
    setSearchParams(
      nextMonth === null ? { year: String(nextYear) } : { year: String(nextYear), month: String(nextMonth) },
      { replace: true },
    )
  // Shared so one click opens the breakdown under every pie at once.
  const [showBreakdown, setShowBreakdown] = useState(false)
  const toggleBreakdown = () => setShowBreakdown((v) => !v)

  const { from, to } = dateRange(year, month)
  const { data, isPending, isError, error } = useQuery({
    // Always rolled up for now: sub-categories are shown inside their parent.
    queryKey: ['category-summary', from, to],
    queryFn: () => fetchCategorySummary({ from, to, rollup: true }),
  })

  return (
    <section>
      {data && data.current_balance.length > 0 && (
        <p style={{ marginTop: 0, color: '#666' }}>
          Current balance {data.current_balance.map((b) => formatAmount(b.total, b.currency)).join(', ')}
        </p>
      )}

      <div style={filterRow}>
        <label>
          Year{' '}
          <select value={year} onChange={(e) => setPeriod(Number(e.target.value), month)}>
            {YEARS.map((y) => (
              <option key={y} value={y}>
                {y}
              </option>
            ))}
          </select>
        </label>
        <label>
          Month{' '}
          <select
            value={month ?? ''}
            onChange={(e) => setPeriod(year, e.target.value === '' ? null : Number(e.target.value))}
          >
            <option value="">Whole year</option>
            {MONTH_NAMES.map((name, i) => (
              <option key={i} value={i + 1}>
                {name}
              </option>
            ))}
          </select>
        </label>
      </div>

      {isPending ? (
        <p>Loading…</p>
      ) : isError ? (
        <p>Failed to load: {errMessage(error)}</p>
      ) : (
        <div style={{ display: 'flex', gap: '2rem', flexWrap: 'wrap' }}>
          <DirectionPanel title="Income" totals={data.incoming} showBreakdown={showBreakdown} onToggleBreakdown={toggleBreakdown} />
          <DirectionPanel title="Expenses" totals={data.outgoing} showBreakdown={showBreakdown} onToggleBreakdown={toggleBreakdown} />
        </div>
      )}
    </section>
  )
}

interface BreakdownToggle {
  showBreakdown: boolean
  onToggleBreakdown: () => void
}

function DirectionPanel({
  title,
  totals,
  ...breakdown
}: { title: string; totals: CategoryTotal[] } & BreakdownToggle) {
  const byCurrency = groupByCurrency(totals)

  return (
    <div style={{ flex: '1 1 360px', minWidth: 320 }}>
      <h3 style={{ marginBottom: '0.5rem' }}>{title}</h3>
      {byCurrency.size === 0 ? (
        <p style={{ color: '#666' }}>No data for this period.</p>
      ) : (
        [...byCurrency.entries()].map(([currency, rows]) => (
          <CategoryPie key={currency} currency={currency} totals={rows} {...breakdown} />
        ))
      )}
    </div>
  )
}

function CategoryPie({
  currency,
  totals,
  showBreakdown,
  onToggleBreakdown,
}: { currency: string; totals: CategoryTotal[] } & BreakdownToggle) {

  const sorted = useMemo(() => [...totals].sort((a, b) => Number(b.total) - Number(a.total)), [totals])
  const grandTotal = useMemo(() => sorted.reduce((sum, t) => sum + Number(t.total), 0), [sorted])

  const slices = useMemo(() => {
    const top = sorted.slice(0, MAX_SLICES).map((t) => ({
      name: categoryLabel(t.category),
      value: Number(t.total),
      color: undefined as string | undefined,
    }))
    const rest = sorted.slice(MAX_SLICES)
    if (rest.length === 0) return top
    const otherValue = rest.reduce((sum, t) => sum + Number(t.total), 0)
    return [...top, { name: `Other (${rest.length})`, value: otherValue, color: OTHER_COLOR }]
  }, [sorted])

  return (
    <div style={{ marginBottom: '1.5rem' }}>
      <p style={{ color: '#666', fontSize: '0.8rem', margin: '0 0 0.25rem' }}>
        Total: {formatAmount(String(grandTotal), currency)}
      </p>
      <ResponsiveContainer width="100%" height={280}>
        <PieChart>
          <Pie
            data={slices}
            dataKey="value"
            nameKey="name"
            cx="50%"
            cy="50%"
            outerRadius={100}
            stroke={SURFACE}
            strokeWidth={2}
          >
            {slices.map((s, i) => (
              <Cell key={s.name} fill={s.color ?? SLICE_COLORS[i]} />
            ))}
          </Pie>
          <Tooltip
            formatter={(value: number) => {
              const percent = grandTotal === 0 ? '—' : `${((value / grandTotal) * 100).toFixed(1)}%`
              return `${formatAmount(String(value), currency)} (${percent})`
            }}
          />
          <Legend />
        </PieChart>
      </ResponsiveContainer>
      <button onClick={onToggleBreakdown} style={{ fontSize: '0.8rem' }}>
        {showBreakdown ? 'Hide' : 'Show'} breakdown
      </button>
      {showBreakdown && (
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.8rem', marginTop: '0.5rem' }}>
          <tbody>
            {sorted.map((t) => (
              <tr key={t.category_id}>
                <td style={td}>{categoryLabel(t.category)}</td>
                <td style={{ ...td, textAlign: 'right' }}>{formatAmount(t.total, t.currency)}</td>
                <td style={{ ...td, textAlign: 'right', color: '#666' }}>
                  {grandTotal === 0 ? '—' : `${((Number(t.total) / grandTotal) * 100).toFixed(1)}%`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

const tab: CSSProperties = {
  padding: '0.3rem 0.8rem',
  border: '1px solid #ccc',
  borderRadius: 4,
  background: '#fff',
  cursor: 'pointer',
}

const activeTab: CSSProperties = { ...tab, background: '#2a78d6', borderColor: '#2a78d6', color: '#fff' }

const filterRow: CSSProperties = {
  display: 'flex',
  gap: '1rem',
  alignItems: 'center',
  marginBottom: '1rem',
}

const td: CSSProperties = {
  borderBottom: '1px solid #eee',
  padding: '0.25rem 0.4rem',
}
