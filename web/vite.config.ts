import preact from '@preact/preset-vite'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [preact()],
  server: {
    proxy: {
      '/api': 'http://localhost:8442',
    },
  },
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
  },
})
