import { defineConfig } from '@playwright/test';

// Browser tests run against the real application wired to local fixture proxies
// and fake public services (scripts/e2e-server.mjs). They never use the internet.
// They use the Chrome installed on the machine, so no browser is downloaded.
const channel = process.env.E2E_BROWSER_CHANNEL ?? 'chrome';

export default defineConfig({
  testDir: 'e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  outputDir: 'test-results',
  use: {
    baseURL: 'http://127.0.0.1:18080',
    channel,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    locale: 'en-US',
  },
  webServer: {
    command: 'node ../scripts/e2e-server.mjs',
    url: 'http://127.0.0.1:18080/healthz',
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
  projects: [
    { name: 'setup', testMatch: /setup\.spec\.ts/ },
    {
      name: 'desktop',
      testIgnore: /(mobile|setup)\.spec\.ts/,
      dependencies: ['setup'],
      use: { viewport: { width: 1440, height: 900 }, storageState: 'e2e/.auth/state.json' },
    },
    {
      name: 'mobile',
      testMatch: /mobile\.spec\.ts/,
      dependencies: ['desktop'],
      use: {
        viewport: { width: 375, height: 812 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true,
        storageState: 'e2e/.auth/state.json',
      },
    },
  ],
});
