'use strict';
// Shared daemon harness: build the binary, boot a loopback `serve` with an
// isolated HOME/DB, then mint a real viewer URL via `graph ui` (which reuses
// the running server through ensure-server and prints {status,url,repo}).
// Key detail: the UI key rides in the URL fragment (#k=...), which browsers
// never send — the spec must forward it as a Bearer token for /api/graph.
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');

const repoRoot = path.resolve(__dirname, '..', '..');
const resultsDir = path.join(repoRoot, 'test-results');
const binName = process.platform === 'win32' ? 'e2e-bin.exe' : 'e2e-bin';
const binPath = path.join(resultsDir, binName);
const statePath = path.join(resultsDir, 'daemon.json');

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.on('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close((err) => (err ? reject(err) : resolve(port)));
    });
  });
}

function buildBinary() {
  fs.mkdirSync(resultsDir, { recursive: true });
  execFileSync('go', ['build', '-o', binPath, './cmd/droids-mem'], {
    cwd: repoRoot,
    stdio: 'pipe',
  });
  return binPath;
}

async function waitForHealth(addr, timeoutMs = 15000) {
  const end = Date.now() + timeoutMs;
  for (;;) {
    try {
      const res = await fetch(`http://${addr}/healthz`);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    if (Date.now() > end) throw new Error(`daemon on ${addr} never became healthy`);
    await new Promise((r) => setTimeout(r, 200));
  }
}

function parseJSONOutput(out) {
  const i = out.indexOf('{');
  if (i < 0) throw new Error(`no JSON in command output: ${out.slice(0, 200)}`);
  return JSON.parse(out.slice(i));
}

async function startDaemon() {
  // Always rebuild: the UI is go:embed'ed, so a cached binary serves stale
  // assets after any ui/ edit. go build is incremental, so this is cheap.
  const bin = buildBinary();
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'dm-e2e-home-'));
  const db = path.join(home, 'mem.db');
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  const env = {
    ...process.env,
    DROIDS_MEM_HOME: home,
    DROIDS_MEM_DB: db,
    DROIDS_MEM_MCP_ADDR: addr,
  };
  const proc = spawn(bin, ['serve', '--addr', addr], {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let dead = false;
  proc.on('exit', () => {
    dead = true;
  });
  try {
    await waitForHealth(addr);
  } catch (err) {
    proc.kill('SIGKILL');
    throw err;
  }
  if (dead) throw new Error('daemon exited before health check passed');
  const state = { bin, home, db, addr, pid: proc.pid };
  proc.unref();
  // Keep the handle reachable for teardown via state + pid; the process
  // object itself cannot cross into teardown, so teardown signals by pid.
  startDaemon.proc = proc;
  fs.writeFileSync(statePath, JSON.stringify({ ...state, url: null }, null, 2));
  return { ...state, proc, env };
}

function mintUIUrl(state, repoDir) {
  const env = {
    ...process.env,
    DROIDS_MEM_HOME: state.home,
    DROIDS_MEM_DB: state.db,
    DROIDS_MEM_MCP_ADDR: state.addr,
  };
  const out = execFileSync(state.bin, ['graph', 'ui', '--repo', repoDir], {
    env,
    cwd: repoDir,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const parsed = parseJSONOutput(out);
  if (parsed.status !== 'ok' || !parsed.url) {
    throw new Error(`graph ui did not return a URL: ${out.slice(0, 300)}`);
  }
  return parsed.url;
}

function readState() {
  return JSON.parse(fs.readFileSync(statePath, 'utf8'));
}

module.exports = {
  repoRoot,
  resultsDir,
  binPath,
  statePath,
  fixtureDir: path.join(repoRoot, 'e2e', 'fixture'),
  buildBinary,
  startDaemon,
  mintUIUrl,
  readState,
  waitForHealth,
};
