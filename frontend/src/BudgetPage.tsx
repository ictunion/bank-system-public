import { type CSSProperties, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Cell, Legend, Pie, PieChart, ResponsiveContainer, Tooltip } from 'recharts'
import { categoryLabel } from './api/categories'
import { type CategoryTotal, fetchCategorySummary } from './api/budget'
import { errMessage } from './api/client'

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

export function BudgetPage() {
  const [year, setYear] = useState(CURRENT_YEAR)
  const [month, setMonth] = useState<number | null>(null)

  const { from, to } = dateRange(year, month)
  const { data, isPending, isError, error } = useQuery({
    queryKey: ['category-summary', from, to],
    queryFn: () => fetchCategorySummary({ from, to }),
  })

  return (
    <section>
      <h2 style={{ marginTop: 0 }}>Budget</h2>

      <div style={filterRow}>
        <label>
          Year{' '}
          <select value={year} onChange={(e) => setYear(Number(e.target.value))}>
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
            onChange={(e) => setMonth(e.target.value === '' ? null : Number(e.target.value))}
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
          <DirectionPanel title="Incoming" totals={data.incoming} />
          <DirectionPanel title="Outgoing" totals={data.outgoing} />
        </div>
      )}
    </section>
  )
}

function DirectionPanel({ title, totals }: { title: string; totals: CategoryTotal[] }) {
  const byCurrency = groupByCurrency(totals)

  return (
    <div style={{ flex: '1 1 360px', minWidth: 320 }}>
      <h3 style={{ marginBottom: '0.5rem' }}>{title}</h3>
      {byCurrency.size === 0 ? (
        <p style={{ color: '#666' }}>No data for this period.</p>
      ) : (
        [...byCurrency.entries()].map(([currency, rows]) => (
          <CategoryPie key={currency} currency={currency} totals={rows} />
        ))
      )}
    </div>
  )
}

function CategoryPie({ currency, totals }: { currency: string; totals: CategoryTotal[] }) {
  const [showTable, setShowTable] = useState(false)

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
      <button onClick={() => setShowTable((v) => !v)} style={{ fontSize: '0.8rem' }}>
        {showTable ? 'Hide' : 'Show'} breakdown
      </button>
      {showTable && (
        <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: '0.8rem', marginTop: '0.5rem' }}>
          <tbody>
            {sorted.map((t) => (
              <tr key={t.category}>
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
