import { type User, UserManager, WebStorageStateStore } from 'oidc-client-ts'

const url = import.meta.env.VITE_KEYCLOAK_URL
const realm = import.meta.env.VITE_KEYCLOAK_REALM
const clientId = import.meta.env.VITE_KEYCLOAK_CLIENT_ID

if (!url || !realm || !clientId) {
  throw new Error(
    'Missing Keycloak env: set VITE_KEYCLOAK_URL, VITE_KEYCLOAK_REALM, VITE_KEYCLOAK_CLIENT_ID',
  )
}

// One UserManager for the app. Authorization Code + PKCE (oidc-client-ts adds
// the PKCE challenge automatically for response_type "code").
export const userManager = new UserManager({
  authority: `${url}/realms/${realm}`,
  client_id: clientId,
  redirect_uri: `${window.location.origin}/`,
  post_logout_redirect_uri: `${window.location.origin}/`,
  scope: 'openid profile email',
  response_type: 'code',

  // Tokens (access / refresh / id) are persisted in localStorage so a reload,
  // an opened link or a second tab reuses the existing session instead of
  // round-tripping through Keycloak on every page load; a redirect only
  // happens when no session is stored or it can't be refreshed. Trade-off:
  // any script running on this origin (i.e. an XSS bug) can read them — the
  // CSP is the main mitigation. The transient redirect state (PKCE verifier,
  // nonce) stays in sessionStorage (the default stateStore).
  userStore: new WebStorageStateStore({ store: window.localStorage }),

  // Refresh the access token in the background before it expires, using the
  // stored refresh token (refresh_token grant — no iframe, no silent
  // redirect URI needed).
  automaticSilentRenew: true,

  // We don't use Keycloak's session-monitoring iframe (keeps frame-src closed
  // in the CSP). A Keycloak-side logout is noticed on the next refresh or API
  // 401 instead.
  monitorSession: false,
})

const OIDC_CALLBACK_PARAMS = ['code', 'state', 'session_state', 'iss', 'error', 'error_description', 'error_uri']

interface SigninState {
  returnTo: string
}

// Passed as `state` to every signinRedirect so the page the user was on
// (path + query + hash) survives the round trip to Keycloak — redirect_uri is
// always the bare origin, so without this every login lands back on "/"
// with its query params gone.
export function signinState(): SigninState {
  const { pathname, search, hash } = window.location
  // Retrying after a failed callback starts from the callback URL itself;
  // carrying its stale code/state along would replay them on the next load.
  const params = new URLSearchParams(search)
  for (const key of OIDC_CALLBACK_PARAMS) params.delete(key)
  const query = params.toString()
  return { returnTo: `${pathname}${query ? `?${query}` : ''}${hash}` }
}

// Only same-origin relative paths; "//host" would be protocol-relative.
function safeReturnTo(state: unknown): string {
  const returnTo = (state as Partial<SigninState> | undefined)?.returnTo
  if (typeof returnTo === 'string' && returnTo.startsWith('/') && !returnTo.startsWith('//')) {
    return returnTo
  }
  return '/'
}

// After a successful login: replace the ?code=&state=&session_state= callback
// URL with the page the login started from. BrowserRouter is already mounted
// on the callback URL and doesn't observe replaceState, so a synthetic
// popstate makes it re-read the location.
export function onSigninCallback(user: User | void) {
  window.history.replaceState({}, document.title, safeReturnTo(user?.state))
  window.dispatchEvent(new PopStateEvent('popstate'))
}
