'use strict';
// Package, symbol/flow, search and entry-point views, plus the navigation trail.
(function () {
  var TRAIL_MAX = 50;

  function shortName(q) { return q.length > 40 ? '…' + q.slice(-39) : q; }
  function where(s) { return s.file ? s.file + (s.line ? ':' + s.line : '') : ''; }

  // ---- trail: breadcrumb of visited pages, kept per browser session ----
  function loadTrail() {
    try { return JSON.parse(sessionStorage.getItem('dm.trail')) || []; } catch (e) { return []; }
  }
  function renderTrail(t) {
    var box = document.getElementById('trail');
    box.textContent = '';
    t.forEach(function (c, i) {
      box.append(i === t.length - 1 ? DM.el('span', 'cur', c.l) : DM.link(c.h, c.l));
    });
  }
  DM.trailPush = function (name, arg) {
    var labels = { map: 'Map', entry: 'Entry points', pkg: arg, sym: shortName(arg), search: 'Search: ' + arg };
    var h = location.hash || '#/';
    var t = loadTrail();
    if (!t.length || t[t.length - 1].h !== h) t.push({ l: labels[name] || name, h: h });
    t = t.slice(-TRAIL_MAX);
    sessionStorage.setItem('dm.trail', JSON.stringify(t));
    renderTrail(t);
  };

  // ---- nav: search box and entry-points link ----
  document.addEventListener('DOMContentLoaded', function () {
    var input = DM.el('input');
    input.type = 'search';
    input.placeholder = 'Search symbols (2+ chars)';
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') location.hash = '#/search/' + encodeURIComponent(input.value.trim());
    });
    document.getElementById('navx').append(DM.link('#/entry', 'Entry points'), input);
  });

  // ---- shared: a list of symbol stubs ----
  function stubList(items) {
    var ul = DM.el('ul', 'list');
    items.forEach(function (s) {
      var li = DM.el('li', null, DM.link(DM.symHash(s.qname), s.qname));
      if (s.signature && s.signature !== s.qname) li.append(DM.el('div', 'sig', s.signature));
      if (where(s)) li.append(DM.el('div', 'muted small', where(s)));
      ul.append(li);
    });
    return ul;
  }

  // ---- package: symbols, exported first ----
  DM.views.pkg = async function (ctx) {
    var d = await DM.api('package', { package: ctx.arg });
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { truncated: d.truncated });
    var syms = d.symbols.filter(function (s) { return s.exported; })
      .concat(d.symbols.filter(function (s) { return !s.exported; }));
    var ul = DM.el('ul', 'list');
    syms.forEach(function (s) {
      ul.append(DM.el('li', null,
        DM.el('span', 'kind', s.kind), ' ', DM.link(DM.symHash(s.qname), s.qname),
        s.exported ? null : DM.el('span', 'muted small', ' unexported'),
        DM.el('div', 'sig', s.signature),
        DM.el('div', 'muted small', where(s))));
    });
    ctx.main.textContent = '';
    ctx.main.append(DM.el('h2', null, d.package),
      DM.el('p', 'muted', d.truncated ? 'Showing ' + d.symbols.length + ' of ' + d.total + ' symbols.' : d.symbols.length + ' symbols.'), ul);
  };

  // ---- symbol / flow ----
  function column(title, items, depth) {
    var col = DM.el('section', 'col', DM.el('h3', null, title));
    if (!items.length) col.append(DM.el('p', 'muted small', 'none'));
    items.forEach(function (n) {
      var a = DM.link(DM.symHash(n.qname), shortName(n.qname));
      a.title = n.qname + '\n' + n.signature + '\n' + where(n);
      col.append(DM.el('div', 'nb', a));
    });
    return col;
  }

  function selector(label, opts, cur, onPick) {
    var sel = DM.el('select');
    opts.forEach(function (o) {
      var op = DM.el('option', null, o);
      op.value = o;
      op.selected = String(o) === String(cur);
      sel.append(op);
    });
    sel.addEventListener('change', function () { onPick(sel.value); });
    return DM.el('label', 'muted', label + ' ', sel);
  }

  DM.views.sym = async function (ctx) {
    var o = DM.opts;
    var d = await DM.api('symbol', { symbol: ctx.arg, direction: o.dir, depth: o.depth });
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { syntactic: d.precision === 'syntactic', carried: d.carried, truncated: d.truncated });
    if (!d.symbol) {
      ctx.main.textContent = '';
      ctx.main.append(DM.el('p', 'state', 'Multiple or no matches for "' + ctx.arg + '".'), stubList(d.matches || []));
      return;
    }
    var s = d.symbol, callers = d.callers || [], callees = d.callees || [];
    var again = function () { DM.route(); };
    var head = DM.el('div', 'toolbar',
      selector('depth', [1, 2, 3], o.depth, function (v) { o.depth = +v; again(); }),
      selector('direction', ['both', 'up', 'down'], o.dir, function (v) { o.dir = v; again(); }));
    var stats = [];
    if (d.transitive_callers != null) stats.push('transitive callers: ' + d.transitive_callers);
    if (d.callers_total) stats.push('direct callers: ' + d.callers_total + ' (list capped)');
    if (d.callees_total) stats.push('direct callees: ' + d.callees_total + ' (list capped)');
    if (d.callers_in_tests) stats.push('callers in tests: ' + d.callers_in_tests);
    if (d.callers_via_interface) stats.push('callers via interface: ' + d.callers_via_interface);

    var focus = DM.el('section', 'col focus', DM.el('h3', null, 'Focus'),
      DM.el('div', 'sig', s.signature), DM.link(DM.pkgHash(s.package), s.package),
      DM.el('div', 'muted small', where(s)));
    if (s.doc) focus.append(DM.el('p', 'small', s.doc));
    if (s.source) focus.append(DM.el('pre', null, s.source));

    var cols = DM.el('div', 'flow');
    if (o.dir !== 'down') {
      for (var k = o.depth; k >= 1; k--) {
        cols.append(column('Callers d' + k, callers.filter(function (n) { return n.depth === this; }, k)));
      }
    }
    cols.append(focus);
    if (o.dir !== 'up') {
      for (k = 1; k <= o.depth; k++) {
        cols.append(column('Callees d' + k, callees.filter(function (n) { return n.depth === this; }, k)));
      }
    }
    ctx.main.textContent = '';
    ctx.main.append(DM.el('h2', null, s.qname, ' ', DM.el('span', 'kind', s.kind)),
      DM.el('p', 'muted', stats.join(' · ')), head, cols);
  };

  // ---- search ----
  DM.views.search = async function (ctx) {
    if (ctx.arg.length < 2) return DM.note(ctx.main, 'Type at least 2 characters and press Enter.');
    var d = await DM.api('search', { q: ctx.arg });
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { truncated: d.truncated });
    ctx.main.textContent = '';
    ctx.main.append(DM.el('h2', null, 'Search: ' + ctx.arg));
    if (!d.symbols.length) return ctx.main.append(DM.el('p', 'state', 'No matches.'));
    ctx.main.append(stubList(d.symbols));
  };

  // ---- entry points ----
  DM.views.entry = async function (ctx) {
    var d = await DM.api('entrypoints');
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { truncated: d.truncated });
    ctx.main.textContent = '';
    ctx.main.append(DM.el('h2', null, 'Entry points'),
      DM.el('p', 'muted', 'heuristic — may include false roots' + (d.hint ? '. ' + d.hint : '')));
    if (!d.symbols.length) return ctx.main.append(DM.el('p', 'state', 'No entry points found.'));
    ctx.main.append(stubList(d.symbols));
  };
})();
