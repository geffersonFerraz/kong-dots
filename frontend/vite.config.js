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
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.KONGDOTS_API ?? 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
