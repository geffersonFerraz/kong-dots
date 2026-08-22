import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.js'],
  },
  server: {
    // Loopback unless KONGFLOW_BIND says otherwise, matching the compose files.
    // Set it to 0.0.0.0 to open the dev server to the network.
    host: process.env.KONGFLOW_BIND ?? '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.KONGFLOW_API ?? 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
