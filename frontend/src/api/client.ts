import { userManager } from '../auth'

// Single entry point for API calls. Attaches the current Keycloak access token,
// and on a 401 tries one silent renew + retry before forcing a fresh sign-in.
// All paths are same-origin and relative (`/api/...`) — dev proxy and prod nginx
// both route `/api/` to the Go service.
export async function apiFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  const user = await userManager.getUser()
  if (user && !user.expired && user.access_token) {
    headers.set('Authorization', `Bearer ${user.access_token}`)
  }

  const res = await fetch(path, { ...init, headers })
  if (res.status !== 401) return res

  // Access token rejected — attempt one refresh, then retry the request once.
  try {
    const renewed = await userManager.signinSilent()
    if (renewed?.access_token) {
      headers.set('Authorization', `Bearer ${renewed.access_token}`)
      return await fetch(path, { ...init, headers })
    }
  } catch {
    // fall through to full re-auth
  }
  await userManager.signinRedirect()
  return res
}

export async function apiGet<T>(path: string): Promise<T> {
  const res = await apiFetch(path)
  if (!res.ok) {
    throw new Error(`GET ${path} → ${res.status}: ${await res.text()}`)
  }
  return (await res.json()) as T
}
