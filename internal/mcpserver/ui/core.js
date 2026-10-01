'use strict';
// Shared plumbing: session bootstrap, API client, router, badges. Every view
// renders through textContent / text nodes only (CSP forbids inline script and
// style, and nothing here builds markup from strings).
var DM = window.DM = { views: {}, seq: 0, opts: { depth: 2, dir: 'both', symDepth: 1 } };

// el(tag, className, ...children): strings become text nodes, null is skipped.
function kids(e, args) {
  for (var i = 2; i < args.length; i++) {
    var k = args[i];
    if (k != null) e.append(typeof k === 'object' ? k : document.createTextNode(String(k)));
  }
  return e;
}
DM.el = function (tag, cls) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  return kids(e, arguments);
};
DM.svg = function (tag, attrs) {
  var e = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (var a in attrs) e.setAttribute(a, attrs[a]);
  return kids(e, arguments);
};

DM.link = function (hash, text, cls) {
  var a = DM.el('a', cls, text);
  a.href = hash;
  return a;
};
DM.hash = function (kind, q) { return '#/' + kind + '/' + encodeURIComponent(q); };

// Short display name. Mapper qnames are "<module>:<Container.name>", Go qnames
// are "<import path>.<Name or Recv.Method>"; the full qname stays in a title.
DM.short = function (q) {
  var i = q.lastIndexOf(':');
  if (i >= 0) return q.slice(i + 1);
  var s = q.slice(q.lastIndexOf('/') + 1), j = s.indexOf('.');
  return j >= 0 ? s.slice(j + 1) : s;
};

// Kind badge: a small outlined square with one glyph; unknown kinds show "·".
var KIND = { func: 'ƒ', method: 'm', constructor: 'm', class: 'C', interface: 'I', type: 'T', const: 'c', var: 'v' };
DM.kindBadge = function (kind) {
  var b = DM.el('span', 'kb', KIND[kind] || '·');
  b.setAttribute('role', 'img');
  b.setAttribute('aria-label', kind || 'unknown kind');
  b.title = kind || 'unknown kind';
  return b;
};

// Row: the whole row is one link to the symbol; meta is plain muted text.
DM.row = function (n, meta) {
  var a = DM.link(DM.hash('sym', n.qname), null, 'row');
  a.title = n.qname;
  a.append(DM.kindBadge(n.kind), DM.el('span', 'nm', DM.short(n.qname)), meta ? DM.el('span', 'meta', meta) : null);
  return a;
};

// ---- trail: symbols opened this browser session, most recent last ----
var TRAIL_MAX = 50;
DM.trail = function () {
  var t;
  try { t = JSON.parse(sessionStorage.getItem('dm.trail')); } catch (e) { t = null; }
  return Array.isArray(t) ? t.filter(function (c) { return c && typeof c.q === 'string'; }) : [];
};
DM.renderTrail = function (activeQ) {
  var box = document.getElementById('trail');
  box.textContent = '';
  DM.trail().forEach(function (c) {
    var a = DM.link(DM.hash('sym', c.q), null, 'chip');
    a.title = c.q;
    a.append(DM.kindBadge(c.k), DM.short(c.q));
    if (c.q === activeQ) a.setAttribute('aria-current', 'true');
    box.append(a);
  });
};
DM.trailPush = function (q, kind) {
  var t = DM.trail().filter(function (c) { return c.q !== q; });
  t.push({ q: q, k: kind });
  sessionStorage.setItem('dm.trail', JSON.stringify(t.slice(-TRAIL_MAX)));
  DM.renderTrail(q);
};

// Bootstrap: the launcher puts the key in the URL fragment. Move it into
// sessionStorage and strip the fragment so the key does not linger in history
// or get copied from the address bar.
(function bootstrap() {
  if (location.hash.indexOf('#k=') !== 0) return;
  sessionStorage.setItem('dm.key', new URLSearchParams(location.hash.slice(1)).get('k') || '');
  history.replaceState(null, '', location.pathname + location.search + '#/');
})();

DM.api = async function (path, params) {
  var key = sessionStorage.getItem('dm.key');
  if (!key) {
    var none = new Error('no session key');
    none.status = 401;
    throw none;
  }
  var qs = params ? '?' + new URLSearchParams(params) : '';
  var r = await fetch('/api/graph/' + path + qs, { headers: { Authorization: 'Bearer ' + key } });
  var body = null;
  try { body = await r.json(); } catch (x) { /* non-JSON error body */ }
  if (!r.ok) {
    var err = new Error((body && body.error) || r.statusText);
    err.status = r.status;
    throw err;
  }
  return body;
};

DM.showError = function (main, err) {
  var msg = err.message;
  if (err.status === 401) msg = 'Session expired — re-run droids-mem graph ui';
  else if (err.status === 404) msg = msg + '. If this repo has not been indexed yet, run droids-mem graph ui.';
  main.textContent = '';
  main.append(DM.el('p', 'state error', msg));
};

DM.note = function (main, text) {
  main.textContent = '';
  main.append(DM.el('p', 'state', text));
};

// setBadges renders freshness/precision signals. o: {syntactic, carried, truncated}.
DM.setBadges = function (f, o) {
  var box = document.getElementById('badges');
  box.textContent = '';
  var add = function (cls, text, title) {
    var b = DM.el('span', 'badge ' + cls, text);
    if (title) b.title = title;
    box.append(b);
  };
  if (o.syntactic) add('warn', 'approximate', 'Heuristic (syntactic) edges, not type-checked');
  if (f.stale) add('warn', 'stale', 'Sources changed since the graph was built');
  if (f.rebuilding) add('info', 'rebuilding');
  if (o.carried || f.stale_units_total) add('warn', 'carried', 'Some packages use edges carried from an earlier build');
  if (f.index_error) add('bad', 'index error', f.index_error);
  if (o.truncated) add('info', 'truncated', 'Result was capped');
};

// setStats fills the header size line from overview.stats.
DM.setStats = function (st) {
  var el = document.getElementById('stats');
  el.textContent = st.symbols + ' symbols · ' + st.edges + ' edges · ' + st.files + ' files';
  el.hidden = false;
};

DM.route = async function () {
  var h = location.hash.replace(/^#\/?/, '');
  var i = h.indexOf('/');
  var name = (i < 0 ? h : h.slice(0, i)) || 'map';
  var arg = i < 0 ? '' : decodeURIComponent(h.slice(i + 1));
  var t = ++DM.seq;
  var main = document.getElementById('view');
  document.getElementById('badges').textContent = '';
  var view = DM.views[name];
  if (!view) return DM.note(main, 'Page not found.');
  var sf = name === 'sym' || name === 'flow';
  // An empty #/sym/ or #/flow/ resolves to the last symbol in the trail.
  if (sf && !arg) {
    var trail = DM.trail();
    arg = trail.length ? trail[trail.length - 1].q : '';
    if (arg) history.replaceState(null, '', DM.hash(name, arg));
  }
  ['map', 'sym', 'flow'].forEach(function (n) {
    var tab = document.getElementById('tab-' + n);
    if (n === (sf ? name : 'map')) tab.setAttribute('aria-current', 'page');
    else tab.removeAttribute('aria-current');
  });
  DM.renderTrail(sf ? arg : '');
  if (name === 'search') document.getElementById('q').value = arg;
  if (sf && !arg) return DM.note(main, 'Search or pick a symbol from the Map.');
  DM.note(main, 'Loading…');
  try {
    await view({ main: main, arg: arg, alive: function () { return t === DM.seq; } });
  } catch (err) {
    if (t === DM.seq) DM.showError(main, err);
  }
};

document.addEventListener('DOMContentLoaded', function () {
  document.getElementById('clear').addEventListener('click', function () {
    sessionStorage.setItem('dm.trail', '[]');
    DM.renderTrail('');
  });
  // Search box: Enter searches, Escape leaves; "/" anywhere else focuses it.
  var q = document.getElementById('q');
  q.addEventListener('keydown', function (e) {
    var v = q.value.trim();
    if (e.key === 'Enter' && v) location.hash = '#/search/' + encodeURIComponent(v);
    else if (e.key === 'Escape') q.blur();
  });
  document.addEventListener('keydown', function (e) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target.isContentEditable || (e.target.closest && e.target.closest('input, select, textarea'))) return;
    e.preventDefault();
    q.focus();
    q.select();
  });
  window.addEventListener('hashchange', DM.route);
  DM.route();
});
