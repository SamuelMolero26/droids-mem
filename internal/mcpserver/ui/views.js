'use strict';
// Package, flow, search and entry-point views.
(function () {
  function where(s) { return s.file ? s.file + (s.line ? ':' + s.line : '') : ''; }

  // ---- shared: a list of symbol stubs ----
  function stubList(items) {
    var ul = DM.el('ul', 'list');
    items.forEach(function (s) {
      var li = DM.el('li', null, DM.row(s, where(s)));
      if (s.signature && s.signature !== s.qname) li.append(DM.el('div', 'sig', s.signature));
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
      ul.append(DM.el('li', null, DM.row(s, where(s) + (s.exported ? '' : ' · unexported')),
        DM.el('div', 'sig', s.signature)));
    });
    ctx.main.textContent = '';
    ctx.main.append(DM.el('h2', null, d.package),
      DM.el('p', 'muted', d.truncated ? 'Showing ' + d.symbols.length + ' of ' + d.total + ' symbols.' : d.symbols.length + ' symbols.'), ul);
  };

  // ---- flow: callers | focus | callees columns ----
  function column(title, items, depth) {
    var col = DM.el('section', 'col', DM.el('h3', null, title));
    if (!items.length) col.append(DM.el('p', 'muted small', 'none'));
    items.forEach(function (n) {
      var a = DM.link(DM.flowHash(n.qname), DM.short(n.qname));
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

  DM.views.flow = async function (ctx) {
    var o = DM.opts;
    var d = await DM.api('symbol', { symbol: ctx.arg, direction: o.dir, depth: o.depth });
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { syntactic: d.precision === 'syntactic', carried: d.carried, truncated: d.truncated });
    if (!d.symbol) {
      ctx.main.textContent = '';
      ctx.main.append(DM.el('p', 'state', 'Multiple or no matches for "' + ctx.arg + '".'), stubList(d.matches || []));
      return;
    }
    DM.trailPush(d.symbol.qname, d.symbol.kind);
    var s = d.symbol, callers = d.callers || [], callees = d.callees || [];
    var again = function () { DM.route(); };
    var head = DM.el('div', 'toolbar',
      selector('depth', [1, 2, 3, 4, 5], o.depth, function (v) { o.depth = +v; again(); }),
      selector('direction', ['both', 'up', 'down'], o.dir, function (v) { o.dir = v; again(); }));
    var stats = [];
    if (d.transitive_callers != null) stats.push('transitive callers: ' + d.transitive_callers);
    if (d.callers_total) stats.push('direct callers: ' + d.callers_total + ' (list capped)');
    if (d.callees_total) stats.push('direct callees: ' + d.callees_total + ' (list capped)');
    if (d.callers_in_tests) stats.push('callers in tests: ' + d.callers_in_tests);
    if (d.callers_via_interface) stats.push('callers via interface: ' + d.callers_via_interface);

    var focus = DM.el('section', 'col focus', DM.el('h3', null, 'Focus'),
      DM.el('div', 'nb', DM.link(DM.symHash(s.qname), DM.short(s.qname))),
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
    ctx.main.append(DM.el('h2', null, s.qname, ' ', DM.el('span', 'muted', s.kind)),
      DM.el('p', 'muted', stats.join(' · ')), head, cols);
  };

  DM.stubList = stubList;

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
      DM.el('p', 'muted', d.hint));
    if (!d.symbols.length) return ctx.main.append(DM.el('p', 'state', 'No entry points found.'));
    ctx.main.append(stubList(d.symbols));
  };
})();
