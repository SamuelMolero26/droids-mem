'use strict';
// Symbol and Flow views: column order and per-column contents, neighbor
// navigation, focus details, empty states, and the symbol detail pane
// (signature, meta, pills, numbered source, grouped callers, step links).
// Assertions are scoped to one column/pane so a name in the source block
// or another column cannot satisfy them. Expected values were read off the
// live API for e2e/fixture; the UI renders DM.short names.
const { test, expect } = require('@playwright/test');
const { readState } = require('./helpers/daemon');

const LOGIN = 'internal/auth.Service.Login';
const FETCH = 'internal/cache.Fetch';
const API_MAIN = 'cmd/api.main';

let base;
let viewerUrl;

test.beforeAll(() => {
  const state = readState();
  base = `http://${state.addr}`;
  viewerUrl = state.url;
});

async function boot(page, view, qname) {
  await page.goto(viewerUrl);
  await page.goto(`${base}/ui/#/${view}/${encodeURIComponent(qname)}`);
}

// column returns the flow column whose h3 is exactly title.
function column(page, title) {
  return page
    .locator('.flow > section.col')
    .filter({ has: page.locator('h3', { hasText: new RegExp(`^${title}$`) }) });
}

test('flow orders columns callers, focus, callees by depth', async ({ page }) => {
  await boot(page, 'flow', LOGIN);
  await expect(page.locator('.flow > section.col > h3')).toHaveText([
    'Callers d2',
    'Callers d1',
    'Focus',
    'Callees d1',
    'Callees d2',
  ]);
  await expect(column(page, 'Callers d2').locator('a')).toHaveText(['Run']);
  await expect(column(page, 'Callers d1').locator('a')).toHaveText(['HandleLogin']);
  await expect(column(page, 'Callees d1').locator('a')).toHaveText(['DB.Get']);
  await expect(column(page, 'Callees d2').locator('a')).toHaveText(['Fetch']);
  await expect(column(page, 'Callers d1').locator('a')).toHaveAttribute(
    'href',
    `#/flow/${encodeURIComponent('cmd/api.HandleLogin')}`,
  );
});

test('flow header and focus show the symbol details', async ({ page }) => {
  await boot(page, 'flow', LOGIN);
  await expect(page.locator('main h2')).toHaveText(`${LOGIN} method`);
  await expect(page.locator('main > p.muted')).toContainText('transitive callers: 3');
  const focus = column(page, 'Focus');
  await expect(focus.locator('.sig')).toHaveText('func (s Service) Login() string');
  await expect(focus.locator(`a[href="#/pkg/${encodeURIComponent('internal/auth')}"]`)).toHaveText(
    'internal/auth',
  );
  await expect(focus).toContainText('internal/auth/auth.go:17');
  await expect(focus).toContainText('Login returns the display name for the session user.');
  await expect(focus.locator('pre')).toContainText('s.Store.Get("session")');
});

test('clicking a neighbor refocuses the flow and back restores it', async ({ page }) => {
  await boot(page, 'flow', LOGIN);
  await column(page, 'Callers d1').getByRole('link', { name: 'HandleLogin' }).click();
  await expect(page).toHaveURL(new RegExp(`#/flow/${encodeURIComponent('cmd/api.HandleLogin')}$`));
  await expect(column(page, 'Focus').locator('.nb a')).toHaveText('HandleLogin');
  await expect(column(page, 'Callers d1').locator('a')).toHaveText(['Run']);
  await expect(column(page, 'Callees d1').locator('a')).toHaveText(['Service.Login']);
  await page.goBack();
  await expect(column(page, 'Focus').locator('.nb a')).toHaveText('Service.Login');
});

test('flow marks an empty side with none', async ({ page }) => {
  await boot(page, 'flow', FETCH);
  await expect(column(page, 'Callees d1')).toContainText('none');
  await expect(column(page, 'Callees d1').locator('a')).toHaveCount(0);
  await expect(column(page, 'Callers d1').locator('a')).toHaveText(['DB.Get']);
});

test('flow focus link opens the symbol page', async ({ page }) => {
  await boot(page, 'flow', LOGIN);
  await column(page, 'Focus').locator('.nb a').click();
  await expect(page).toHaveURL(new RegExp(`#/sym/${encodeURIComponent(LOGIN)}$`));
  await expect(page.locator('#h-sym')).toContainText('Service.Login');
});

test('symbol detail shows signature, meta, pills, doc and numbered source', async ({ page }) => {
  await boot(page, 'sym', LOGIN);
  const detail = page.locator('section.detail');
  await expect(page.locator('#h-sym')).toContainText('Service.Login');
  await expect(page.locator('#h-sym')).toHaveAttribute('title', LOGIN);
  await expect(detail.locator('p.meta').first()).toHaveText(
    'method · internal/auth/auth.go:17 · 3 lines',
  );
  await expect(detail.locator('.pills .badge')).toHaveText(['exported', '3 transitive callers']);
  await expect(detail.locator('pre.sig')).toHaveText('func (s Service) Login() string');
  await expect(detail.locator('p.doc')).toHaveText(
    'Login returns the display name for the session user.',
  );
  await expect(detail.locator('.src .sl .ln')).toHaveText(['17', '18', '19']);
  await expect(detail.locator('.src .sl').nth(1)).toContainText('s.Store.Get("session")');
  await expect(detail.locator(`a[href="#/pkg/${encodeURIComponent('internal/auth')}"]`)).toBeVisible();
});

test('symbol callers group by file and rows open the caller', async ({ page }) => {
  await boot(page, 'sym', LOGIN);
  const callers = page.locator('section.callers');
  await expect(callers.locator('.grp-h .path')).toHaveText(['cmd/api/main.go']);
  await expect(callers.locator('a.row .nm')).toHaveText(['HandleLogin']);
  await expect(callers.locator('a.row .meta')).toHaveText([':22']);
  const calls = page.locator('section.calls');
  await expect(calls.locator('a.row .nm')).toHaveText(['DB.Get']);
  await expect(calls.locator('a.row .meta')).toHaveText(['internal/store/store.go']);
  await callers.getByRole('link', { name: /HandleLogin/ }).click();
  await expect(page).toHaveURL(new RegExp(`#/sym/${encodeURIComponent('cmd/api.HandleLogin')}$`));
  await expect(page.locator('#h-sym')).toContainText('HandleLogin');
});

test('symbol step down opens the flow in the down direction', async ({ page }) => {
  await boot(page, 'sym', LOGIN);
  await page.getByRole('link', { name: 'step down ↓' }).click();
  await expect(page).toHaveURL(/#\/flow\//);
  await expect(page.locator('.flow > section.col > h3')).toHaveText([
    'Focus',
    'Callees d1',
    'Callees d2',
  ]);
});

test('symbol says so when a side is empty', async ({ page }) => {
  await boot(page, 'sym', FETCH);
  await expect(page.locator('section.calls')).toContainText('No callees.');
  await boot(page, 'sym', API_MAIN);
  await expect(page.locator('section.callers')).toContainText('No callers.');
});

test('symbol with an ambiguous name lists the matches', async ({ page }) => {
  await boot(page, 'sym', 'Caller');
  await expect(page.locator('main')).toContainText('No single symbol matches "Caller".');
  await expect(page.getByRole('link', { name: 'Search for it' })).toHaveAttribute(
    'href',
    '#/search/Caller',
  );
  await expect(page.locator('main ul.list > li').first()).toBeVisible();
});
