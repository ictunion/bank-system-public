import { InMemoryWebStorage, UserManager, WebStorageStateStore } from 'oidc-client-ts'

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

  // Tokens (access / refresh / id) are held in memory only: a full page reload
  // drops them and the app re-runs signinRedirect(), which is prompt-less while
  // the Keycloak SSO session is alive. This keeps tokens out of
  // local/sessionStorage where injected script could read them. The transient
  // redirect state (PKCE verifier, nonce) still needs to survive the navigation
  // to Keycloak and back, so it stays in sessionStorage (the default
  // stateStore) — it is not a bearer token and expires in seconds.
  userStore: new WebStorageStateStore({ store: new InMemoryWebStorage() }),

  // Refresh the access token in the background before it expires, using the
  // in-memory refresh token (refresh_token grant — no iframe, no silent
  // redirect URI needed).
  automaticSilentRenew: true,

  // We don't use Keycloak's session-monitoring iframe (keeps frame-src closed
  // in the CSP). A reload re-checks the session via the redirect instead.
  monitorSession: false,
})

// Strip ?code=&state=&session_state= from the URL bar after a successful login.
export function onSigninCallback() {
  window.history.replaceState({}, document.title, window.location.pathname)
}
