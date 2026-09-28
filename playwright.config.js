// Playwright config for full-daemon graph-ui e2e.
// webServer builds the binary and boots one real loopback daemon with an
// isolated HOME/DB for the whole run; global setup mints the viewer URL.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { defineConfig, devices } = require('@playwright/test');

// The config is re-evaluated in every worker; pin HOME in env so all of
// them agree on one dir. ponytail: fixed port, set DM_E2E_PORT on collision.
process.env.DM_E2E_HOME ??= fs.mkdtempSync(path.join(os.tmpdir(), 'dm-e2e-home-'));
const addr = `127.0.0.1:${process.env.DM_E2E_PORT ?? '17787'}`;
const bin = path.join(__dirname, 'test-results', process.platform === 'win32' ? 'e2e-bin.exe' : 'e2e-bin');
process.env.DM_E2E_ADDR = addr;
process.env.DM_E2E_BIN = bin;

module.exports = defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.js',
  globalSetup: './e2e/global-setup.js',
  timeout: 30 * 1000,
  expect: { timeout: 10 * 1000 },
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'test-results/html' }]],
  outputDir: 'test-results/results',
  webServer: {
    // Always rebuild: the UI is go:embed'ed, so a cached binary serves stale
    // assets after any ui/ edit. go build is incremental, so this is cheap.
    command: `go build -o "${bin}" ./cmd/droids-mem && exec "${bin}" serve --addr ${addr}`,
    url: `http://${addr}/healthz`,
    env: {
      DROIDS_MEM_HOME: process.env.DM_E2E_HOME,
      DROIDS_MEM_DB: path.join(process.env.DM_E2E_HOME, 'mem.db'),
      DROIDS_MEM_MCP_ADDR: addr,
    },
    reuseExistingServer: false,
    timeout: 120 * 1000,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
