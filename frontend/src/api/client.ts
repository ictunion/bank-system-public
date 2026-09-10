import { userManager } from '../auth'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly body: unknown,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

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

// JSON request/response helper. Returns the parsed body (or undefined for 204),
// throws ApiError with the parsed error body on a non-2xx.
export async function apiSend<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.body = JSON.stringify(body)
    init.headers = { 'Content-Type': 'application/json' }
  }

  const res = await apiFetch(path, init)
  if (res.status === 204) return undefined as T

  const text = await res.text()
  let parsed: unknown = null
  if (text) {
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = text
    }
  }

  if (!res.ok) {
    throw new ApiError(res.status, parsed, `${method} ${path} → ${res.status}`)
  }
  return parsed as T
}

export function apiGet<T>(path: string): Promise<T> {
  return apiSend<T>('GET', path)
}

// Pull the backend's { "error": "..." } message out of an ApiError, else fall back.
export function errMessage(err: unknown): string {
  if (
    err instanceof ApiError &&
    err.body != null &&
    typeof err.body === 'object' &&
    'error' in err.body
  ) {
    return String((err.body as { error: unknown }).error)
  }
  return String(err)
}
