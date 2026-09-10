import { useAuth } from 'react-oidc-context'
import { Link, Route, Routes } from 'react-router-dom'
import { AuthGate } from './AuthGate'
import { BankAccountsPage } from './BankAccountsPage'
import { BudgetPage } from './BudgetPage'
import { CategoriesPage } from './CategoriesPage'
import { EventLogsPage } from './EventLogsPage'
import { useHasRole } from './roles'
import { TransactionsTable } from './TransactionsTable'

export function App() {
  return (
    <AuthGate>
      <Dashboard />
    </AuthGate>
  )
}

function Dashboard() {
  const auth = useAuth()
  const canManageBankAccounts = useHasRole('manage-bank-accounts')
  const canViewEventLogs = useHasRole('view-event-logs')
  const canViewBudget = useHasRole('view-budget')

  return (
    <main>
      <header
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          gap: '1rem',
          color: '#fff',
          padding: '1rem 2rem',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'baseline', gap: '1.5rem' }}>
          <h1 style={{ margin: 0, fontSize: '1.25rem' }}>Bank System — Admin</h1>
          <nav style={{ display: 'flex', gap: '1rem' }}>
            <Link to="/" style={{ color: '#fff' }}>
              Transactions
            </Link>
            <Link to="/categories" style={{ color: '#fff' }}>
              Categories
            </Link>
            {canViewBudget && (
              <Link to="/budget" style={{ color: '#fff' }}>
                Budget
              </Link>
            )}
            {canManageBankAccounts && (
              <Link to="/bank-accounts" style={{ color: '#fff' }}>
                Bank accounts
              </Link>
            )}
            {canViewEventLogs && (
              <Link to="/event-logs" style={{ color: '#fff' }}>
                Event logs
              </Link>
            )}
          </nav>
        </div>
        <span>
          {auth.user?.profile.email}{' '}
          <button onClick={() => void auth.signoutRedirect()}>Sign out</button>
        </span>
      </header>
      <div style={{ padding: '0 2rem 2rem' }}>
        <div style={{ background: '#fff', borderRadius: '8px', padding: '1.5rem' }}>
          <Routes>
            <Route path="/" element={<TransactionsTable />} />
            <Route path="/categories" element={<CategoriesPage />} />
            <Route path="/budget" element={<BudgetPage />} />
            <Route path="/bank-accounts" element={<BankAccountsPage />} />
            <Route path="/event-logs" element={<EventLogsPage />} />
          </Routes>
        </div>
      </div>
    </main>
  )
}
