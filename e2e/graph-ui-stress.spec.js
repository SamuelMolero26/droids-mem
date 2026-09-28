'use strict';
// Stress suite for the viewer: deep chains, truncation, interface/test
// callers, no-caller kinds, entrypoints, router edges, trail, and layout
// layout. Shares the daemon from global setup (test-results/daemon.json).
// Assertion texts below were read off the implementation and the live API —
// the UI renders short names (Add, not calc.Add), never full qnames.
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

async function qname(fragment, name) {
  const found = await api('search', { q: name });
  const hit = (found.symbols || []).find((s) => s.qname.includes(fragment));
  expect(hit, `search ${name} finds ${fragment}`).toBeTruthy();
  return hit.qname;
}

test.beforeAll(() => {
  const state = readState();
  base = `http://${state.addr}`;
  viewerUrl = state.url;
  key = viewerUrl.split('#k=')[1].split('&')[0];
});

async function boot(page, hash) {
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#${hash}`);
}

test('flow renders the full depth-3 chain around auth Login', async ({ page }) => {
  const login = await qname('auth.Service.Login', 'Login');
  await boot(page, `/flow/${encodeURIComponent(login)}`);
  await page.locator('label', { hasText: 'depth' }).locator('select').selectOption('3');
  await expect(page.locator('.flow')).toContainText('Callers d3');
  await expect(page.locator('.flow')).toContainText('main');
  await expect(page.locator('.flow')).toContainText('Run');
  await expect(page.locator('.flow')).toContainText('HandleLogin');
  await expect(page.locator('.flow')).toContainText('Callees d2');
  await expect(page.locator('.flow')).toContainText('Fetch');
});

test('flow direction selector filters to one side', async ({ page }) => {
  const login = await qname('auth.Service.Login', 'Login');
  await boot(page, `/flow/${encodeURIComponent(login)}`);
  const dir = page.locator('label', { hasText: 'direction' }).locator('select');
  await dir.selectOption('up');
  await expect(page.locator('.flow')).toContainText('Callers d1');
  await expect(page.locator('.flow')).not.toContainText('Callees');
  await dir.selectOption('down');
  await expect(page.locator('.flow')).toContainText('Callees d1');
  await expect(page.locator('.flow')).not.toContainText('Callers d1');
});

test('symbol step-up link opens the flow in the up direction', async ({ page }) => {
  const get = await qname('store.DB.Get', 'Get');
  await boot(page, `/sym/${encodeURIComponent(get)}`);
  await page.getByRole('link', { name: '↑ step up' }).click();
  await expect(page).toHaveURL(/#\/flow\//);
  await expect(page.locator('.flow')).toContainText('Callers d1');
  await expect(page.locator('.flow')).not.toContainText('Callees d1');
});

test('symbol truncates the 60-caller fanout with a showing note', async ({ page }) => {
  const target = await qname('fanout.Target', 'Target');
  await boot(page, `/sym/${encodeURIComponent(target)}`);
  await expect(page.locator('section.callers')).toContainText('Showing 50 of 60');
});

test('interface and test callers surface on store Get', async ({ page }) => {
  const get = await qname('store.DB.Get', 'Get');
  await boot(page, `/sym/${encodeURIComponent(get)}`);
  await expect(page.locator('section.callers')).toContainText('1 via interface');
  await expect(page.locator('section.callers')).toContainText('Tests');
  await expect(page.locator('section.callers')).toContainText('TestGet');
});

test('caller-less kinds explain the metric instead of listing', async ({ page }) => {
  const user = await qname('model.User', 'User');
  await boot(page, `/sym/${encodeURIComponent(user)}`);
  await expect(page.locator('section.callers')).toContainText('call-edge metric');
  await expect(page.locator('#h-calls')).toContainText('Satisfies');
  await expect(page.locator('section.calls')).toContainText('None.');
});

test('interface symbol lists its implementers', async ({ page }) => {
  const store = await qname('store.Store', 'Store');
  await boot(page, `/sym/${encodeURIComponent(store)}`);
  await expect(page.locator('#h-calls')).toContainText('Implemented by');
  await expect(page.locator('section.calls')).toContainText('DB');
});

test('package view orders exported first and marks unexported', async ({ page }) => {
  await boot(page, '/pkg/internal%2Fauth');
  const rows = page.locator('ul.list > li');
  const texts = await rows.allTextContents();
  const loginIdx = texts.findIndex((t) => t.includes('Login'));
  const unexpIdx = texts.findIndex((t) => t.includes('unexported'));
  expect(loginIdx).toBeGreaterThanOrEqual(0);
  expect(unexpIdx).toBeGreaterThan(loginIdx);
});

test('entry points list executable roots', async ({ page }) => {
  await boot(page, '/entry');
  await expect(page.locator('main h2')).toHaveText('Entry points');
  await expect(page.locator('main')).toContainText('main');
});

test('router answers unknown views and short queries honestly', async ({ page }) => {
  await boot(page, '/nope');
  await expect(page.locator('main')).toContainText('Page not found.');
  await page.goto(viewerUrl);
  await page.locator('#q').fill('A');
  await page.locator('#q').press('Enter');
  await expect(page.locator('main')).toContainText('Type at least 2 characters');
  await page.goto(`${base}/ui/#/sym/${encodeURIComponent('zzz.Nope')}`);
  await expect(page.locator('main')).toContainText('not found');
  // 'Caller' is genuinely ambiguous (60 CallerNN): the API answers 200
  // with a matches list and the flow view says so.
  await page.goto(`${base}/ui/#/flow/${encodeURIComponent('Caller')}`);
  await expect(page.locator('main')).toContainText('Multiple or no matches');
});

test('trail accumulates visits and Clear resets it', async ({ page }) => {
  const login = await qname('auth.Service.Login', 'Login');
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#/sym/${encodeURIComponent(login)}`);
  await expect(page.locator('#trail')).toContainText('Login');
  await page.goto(`${base}/ui/#/flow/${encodeURIComponent(login)}`);
  await expect(page.locator('#trail')).toContainText('Login');
  await page.locator('#clear').click();
  await expect(page.locator('#trail')).toBeEmpty();
});

// mapCards waits for every package card, then returns [name, x, y] per card.
// The map draws after an async overview fetch, so reading before the cards
// exist returns a partial layout.
async function mapCards(page) {
  const { packages } = await api('overview');
  await expect(page.locator('svg a[href^="#/pkg/"] rect.card')).toHaveCount(packages.length);
  return page.evaluate(() =>
    Array.from(document.querySelectorAll('svg a[href^="#/pkg/"]')).map((a) => {
      const r = a.querySelector('rect.card');
      return [
        decodeURIComponent(a.getAttribute('href').slice('#/pkg/'.length)),
        Number(r.getAttribute('x')),
        Number(r.getAttribute('y')),
      ];
    }),
  );
}

test('map layout is deterministic and draws callers above callees', async ({ page }) => {
  await boot(page, '/');
  const first = await mapCards(page);
  expect(first.length).toBeGreaterThanOrEqual(10);
  await page.reload();
  expect(await mapCards(page)).toEqual(first);

  const y = Object.fromEntries(first.map(([name, , cy]) => [name, cy]));
  const edges = await page.evaluate(() =>
    Array.from(document.querySelectorAll('path.edge')).map((p) => [
      p.querySelector('title').textContent,
      p.classList.contains('back'),
    ]),
  );
  expect(edges.length).toBeGreaterThan(0);
  for (const [title, back] of edges) {
    if (back) continue;
    const [from, to] = title.replace(/ \(.*$/, '').split(' → ');
    expect(y[from], `${from} above ${to}`).toBeLessThan(y[to]);
  }
});

test('map closes the python cycle with a back edge and parks the isolate', async ({ page }) => {
  await boot(page, '/');
  const cards = await mapCards(page);
  // Stroke-only SVG paths report as hidden to Playwright; assert via text.
  await expect(page.locator('path.edge.back > title')).toHaveText([/^py\.cyc_b → py\.cyc_a /]);
  await expect(page.locator('#badges')).toContainText('approximate');
  // Packages with no cross-package edges share the bottom layer.
  const maxY = Math.max(...cards.map(([, , cy]) => cy));
  const bottom = cards.filter(([, , cy]) => cy === maxY).map(([name]) => name);
  expect(bottom).toContain('internal/isolated');
  expect(cards.filter(([name]) => name.startsWith('cmd/')).every(([, , cy]) => cy < maxY)).toBe(true);
});
