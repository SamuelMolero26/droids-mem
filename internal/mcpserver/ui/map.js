'use strict';
// Package map: a deterministic layered layout (callers left, callees right).
(function () {
  var W = 180, H = 20, DX = 220, DY = 28, PAD = 16;

  // layout assigns a column and row to every package. Same input, same output.
  function layout(nodes, edges) {
    var names = nodes.map(function (n) { return n.name; }).sort();
    var n = names.length, idx = {};
    names.forEach(function (s, i) { idx[s] = i; });
    var out = names.map(function () { return []; });
    var deg = names.map(function () { return 0; });
    var callers = names.map(function () { return 0; });
    edges.forEach(function (e) {
      var a = idx[e.from], b = idx[e.to];
      if (a === undefined || b === undefined || a === b) return;
      out[a].push(b);
      deg[a]++; deg[b]++; callers[b]++;
    });
    out.forEach(function (l) { l.sort(function (x, y) { return x - y; }); });

    // DFS from uncalled packages first; an edge into a package still on the
    // stack closes a cycle and is drawn as a dashed back edge.
    var state = names.map(function () { return 0; }), post = [], back = {};
    function dfs(u) {
      state[u] = 1;
      out[u].forEach(function (v) {
        if (state[v] === 1) back[u + ':' + v] = true;
        else if (state[v] === 0) dfs(v);
      });
      state[u] = 2;
      post.push(u);
    }
    for (var u = 0; u < n; u++) if (callers[u] === 0) dfs(u);
    for (u = 0; u < n; u++) if (state[u] === 0) dfs(u);

    // Column = longest path from an uncalled package. Reverse postorder visits
    // every package before its (non-back-edge) callees.
    var col = names.map(function () { return 0; });
    var preds = names.map(function () { return []; });
    for (var i = n - 1; i >= 0; i--) {
      var a = post[i];
      out[a].forEach(function (b) {
        if (back[a + ':' + b]) return;
        col[b] = Math.max(col[b], col[a] + 1);
        preds[b].push(a);
      });
    }
    var maxCol = 0;
    for (u = 0; u < n; u++) if (deg[u] > 0) maxCol = Math.max(maxCol, col[u]);
    var anyLinked = deg.some(function (d) { return d > 0; });
    for (u = 0; u < n; u++) if (deg[u] === 0) col[u] = anyLinked ? maxCol + 1 : 0;

    // Row order within a column: by average caller row to limit crossings.
    var row = names.map(function () { return 0; }), cols = {};
    for (u = 0; u < n; u++) (cols[col[u]] = cols[col[u]] || []).push(u);
    Object.keys(cols).map(Number).sort(function (x, y) { return x - y; }).forEach(function (c) {
      var key = {};
      cols[c].forEach(function (v) {
        var p = preds[v];
        key[v] = p.length ? p.reduce(function (s, q) { return s + row[q]; }, 0) / p.length : Infinity;
      });
      cols[c].sort(function (x, y) { return key[x] === key[y] ? x - y : key[x] < key[y] ? -1 : 1; });
      cols[c].forEach(function (v, r) { row[v] = r; });
    });
    return { names: names, idx: idx, col: col, row: row, back: back };
  }

  function shorten(s) { return s.length > 28 ? '…' + s.slice(-27) : s; }

  function draw(d, scale, holder) {
    var L = layout(d.packages, d.edges);
    var byName = {};
    d.packages.forEach(function (p) { byName[p.name] = p; });
    var maxCol = 0, maxRow = 0;
    L.names.forEach(function (_, i) { maxCol = Math.max(maxCol, L.col[i]); maxRow = Math.max(maxRow, L.row[i]); });
    var w = PAD * 2 + maxCol * DX + W, h = PAD * 2 + maxRow * DY + H;
    var svg = DM.svg('svg', { viewBox: '0 0 ' + w + ' ' + h, width: w * scale, height: h * scale, role: 'img' });
    var pos = function (name) { return { x: PAD + L.col[L.idx[name]] * DX, y: PAD + L.row[L.idx[name]] * DY }; };

    var incident = {};
    d.edges.forEach(function (e) {
      if (L.idx[e.from] === undefined || L.idx[e.to] === undefined || e.from === e.to) return;
      var a = pos(e.from), b = pos(e.to);
      var x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2;
      var isBack = L.back[L.idx[e.from] + ':' + L.idx[e.to]];
      var path = DM.svg('path', {
        d: 'M' + x1 + ' ' + y1 + ' C' + (x1 + 60) + ' ' + y1 + ' ' + (x2 - 60) + ' ' + y2 + ' ' + x2 + ' ' + y2,
        'stroke-width': Math.min(6, 1 + Math.log2(e.calls)),
        class: 'edge' + (isBack ? ' back' : '')
      }, DM.svg('title', {}, e.from + ' → ' + e.to + ' (' + e.calls + ' calls)'));
      svg.append(path);
      (incident[e.from] = incident[e.from] || []).push(path);
      (incident[e.to] = incident[e.to] || []).push(path);
    });

    L.names.forEach(function (name) {
      var p = byName[name], at = pos(name);
      var cls = 'node' + (p.precision === 'syntactic' ? ' syn' : '') + (p.carried ? ' carried' : '');
      var g = DM.svg('a', { href: DM.pkgHash(name), class: cls },
        DM.svg('title', {}, name + ' — ' + p.symbols + ' symbols' + (p.carried ? ' (carried from earlier build)' : '')),
        DM.svg('rect', { x: at.x, y: at.y, width: W, height: H, rx: 3 }),
        DM.svg('text', { x: at.x + 6, y: at.y + 14 }, shorten(name)));
      var toggle = function (on) {
        g.classList.toggle('hl', on);
        (incident[name] || []).forEach(function (e) { e.classList.toggle('hl', on); });
      };
      g.addEventListener('mouseenter', function () { toggle(true); });
      g.addEventListener('mouseleave', function () { toggle(false); });
      svg.append(g);
    });
    holder.textContent = '';
    holder.append(svg);
  }

  DM.views.map = async function (ctx) {
    var d = await DM.api('overview');
    if (!ctx.alive()) return;
    var syn = d.packages.some(function (p) { return p.precision === 'syntactic'; });
    var carried = d.packages.some(function (p) { return p.carried; });
    DM.setBadges(d.freshness, { syntactic: syn, carried: carried, truncated: d.truncated });
    if (!d.packages.length) {
      return DM.note(ctx.main, 'No indexable symbols' + (d.freshness.empty_reason ? ' (' + d.freshness.empty_reason + ')' : '') + '.');
    }
    var scale = 1;
    var holder = DM.el('div', 'mapbox');
    var plus = DM.el('button', null, '+'), minus = DM.el('button', null, '−');
    var redraw = function () { draw(d, scale, holder); };
    plus.onclick = function () { scale = Math.min(3, scale + 0.25); redraw(); };
    minus.onclick = function () { scale = Math.max(0.25, scale - 0.25); redraw(); };
    var info = d.packages.length + ' packages, ' + d.edges.length + ' edges' +
      (d.truncated ? ' (truncated: largest packages and busiest edges only)' : '') +
      '. Callers on the left; dashed edges close a cycle. Click a package to open it.';
    ctx.main.textContent = '';
    ctx.main.append(DM.el('div', 'toolbar', minus, plus, DM.link('#/entry', 'Entry points'), DM.el('span', 'muted', info)), holder);
    redraw();
  };
})();
