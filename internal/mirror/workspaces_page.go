package mirror

// workspacesHTML is the workspaces page served at /workspaces. It lists
// every workspace VS Code knows about (from globalStorage storage.json)
// with a live search box; clicking a row asks the server to launch a VS
// Code window for it via the code CLI. Same idiom as the mirror page:
// one inline <style>, one inline <script> IIFE, no framework.
const workspacesHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VS Code Workspaces</title>
<style>
  /* Mobile-first: big touch targets and 16px inputs (no iOS zoom-on-focus);
     the desktop look is restored in the min-width:800px block below. */
  html, body { margin: 0; height: 100%; background: #1e1e1e; color: #d7d7d7; font: 15px Consolas, 'Courier New', monospace; }
  #bar { position: fixed; top: 0; left: 0; right: 0; background: #323233; display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 10px; z-index: 10; }
  #bar select, #q { background: #252526; color: #d7d7d7; border: 1px solid #3c3c3c; height: 36px; box-sizing: border-box; }
  #nav { min-width: 112px; font: 16px Consolas, 'Courier New', monospace; }
  #q { order: 2; flex: 1 1 100%; font: 16px Consolas, 'Courier New', monospace; padding: 0 10px; }
  #count { order: 1; color: #8a8a8a; white-space: nowrap; font-size: 13px; margin-left: auto; }
  #list { position: absolute; top: 96px; bottom: 0; left: 0; right: 0; overflow-y: auto; }
  .row { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; padding: 10px 12px; cursor: pointer; border-bottom: 1px solid #2a2a2a; touch-action: manipulation; -webkit-tap-highlight-color: transparent; }
  .row:hover { background: #2a2d2e; }
  .row:active { background: #37373d; }
  .dot { color: #73c991; }
  .name { font-size: 15px; font-weight: bold; white-space: nowrap; flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .badge { color: #75beff; white-space: nowrap; font-size: 13px; }
  /* Mobile row: name/badge/status on line 1, path on its own line. */
  .path { order: 4; color: #8a8a8a; flex: 1 1 100%; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .status { order: 3; color: #dcdcaa; white-space: nowrap; font-size: 13px; margin-left: auto; }
  .row.err .status { color: #f48771; }
  .msg { padding: 12px; color: #f48771; font-size: 14px; }
  @media (min-width: 800px) {
    html, body { font-size: 12px; }
    #bar { flex-wrap: nowrap; height: 26px; padding: 0 8px; }
    #bar select, #q { height: 20px; font-size: 12px; }
    #nav { min-width: 110px; }
    #q { order: 2; flex: 1 1 auto; padding: 0 6px; }
    #count { order: 3; margin-left: 0; }
    #list { top: 26px; }
    .row { flex-wrap: nowrap; gap: 8px; padding: 4px 10px; }
    .name { font-size: 12px; }
    .badge { font-size: 12px; }
    /* Desktop row: everything back on one line. */
    .path { order: 0; flex: 1 1 auto; font-size: 12px; }
    .status { order: 0; margin-left: 0; font-size: 12px; }
    .msg { padding: 8px 10px; font-size: 12px; }
  }
</style>
</head>
<body>
<div id="bar">
  <select id="nav">
    <option value="workspaces" selected>Workspaces</option>
    <option value="mirror">&larr; Mirror</option>
  </select>
  <input id="q" placeholder="search workspaces…" autocomplete="off">
  <span id="count"></span>
</div>
<div id="list"><div class="msg">loading…</div></div>
<script>
(function () {
  var nav = document.getElementById('nav');
  var q = document.getElementById('q');
  var list = document.getElementById('list');
  var count = document.getElementById('count');
  var all = [];

  nav.addEventListener('change', function () {
    if (nav.value === 'mirror') location.href = '/';
  });

  function esc(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // Render the rows matching the search box (case-insensitive substring
  // over name, path, and remote authority).
  function render() {
    var needle = q.value.trim().toLowerCase();
    var rows = [];
    for (var i = 0; i < all.length; i++) {
      var w = all[i];
      if (needle) {
        var hay = (w.name + ' ' + (w.path || '') + ' ' + (w.remote || '')).toLowerCase();
        if (hay.indexOf(needle) < 0) continue;
      }
      rows.push(w);
    }
    count.textContent = rows.length + ' / ' + all.length;
    if (!rows.length) {
      list.innerHTML = '<div class="msg">no match</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < rows.length; i++) {
      var w = rows[i];
      html += '<div class="row" data-uri="' + esc(w.uri) + '">' +
        '<span class="dot">' + (w.open ? '●' : '') + '</span>' +
        '<span class="name">' + esc(w.name) + '</span>' +
        (w.remote ? '<span class="badge">[' + esc(w.remote) + ']</span>' : '') +
        '<span class="path">' + esc(w.path || w.uri) + '</span>' +
        '<span class="status"></span>' +
        '</div>';
    }
    list.innerHTML = html;
  }

  q.addEventListener('input', render);

  list.addEventListener('click', function (ev) {
    var row = ev.target;
    while (row && row !== list && !(row.className === 'row')) row = row.parentElement;
    if (!row || row === list) return;
    openRow(row);
  });

  function openRow(row) {
    var st = row.querySelector('.status');
    if (st.textContent === 'opening…') return; // already in flight
    st.textContent = 'opening…';
    fetch('/api/workspaces/open', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uri: row.getAttribute('data-uri') })
    }).then(function (r) {
      return r.json().then(function (j) { return { ok: r.ok, j: j }; });
    }).then(function (res) {
      if (res.ok) {
        st.textContent = 'opened ✓';
      } else {
        row.className = 'row err';
        st.textContent = res.j.err || 'failed';
      }
    }).catch(function (e) {
      row.className = 'row err';
      st.textContent = String(e);
    });
  }

  fetch('/api/workspaces').then(function (r) {
    return r.json().then(function (j) { return { ok: r.ok, j: j }; });
  }).then(function (res) {
    if (!res.ok) {
      list.innerHTML = '<div class="msg">' + esc(res.j.err || 'failed to load workspaces') + '</div>';
      count.textContent = '';
      return;
    }
    all = res.j;
    render();
  }).catch(function (e) {
    list.innerHTML = '<div class="msg">' + esc(String(e)) + '</div>';
  });
})();
</script>
</body>
</html>
`
