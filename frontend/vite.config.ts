import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go backend listens on PORT (default 25987 — see ../.env.example). In dev,
// Vite proxies /api to it so frontend code can use same-origin relative URLs
// (`/api/...`) exactly as in production, where nginx serves this build at / and
// forwards /api/ to the service.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:25987', changeOrigin: true },
    },
  },
})
