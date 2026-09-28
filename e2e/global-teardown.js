'use strict';
// Playwright global teardown: stop the daemon by pid, remove the isolated
// HOME so e2e state never leaks between runs.
const fs = require('node:fs');
const { statePath } = require('./helpers/daemon');

function waitExit(pid, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const end = Date.now() + timeoutMs;
    const tick = () => {
      try {
        process.kill(pid, 0);
      } catch {
        resolve();
        return;
      }
      if (Date.now() > end) resolve();
      else setTimeout(tick, 200);
    };
    tick();
  });
}

module.exports = async function globalTeardown() {
  let state;
  try {
    state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
  } catch {
    return;
  }
  try {
    process.kill(state.pid, 'SIGTERM');
    await waitExit(state.pid);
    try {
      process.kill(state.pid, 'SIGKILL');
    } catch {
      // already gone
    }
  } catch {
    // already gone
  }
  try {
    fs.rmSync(state.home, { recursive: true, force: true });
  } catch {
    // best effort
  }
  try {
    fs.rmSync(statePath, { force: true });
  } catch {
    // best effort
  }
};
