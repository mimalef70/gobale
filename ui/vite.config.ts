import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  base: './',
  plugins: [
    react(),
    tailwindcss(),
    {
      name: 'development-base',
      transformIndexHtml(html, context) {
        return context.server ? html.replace('__GOOMNI_UI_BASE__', '/ui') : html
      },
    },
  ],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/ui/api': { target: process.env.GOOMNI_DEV_PROXY ?? 'http://127.0.0.1:3000' },
      '/ui/auth': { target: process.env.GOOMNI_DEV_PROXY ?? 'http://127.0.0.1:3000' },
    },
  },
  build: { outDir: '../src/ui/web/dist', emptyOutDir: true, sourcemap: false },
  test: {
    environment: 'jsdom',
    setupFiles: ['./tests/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'tests/**/*.test.{ts,tsx}'],
    clearMocks: true,
    restoreMocks: true,
  },
})
