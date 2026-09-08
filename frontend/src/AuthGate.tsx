import { type ReactNode, useEffect } from 'react'
import { hasAuthParams, useAuth } from 'react-oidc-context'

// Renders children only for an authenticated user. Otherwise it kicks off the
// Keycloak redirect and shows a placeholder. Nothing behind this component —
// no data, no API calls — runs before sign-in completes.
export function AuthGate({ children }: { children: ReactNode }) {
  const auth = useAuth()

  useEffect(() => {
    // Start login exactly once: not already authenticated, not currently
    // processing the ?code callback, not mid-navigation, no prior error.
    if (
      !hasAuthParams() &&
      !auth.isAuthenticated &&
      !auth.activeNavigator &&
      !auth.isLoading &&
      !auth.error
    ) {
      void auth.signinRedirect()
    }
  }, [auth])

  if (auth.error) {
    return (
      <Screen>
        <p>Sign-in failed: {auth.error.message}</p>
        <button onClick={() => void auth.signinRedirect()}>Try again</button>
      </Screen>
    )
  }

  if (!auth.isAuthenticated) {
    return (
      <Screen>
        <p>Signing in…</p>
      </Screen>
    )
  }

  return <>{children}</>
}

function Screen({ children }: { children: ReactNode }) {
  return <main style={{ fontFamily: 'system-ui, sans-serif', padding: '2rem' }}>{children}</main>
}
