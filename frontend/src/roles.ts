import { jwtDecode } from 'jwt-decode'
import { useAuth } from 'react-oidc-context'
import { clientId } from './auth'

interface AccessTokenClaims {
  resource_access?: Record<string, { roles: string[] }>
}

// UI-only convenience (show/hide nav + actions) — the backend independently
// enforces every role via handler.RequireRole regardless.
export function useHasRole(role: string): boolean {
  const auth = useAuth()
  const token = auth.user?.access_token
  if (!token) return false
  try {
    return jwtDecode<AccessTokenClaims>(token).resource_access?.[clientId]?.roles.includes(role) ?? false
  } catch {
    return false
  }
}
