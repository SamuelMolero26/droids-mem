// Playwright config for full-daemon graph-ui e2e.
// The suite boots the real loopback daemon (see e2e/global-setup.js) and
// tests map/symbol/flow against live /api/graph. No webServer: the daemon
// is owned by global setup/teardown so one server serves all specs.
const { defineConfig, devices } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.js',
  globalSetup: './e2e/global-setup.js',
  globalTeardown: './e2e/global-teardown.js',
  timeout: 30 * 1000,
  expect: { timeout: 10 * 1000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'test-results/html' }]],
  outputDir: 'test-results/results',
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
