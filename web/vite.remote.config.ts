import preact from '@preact/preset-vite'
import { resolve } from 'node:path'
import { defineConfig } from 'vite'

export default defineConfig({
  root: resolve(import.meta.dirname, '../remote-web'),
  base: '/llmbeam/',
  publicDir: resolve(import.meta.dirname, 'public'),
  plugins: [preact()],
  build: {
    outDir: resolve(import.meta.dirname, '../dist/remote-web'),
    emptyOutDir: true,
  },
})
