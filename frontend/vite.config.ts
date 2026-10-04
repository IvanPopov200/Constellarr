import process from 'node:process'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'

const apiProxyTarget = process.env.API_PROXY_TARGET ?? 'http://127.0.0.1:8080'
const frontendPort = Number(process.env.FRONTEND_PORT || '5173')

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '127.0.0.1',
    port: frontendPort,
    strictPort: true,
    proxy: {
      '/api': { target: apiProxyTarget, changeOrigin: false },
      '/healthz': { target: apiProxyTarget, changeOrigin: false },
    },
  },
})
