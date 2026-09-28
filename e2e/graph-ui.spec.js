'use strict';
// Full-daemon viewer e2e: boots against the real loopback daemon started by
// global setup (state in test-results/daemon.json). The viewer key rides in
// the launcher URL fragment (#k=...) and the page moves it to sessionStorage,
// so every test bootstraps once via the full URL, then navigates by hash on
// the same origin (sessionStorage survives same-origin goto).
const { test, expect } = require('@playwright/test');
const { readState } = require('./helpers/daemon');

let base;
let viewerUrl;
let key;

async function api(path, params = {}) {
  const qs = new URLSearchParams(params).toString();
  const res = await fetch(`${base}/api/graph/${path}${qs ? `?${qs}` : ''}`, {
    headers: { Authorization: `Bearer ${key}` },
  });
  if (!res.ok) throw new Error(`GET ${path} -> ${res.status}`);
  return res.json();
}

test.beforeAll(() => {
  const state = readState();
  base = `http://${state.addr}`;
  viewerUrl = state.url;
  key = viewerUrl.split('#k=')[1].split('&')[0];
  expect(key.startsWith('v1.')).toBe(true);
});

test('shell boots with tabs, search and trail', async ({ page }) => {
  await page.goto(viewerUrl);
  await expect(page).toHaveTitle(/droids-mem graph/);
  await expect(page.locator('#tab-map')).toHaveText('Map');
  await expect(page.locator('#tab-sym')).toHaveText('Symbol');
  await expect(page.locator('#tab-flow')).toHaveText('Flow');
  await expect(page.locator('#q')).toBeVisible();
  await expect(page.locator('.trailbar')).toContainText('Trail');
});

test('map shows the fixture packages', async ({ page, request }) => {
  const overview = await api('overview');
  const names = (overview.packages || []).map((p) => p.name || p);
  expect(names.length).toBeGreaterThanOrEqual(2);
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#/`);
  for (const name of names) {
    await expect(
      page.locator(`a[href="#/pkg/${encodeURIComponent(name)}"]`).first(),
    ).toBeVisible();
  }
  // Unused `request` is intentional: keeps the API/UI parity hook obvious.
  expect(request).toBeTruthy();
});

test('symbol view renders three panes with main as caller', async ({ page }) => {
  const found = await api('search', { q: 'Add' });
  const hit = (found.symbols || []).find((s) => s.qname.includes('calc.Add'));
  expect(hit).toBeTruthy();
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#/sym/${encodeURIComponent(hit.qname)}`);
  await expect(page.locator('#h-sym')).toContainText('Add');
  await expect(page.locator('#h-callers')).toContainText('Called by');
  await expect(page.locator('#h-calls')).toContainText('Calls');
  await expect(page.locator('section.callers')).toContainText('main.go');
});

test('flow view shows focus with caller and callee columns', async ({ page }) => {
  const found = await api('search', { q: 'Add' });
  const hit = (found.symbols || []).find((s) => s.qname.includes('calc.Add'));
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#/flow/${encodeURIComponent(hit.qname)}`);
  await expect(page.locator('section.focus h3')).toHaveText('Focus');
  await expect(page.locator('.flow')).toContainText('Callers d1');
  await expect(page.locator('.flow')).toContainText('Callees d1');
  await expect(page.locator('section.focus')).toContainText('Add');
  await expect(page.locator('section.focus')).toContainText('calc/calc.go:5');
});

test('search finds Add from the search box', async ({ page }) => {
  await page.goto(viewerUrl);
  await page.locator('#q').fill('Add');
  await page.locator('#q').press('Enter');
  await expect(page.locator('main h2')).toContainText('Search: Add');
  await expect(page.locator('main')).toContainText('Add');
  await expect(page.locator('main')).toContainText('calc/calc.go');
});
