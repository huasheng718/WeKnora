import { defineConfig, devices } from '@playwright/test'
import { validateProductionQaEnvironment } from './e2e/fixtures/qa-environment'

const port = Number(process.env.PLAYWRIGHT_PORT ?? 5177)
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${port}`
const qa = validateProductionQaEnvironment(process.env)
const backendPort = new URL(qa.backendURL).port

export default defineConfig({
  testDir: './e2e',
  globalTeardown: './e2e/qa-teardown.ts',
  fullyParallel: false,
  retries: 1,
  timeout: 120_000,
  expect: { timeout: 10_000 },
  reporter: [
    ['list'],
    ['html', { outputFolder: 'dist/playwright/report', open: 'never' }],
  ],
  outputDir: 'dist/playwright/results',
  use: {
    baseURL,
    locale: 'zh-CN',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: [
    {
      command: './e2e/run-qa-backend.sh',
      env: {
        PLAYWRIGHT_PRODUCTION_QA: '1',
        PLAYWRIGHT_QA_BACKEND_MODE: qa.mode,
        PLAYWRIGHT_QA_DATA_DIR: qa.dataDir,
        PLAYWRIGHT_BACKEND_PORT: backendPort,
      },
      url: `${qa.backendURL}/health`,
      reuseExistingServer: false,
      timeout: 180_000,
    },
    {
      command: `npm run dev -- --host 127.0.0.1 --port ${port}`,
      env: { VITE_DEV_PROXY_TARGET: qa.backendURL },
      url: baseURL,
      reuseExistingServer: false,
      timeout: 120_000,
    },
  ],
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
