'use strict';
// Playwright global setup: mint a real viewer URL via `graph ui` against the
// daemon webServer booted (it reuses it through ensure-server and prints
// {status,url,repo}). The UI key rides in the URL fragment (#k=...), which
// browsers never send, so specs forward it as a Bearer token for /api/graph.
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { statePath } = require('./helpers/daemon');

module.exports = async function globalSetup() {
  const home = process.env.DM_E2E_HOME;
  const addr = process.env.DM_E2E_ADDR;
  const fixtureDir = path.join(__dirname, 'fixture');
  const out = execFileSync(process.env.DM_E2E_BIN, ['graph', 'ui', '--repo', fixtureDir], {
    env: {
      ...process.env,
      DROIDS_MEM_HOME: home,
      DROIDS_MEM_DB: path.join(home, 'mem.db'),
      DROIDS_MEM_MCP_ADDR: addr,
    },
    cwd: fixtureDir,
    encoding: 'utf8',
  });
  const { url } = JSON.parse(out.slice(out.indexOf('{')));
  if (!/\/ui\/#k=v1\./.test(url ?? '')) {
    throw new Error(`unexpected graph ui output: ${out.slice(0, 300)}`);
  }
  fs.writeFileSync(statePath, JSON.stringify({ addr, url }, null, 2));

  // Teardown: webServer stops the daemon; drop the isolated HOME and state.
  return async () => {
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(statePath, { force: true });
  };
};
