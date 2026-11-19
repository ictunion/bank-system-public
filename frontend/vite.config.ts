import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// No @types/node in this project; only env lookup is needed here.
declare const process: { env: Record<string, string | undefined> }

// `BUDGET_SITE=1 vite` serves the members' budget site (budget.html) for every
// page navigation, as nginx's budget vhost does in production. Without it the
// dev server falls back to index.html (the admin app).
function budgetEntryFallback(): Plugin {
  return {
    name: 'budget-entry-fallback',
    configureServer(server) {
      server.middlewares.use((incoming, _response, next) => {
        // Without @types/node, IncomingMessage is typed as an empty stub.
        const request = incoming as unknown as {
          method?: string
          url?: string
          headers: { accept?: string }
        }
        if (request.method === 'GET' && request.headers.accept?.includes('text/html')) {
          request.url = '/budget.html'
        }
        next()
      })
    },
  }
}

// The Go backend listens on PORT (default 25987 — see ../.env.example). In dev,
// Vite proxies /api to it so frontend code can use same-origin relative URLs
// (`/api/...`) exactly as in production, where nginx serves this build at / and
// forwards /api/ to the service.
export default defineConfig({
  plugins: [react(), ...(process.env.BUDGET_SITE ? [budgetEntryFallback()] : [])],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:25987', changeOrigin: true },
    },
  },
  build: {
    // Both sites ship in one dist/: index.html (admin) and budget.html (members).
    rollupOptions: {
      input: { main: 'index.html', budget: 'budget.html' },
    },
  },
})
