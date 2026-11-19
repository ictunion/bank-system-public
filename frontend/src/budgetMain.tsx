import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider, useAuth } from 'react-oidc-context'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { onSigninCallback, userManager } from './auth'
import { AuthGate } from './AuthGate'
import { BudgetPage } from './BudgetPage'
import { useHasRole } from './roles'

// Members' budget site: the admin app's Budget page alone, without admin navigation.
const queryClient = new QueryClient()

function BudgetSite() {
  const auth = useAuth()
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
        <h1 style={{ margin: 0, fontSize: '1.25rem' }}>Budget</h1>
        <span>
          {auth.user?.profile.email}{' '}
          <button onClick={() => void auth.signoutRedirect()}>Sign out</button>
        </span>
      </header>
      <div style={{ padding: '0 2rem 2rem' }}>
        <div style={{ background: '#fff', borderRadius: '8px', padding: '1.5rem' }}>
          {canViewBudget ? (
            <Routes>
              <Route path="/budget/plan" element={<BudgetPage view="plan" />} />
              <Route path="/budget/overview" element={<BudgetPage view="overview" />} />
              <Route path="*" element={<Navigate to="/budget/plan" replace />} />
            </Routes>
          ) : (
            <p>You don't have access to the budget.</p>
          )}
        </div>
      </div>
    </main>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthProvider userManager={userManager} onSigninCallback={onSigninCallback}>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <AuthGate>
            <BudgetSite />
          </AuthGate>
        </BrowserRouter>
      </QueryClientProvider>
    </AuthProvider>
  </StrictMode>,
)
