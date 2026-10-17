import { type ReactNode, useEffect, useRef } from 'react'
import { hasAuthParams, useAuth } from 'react-oidc-context'
import { signinState, userManager } from './auth'

// Renders children only for an authenticated user. Otherwise it kicks off the
// Keycloak redirect and shows a placeholder. Nothing behind this component —
// no data, no API calls — runs before sign-in completes.
export function AuthGate({ children }: { children: ReactNode }) {
  const auth = useAuth()

  const silentPending = useRef(false)
  const silentFailed = useRef(false)

  useEffect(() => {
    // Only start a login when not authenticated, not processing the ?code
    // callback, not mid-navigation, no prior error, no refresh in flight.
    if (
      hasAuthParams() ||
      auth.isAuthenticated ||
      auth.activeNavigator ||
      auth.isLoading ||
      auth.error ||
      silentPending.current
    ) {
      return
    }

    // A stored session whose access token already expired (e.g. the tab was
    // closed for a while) can usually be renewed with its refresh token, with
    // no Keycloak redirect. Called on userManager directly rather than
    // auth.signinSilent, which would surface a failure as auth.error ("Sign-in
    // failed") instead of falling back to the redirect below.
    if (auth.user?.refresh_token && !silentFailed.current) {
      silentPending.current = true
      userManager
        .signinSilent()
        .catch(() => null)
        .then((user) => {
          silentPending.current = false
          if (!user) {
            silentFailed.current = true
            void auth.signinRedirect({ state: signinState() })
          }
        })
      return
    }

    void auth.signinRedirect({ state: signinState() })
  }, [auth])

  if (auth.error) {
    return (
      <Screen>
        <p>Sign-in failed: {auth.error.message}</p>
        <button onClick={() => void auth.signinRedirect({ state: signinState() })}>Try again</button>
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
