package mirror

// workspacesHTML is the workspaces page served at /workspaces. It lists
// every workspace VS Code knows about (from globalStorage storage.json)
// with a live search box; clicking a row asks the server to launch a VS
// Code window for it via the code CLI. Same idiom as the mirror page:
// one inline <style>, one inline <script> IIFE, no framework. The look is
// a HAND-WRITTEN replica of the VS Code (Dark Modern) UI: the page must
// render with no live VS Code window, so no workbench CSS is borrowed —
// the font stack and color values are pinned to the live theme in the CSS.
const workspacesHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VS Code Workspaces</title>
<style>
  /* Hand-written VS Code (Dark Modern) look — no workbench CSS is borrowed
     (the page must render with no live VS Code window). Values are pinned
     to the live theme: UI font "Segoe WPC"/"Segoe UI" 13px, window bg
     #181818, fg #cccccc, description #9d9d9d, inputs #313131 with a
     #3c3c3c border and #0078d4 focus, list hover #2a2d2e, selection
     #37373d, diff-green #54b054, badge #616161/#f8f8f8, error #f85149.
     Mobile-first: big touch targets and 16px inputs (no iOS zoom-on-focus);
     the desktop look is restored in the min-width:800px block below. */
  html { color-scheme: dark; }
  html, body { margin: 0; height: 100%; background: #181818; color: #cccccc; font: 13px "Segoe WPC", "Segoe UI", sans-serif; }
  #bar { position: fixed; top: 0; left: 0; right: 0; background: #181818; border-bottom: 1px solid #2b2b2b; display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; padding: 6px 10px; z-index: 10; box-sizing: border-box; }
  /* .monaco-inputbox look: 4px radius, focus border #0078d4. */
  #bar select, #q { background: #313131; color: #cccccc; border: 1px solid #3c3c3c; height: 36px; box-sizing: border-box; border-radius: 4px; }
  #bar select:focus, #q:focus { outline: none; border-color: #0078d4; }
  #q::placeholder { color: #989898; }
  #nav { min-width: 112px; font: 16px "Segoe WPC", "Segoe UI", sans-serif; padding: 0 8px; }
  #q { order: 2; flex: 1 1 100%; font: 16px "Segoe WPC", "Segoe UI", sans-serif; padding: 0 10px; }
  #count { order: 1; color: #9d9d9d; white-space: nowrap; font-size: 12px; margin-left: auto; }
  /* Mobile bar is TWO wrapped rows (picker row + full-width search row):
     6 + 36 + 6 row-gap + 36 + 6 padding + 1px border = 91px. */
  #list { position: absolute; top: 91px; bottom: 0; left: 0; right: 0; overflow-y: auto; }
  /* VS Code-style scrollbar: transparent track, translucent slider. */
  #list::-webkit-scrollbar { width: 12px; }
  #list::-webkit-scrollbar-track { background: transparent; }
  #list::-webkit-scrollbar-thumb { background: rgba(100,100,100,0.4); }
  #list::-webkit-scrollbar-thumb:hover { background: rgba(100,100,100,0.6); }
  /* Rows follow the session-list idiom: name line + muted path line. */
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; padding: 8px 12px; cursor: pointer; touch-action: manipulation; -webkit-tap-highlight-color: transparent; }
  .row:hover { background: #2a2d2e; }
  .row:active { background: #37373d; }
  /* Green dot marks an open workspace; closed rows carry no dot (and no
     reserved slot), so their names sit flush left. */
  .dot { width: 7px; height: 7px; border-radius: 50%; background: #54b054; flex: 0 0 auto; }
  .name { font-size: 13px; white-space: nowrap; flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .badge { color: #f8f8f8; background: #616161; white-space: nowrap; font-size: 11px; line-height: 15px; padding: 0 5px; border-radius: 2px; }
  .status { order: 3; color: #9d9d9d; white-space: nowrap; font-size: 12px; margin-left: auto; }
  .row.err .status { color: #f85149; }
  .path { order: 4; color: #9d9d9d; flex: 1 1 100%; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .msg { padding: 12px; color: #9d9d9d; font-size: 13px; }
  .msg.err { color: #f85149; }
  @media (min-width: 800px) {
    #bar { flex-wrap: nowrap; height: 26px; padding: 0 8px; }
    #bar select, #q { height: 20px; font-size: 12px; }
    #nav { min-width: 110px; padding: 0 6px; }
    #q { order: 2; flex: 1 1 auto; padding: 0 8px; }
    #count { order: 3; margin-left: 0; }
    #list { top: 27px; }
    .row { padding: 5px 10px; }
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
        (w.open ? '<span class="dot"></span>' : '') +
        '<span class="name">' + esc(w.name) + '</span>' +
        (w.remote ? '<span class="badge">' + esc(w.remote) + '</span>' : '') +
        '<span class="status"></span>' +
        '<span class="path">' + esc(w.path || w.uri) + '</span>' +
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
    var uri = row.getAttribute('data-uri');
    // Already open: jump this browser to the mirror of that window
    // (the mirror page asks the server which live window it is) instead
    // of launching anything.
    for (var i = 0; i < all.length; i++) {
      if (all[i].uri === uri && all[i].open) {
        location.href = '/?ws=' + encodeURIComponent(uri);
        return;
      }
    }
    var st = row.querySelector('.status');
    if (st.textContent === 'opening…') return; // already in flight
    st.textContent = 'opening…';
    fetch('/api/workspaces/open', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uri: uri })
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

  // The workspace list arrives over the WebSocket: the server sends it
  // once on connect, then after every storage.json change (opening or
  // closing a VS Code window flips the open flags), so the green dots
  // update live without a refresh.
  function connectWS() {
    var proto = location.protocol === 'https:' ? 'wss' : 'ws';
    var ws = new WebSocket(proto + '://' + location.host + '/ws?page=workspaces');
    ws.onmessage = function (ev) {
      var m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type !== 'workspaces' || !Array.isArray(m.workspaces)) return;
      all = m.workspaces;
      render();
    };
    ws.onclose = function () {
      setTimeout(connectWS, 1000);
    };
  }
  connectWS();
})();
</script>
</body>
</html>
`
