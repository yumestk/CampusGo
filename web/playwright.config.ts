import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', workers: 1, retries: 0, timeout: 30_000,
  use: { baseURL: 'http://127.0.0.1:15173', headless: true, trace: 'off', screenshot: 'off' },
  // Traces/screenshots are opt-in: OIDC pages may contain credentials.
  webServer: { command: 'npm run dev', url: 'http://127.0.0.1:15173', reuseExistingServer: false, timeout: 30_000 },
});
