'use strict';
// Symbol page: Called by | detail with numbered source | Calls, from one
// request. Every string is a text node (source and docs are untrusted).
(function () {
  var TRUNC = '…[truncated]';
  var isTest = function (n) { return /_test\.go$/.test(n.file); };

  // pane builds a labeled section: header (h2 + muted count + optional action) then body.
  function pane(cls, id, title, count, action) {
    var h = DM.el('h2', null, title, count == null ? null : ' ', count == null ? null : DM.el('span', 'muted', count));
    h.id = id;
    var sec = DM.el('section', 'pane ' + cls, DM.el('div', 'pane-h', h, action));
    sec.setAttribute('aria-labelledby', id);
    return sec;
  }

  // step links open Flow in one direction; click runs before hashchange.
  function step(dir, q, text) {
    var a = DM.link(DM.hash('flow', q), text);
    a.addEventListener('click', function () { DM.opts.dir = dir; });
    return a;
  }

  // dtag prefixes a row's meta with its hop count when the page is deeper than 1.
  function dtag(n, deep) { return deep && n.depth ? 'd' + n.depth + ' · ' : ''; }

  // groupByFile: a file header with its count, then rows ordered by declaration line.
  function groupByFile(items, deep) {
    var by = new Map(), frag = document.createDocumentFragment();
    items.forEach(function (n) {
      if (!by.has(n.file)) by.set(n.file, []);
      by.get(n.file).push(n);
    });
    by.forEach(function (rows, file) {
      rows.sort(function (a, b) { return a.line - b.line; });
      frag.append(DM.el('div', 'grp-h', DM.el('span', 'path', file), DM.el('span', 'muted', rows.length)));
      rows.forEach(function (n) { frag.append(DM.row(n, dtag(n, deep) + ':' + n.line)); });
    });
    return frag;
  }

  function callersPane(d, s, deep) {
    var callers = d.callers || [];
    var sec = pane('callers', 'h-callers', 'Called by', deep ? callers.length + ' shown' : d.callers_total || callers.length, step('up', s.qname, '↑ step up'));
    if (d.callers_via_interface > 0) sec.append(DM.el('p', 'note', DM.el('span', 'badge', d.callers_via_interface + (deep ? ' direct' : '') + ' via interface')));
    if (s.kind === 'type' || s.kind === 'const' || s.kind === 'var') {
      sec.append(DM.el('p', 'note', d.hint || 'No callers.'));
      return sec;
    }
    var tests = callers.filter(isTest), prod = callers.filter(function (n) { return !isTest(n); });
    if (!callers.length && !d.callers_in_tests) sec.append(DM.el('p', 'note', 'No callers.'));
    sec.append(groupByFile(prod, deep));
    // True totals exist only at depth 1; deeper views count the rows shown.
    var nt = deep ? tests.length : d.callers_in_tests || tests.length;
    if (nt) {
      var det = DM.el('details', 'tests', DM.el('summary', null, 'Tests · ' + nt + (tests.length < nt ? ' (' + tests.length + ' shown)' : '')));
      if (tests.length) det.append(groupByFile(tests, deep));
      else det.append(DM.el('p', 'note', 'Not in the capped list'));
      sec.append(det);
    }
    if (d.callers_total) sec.append(DM.el('p', 'note', 'Showing ' + callers.length + ' of ' + d.callers_total + '; production callers first.'));
    return sec;
  }

  function callsPane(d, s, deep) {
    var rows, title, count, none, action = null;
    if (s.kind === 'interface') {
      rows = d.implementers || [];
      title = 'Implemented by';
      count = d.implementers_total != null ? d.implementers_total : rows.length;
      none = 'None.';
    } else if (s.kind === 'type') {
      rows = d.satisfies || [];
      title = 'Satisfies';
      count = rows.length;
      none = 'None.';
    } else {
      rows = d.callees || [];
      title = 'Calls';
      count = deep ? rows.length + ' shown' : d.callees_total || rows.length;
      none = 'No callees.';
      action = step('down', s.qname, 'step down ↓');
    }
    var sec = pane('calls', 'h-calls', title, count, action);
    if (!rows.length) sec.append(DM.el('p', 'note', none));
    rows.forEach(function (n) { sec.append(DM.row(n, dtag(n, deep) + n.file)); });
    if (title === 'Calls' && d.callees_total) sec.append(DM.el('p', 'note', 'Showing ' + rows.length + ' of ' + d.callees_total + '.'));
    return sec;
  }

  // sourceBlock numbers each line from the declaration; source can start above
  // the name's line (decorators), so anchor on the first line naming the symbol.
  function sourceBlock(s) {
    var src = s.source, cut = src.slice(-TRUNC.length) === TRUNC;
    if (cut) src = src.slice(0, -TRUNC.length);
    var lines = src.replace(/\n$/, '').split('\n');
    var short = DM.short(s.qname), name = short.slice(short.lastIndexOf('.') + 1);
    var k = lines.findIndex(function (l) { return l.indexOf(name) >= 0; });
    var first = Math.max(1, s.line - Math.max(k, 0));
    var box = DM.el('div', 'src'), frag = document.createDocumentFragment();
    lines.forEach(function (l, i) {
      var ln = DM.el('span', 'ln', String(first + i));
      ln.setAttribute('aria-hidden', 'true');
      frag.append(DM.el('div', 'sl', ln, l));
    });
    box.append(frag);
    return { box: box, count: cut ? null : lines.length, cut: cut };
  }

  function detailPane(d, s) {
    var h1 = DM.el('h1', null, DM.kindBadge(s.kind), DM.short(s.qname));
    h1.id = 'h-sym';
    h1.title = s.qname;
    var src = s.source ? sourceBlock(s) : null;
    var meta = [s.kind, s.file + ':' + s.line];
    if (src && src.count) meta.push(src.count + ' lines');
    var pills = DM.el('div', 'pills');
    var pill = function (t) { pills.append(DM.el('span', 'badge', t)); };
    if (s.exported) pill('exported');
    if (d.precision === 'syntactic') pill('approximate');
    if (d.carried) pill('carried');
    if (d.transitive_callers != null) pill(d.transitive_callers + ' transitive callers');
    var sec = DM.el('section', 'pane detail', h1, DM.el('p', 'meta', meta.join(' · ')),
      DM.el('p', 'meta', 'in ', DM.link(DM.hash('pkg', s.package), s.package)));
    sec.setAttribute('aria-labelledby', 'h-sym');
    if (pills.children.length) sec.append(pills);
    if (s.signature) sec.append(DM.el('pre', 'sig', s.signature));
    (s.doc ? s.doc.split(/\n\s*\n/) : []).forEach(function (p) { sec.append(DM.el('p', 'doc', p.trim())); });
    if (src) {
      var code = DM.el('section', null, DM.el('h2', 'vh', 'Source'), src.box);
      if (src.cut) code.append(DM.el('p', 'note', 'Source truncated at 8 KB — open ' + s.file + ':' + s.line));
      sec.append(DM.el('hr'), code);
    }
    return sec;
  }

  DM.views.sym = async function (ctx) {
    var o = DM.opts, deep = o.symDepth > 1;
    var d = await DM.api('symbol', { symbol: ctx.arg, direction: 'both', depth: o.symDepth });
    if (!ctx.alive()) return;
    DM.setBadges(d.freshness, { syntactic: d.precision === 'syntactic', carried: d.carried, truncated: d.truncated });
    var s = d.symbol;
    if (!s) {
      var m = d.matches || [];
      ctx.main.textContent = '';
      ctx.main.append(DM.el('p', 'state', m.length ? 'No single symbol matches "' + ctx.arg + '". ' : 'No symbol "' + ctx.arg + '" found. ',
        DM.link('#/search/' + encodeURIComponent(ctx.arg), 'Search for it')), DM.stubList(m));
      return;
    }
    DM.trailPush(s.qname, s.kind);
    // DOM order is detail, callers, calls (reading order); the grid places them.
    ctx.main.textContent = '';
    // Depth only: the three-pane layout always shows both directions.
    var bar = DM.el('div', 'toolbar', DM.selector('depth', [1, 2, 3, 4, 5], o.symDepth, function (v) { o.symDepth = +v; DM.route(); }));
    ctx.main.append(bar, DM.el('div', 'sym', detailPane(d, s), callersPane(d, s, deep), callsPane(d, s, deep)));
  };
})();
