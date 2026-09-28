'use strict';
// Shared e2e state: global setup writes {addr, url} for the daemon that
// playwright.config.js webServer booted; specs read it back.
const fs = require('node:fs');
const path = require('node:path');

const statePath = path.join(__dirname, '..', '..', 'test-results', 'daemon.json');

function readState() {
  return JSON.parse(fs.readFileSync(statePath, 'utf8'));
}

module.exports = { statePath, readState };
