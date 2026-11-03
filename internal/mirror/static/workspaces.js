
(function () {
  var nav = document.getElementById('nav');
  var q = document.getElementById('q');
  var list = document.getElementById('list');
  var count = document.getElementById('count');
  var poweroff = document.getElementById('poweroff');
  var all = [];
  var armed = false;

  nav.addEventListener('click', function () {
    location.href = '/';
  });

  // Power-off toggle: "power off at the end of the next task". The armed
  // state is server-side (the shutdown planner), so a click only asks the
  // server to flip it; the response (and the WS pushes) drive the button.
  // The button is icon-only; the armed state glows and the tooltip carries
  // the text.
  function renderPoweroff() {
    poweroff.classList.toggle('armed', armed);
    var label = armed ? '已设置：下次任务结束时关机' : '下次任务结束时关机';
    poweroff.title = label;
    poweroff.setAttribute('aria-label', label);
  }

  poweroff.addEventListener('click', function () {
    var next = !armed;
    fetch('/api/shutdown', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ armed: next })
    }).then(function (r) {
      return r.json().then(function (j) { return { ok: r.ok, j: j }; });
    }).then(function (res) {
      if (res.ok) armed = res.j.armed;
      renderPoweroff();
    }).catch(function () { /* keep the current state */ });
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
    // classList.contains, not an exact className match: a row in its error
    // state carries 'row err', and an exact match would walk straight to
    // the list and leave the row unclickable until the next render.
    while (row && row !== list && !row.classList.contains('row')) row = row.parentElement;
    if (!row || row === list) return;
    openRow(row);
  });

  function openRow(row) {
    var uri = row.getAttribute('data-uri');
    // Already open: jump this browser to the mirror of that window by its
    // CDP window id (the list carries it behind the green dot) instead of
    // launching anything — the mirror selects the window directly, no
    // workspace-URI re-resolution.
    for (var i = 0; i < all.length; i++) {
      if (all[i].uri === uri && all[i].open && all[i].window) {
        location.href = '/?win=' + encodeURIComponent(all[i].window);
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
  // once on connect, then whenever it changes (a window opening/closing,
  // a window switching folders, or a storage.json update). The open
  // flags follow the LIVE windows (server-side CDP probe), so the green
  // dots update without a refresh even when storage.json lags.
  function connectWS() {
    var proto = location.protocol === 'https:' ? 'wss' : 'ws';
    var ws = new WebSocket(proto + '://' + location.host + '/ws?page=workspaces');
    ws.onmessage = function (ev) {
      var m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === 'shutdown') {
        armed = !!m.armed;
        renderPoweroff();
        return;
      }
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
