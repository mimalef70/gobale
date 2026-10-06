import { defineConfig, devices } from '@playwright/test'
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  expect: { timeout: 8000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: {
    baseURL: process.env.GOBALE_E2E_URL || 'http://127.0.0.1:15173/ui/',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
  webServer: process.env.GOBALE_E2E_URL
    ? undefined
    : {
        command: 'npm run dev -- --port 15173',
        url: 'http://127.0.0.1:15173/ui/',
        reuseExistingServer: false,
      },
})
