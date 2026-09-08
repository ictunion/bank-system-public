import { useAuth } from 'react-oidc-context'
import { AuthGate } from './AuthGate'
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
        <h1 style={{ margin: 0, fontSize: '1.25rem' }}>Bank System — Admin</h1>
        <span>
          {auth.user?.profile.email}{' '}
          <button onClick={() => void auth.signoutRedirect()}>Sign out</button>
        </span>
      </header>
      <div style={{ padding: '0 2rem 2rem' }}>
        <div style={{ background: '#fff', borderRadius: '8px', padding: '1.5rem' }}>
          <TransactionsTable />
        </div>
      </div>
    </main>
  )
}
