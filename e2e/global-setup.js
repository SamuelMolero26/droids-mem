'use strict';
// Playwright global setup: build once, boot one loopback daemon with an
// isolated HOME/DB, mint the viewer URL, persist state for specs/teardown.
const fs = require('node:fs');
const { startDaemon, mintUIUrl, fixtureDir, statePath } = require('./helpers/daemon');

module.exports = async function globalSetup() {
  const started = await startDaemon();
  const url = mintUIUrl(started, fixtureDir);
  if (!/\/ui\/#k=v1\./.test(url)) {
    throw new Error(`unexpected viewer URL shape: ${url.slice(0, 80)}`);
  }
  const state = {
    bin: started.bin,
    home: started.home,
    db: started.db,
    addr: started.addr,
    pid: started.pid,
    url,
  };
  fs.writeFileSync(statePath, JSON.stringify(state, null, 2));
  // eslint-disable-next-line no-console
  console.log(`[e2e] daemon on ${state.addr}, viewer ready`);
  return async () => {};
};
