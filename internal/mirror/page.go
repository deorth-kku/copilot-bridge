package mirror

// pageHTML is the single-page mirror client. It renders the extracted pane
// HTML (styled by the extracted workbench CSS) inside #pane, forwards all
// mouse/keyboard events to the server as pane-relative coordinates, and
// re-renders whenever a new state arrives. The extracted HTML carries no
// scripts, so the mirror is purely a visual replica plus an input-capture
// surface; all real behavior happens in the live VS Code page.
const pageHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VS Code Copilot Mirror</title>
<style id="mirror-css"></style>
<style id="mirror-theme"></style>
<style>
  html, body {
    margin: 0 !important; padding: 0 !important;
    background: #1e1e1e !important;
    overflow: auto !important; height: auto !important;
  }
  #status {
    position: fixed !important; top: 0 !important; left: 0 !important; right: 0 !important;
    height: 26px !important; z-index: 9999 !important;
    background: #323233 !important; color: #d7d7d7 !important;
    font: 12px/26px monospace !important; padding: 0 10px !important;
    box-sizing: border-box !important; border-bottom: 1px solid #444 !important;
  }
  #pane { position: relative; margin-top: 26px; overflow: hidden; }
  /* Safety net: the chat find widget is hidden in the live page (visibility:hidden)
     by a rule scoped to .monaco-workbench. The #pane.monaco-workbench class below
     normally makes that rule apply, but keep an explicit hide in case it is not. */
  #pane .simple-find-part-wrapper, #pane .monaco-findInput, #pane .simple-find-part { display: none !important; }
</style>
</head>
<body>
<div id="status">connecting…</div>
<!-- #pane carries the monaco-workbench class so that the ~1900 workbench CSS
     rules scoped to ".monaco-workbench <descendant>" (input box box-sizing,
     border-radius, background, the --vscode-* palette vars, pill text colors,
     etc.) match the extracted subtree. It is the direct parent of the extracted
     .interactive-session root, so pane.firstElementChild stays .interactive-session
     and the DOM-path click mapping is unaffected. -->
<div id="pane" class="monaco-workbench" tabindex="0"></div>
<script>
(function () {
  var status = document.getElementById('status');
  var pane = document.getElementById('pane');
  var cssEl = document.getElementById('mirror-css');
  var themeEl = document.getElementById('mirror-theme');
  var ws = null;
  var lastCSSVer = '';
  var lastThemeVer = '';

  function setStatus(t) { status.textContent = t; }

  function connect() {
    var proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(proto + '://' + location.host + '/ws');
    ws.onopen = function () { setStatus('connected'); };
    ws.onclose = function () { setStatus('disconnected, retrying…'); setTimeout(connect, 1000); };
    ws.onmessage = function (ev) {
      var m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type !== 'state') return;
      if (m.err) { setStatus('! ' + m.err); return; }
      setStatus('mirroring: ' + (m.window || 'window') + (m.html ? '' : ' (layout)'));
      if (m.cssVersion !== lastCSSVer) {
        lastCSSVer = m.cssVersion || '';
        if (m.css != null) cssEl.textContent = m.css;
      }
      if (m.themeVer !== lastThemeVer) {
        lastThemeVer = m.themeVer || '';
        if (m.themeVars != null) themeEl.textContent = '#pane { ' + m.themeVars + ' }';
      }
      if (m.html != null) applyState(m);
    };
  }

  function applyState(m) {
    pane.style.width = m.rect.width + 'px';
    pane.style.height = m.rect.height + 'px';
    if (m.rootStyle) {
      var parts = m.rootStyle.split(';');
      for (var i = 0; i < parts.length; i++) {
        var idx = parts[i].indexOf(':');
        if (idx <= 0) continue;
        var p = parts[i].slice(0, idx).trim();
        var v = parts[i].slice(idx + 1).trim();
        if (p) pane.style.setProperty(p, v);
      }
    }
    pane.innerHTML = m.html;
    restoreScroll(m);
  }

  // Best-effort restore of the pane's inner scroll position after a re-render.
  function restoreScroll(m) {
    if (!m.scroll) return;
    var all = pane.querySelectorAll('*');
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var ov = window.getComputedStyle(el).overflowY;
      if ((ov === 'auto' || ov === 'scroll') && el.scrollHeight > el.clientHeight) {
        el.scrollLeft = m.scroll.left;
        el.scrollTop = m.scroll.top;
        return;
      }
    }
  }

  function paneCoords(e) {
    var r = pane.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }
  // Identify the element under the cursor by its DOM path (child indices from
  // the pane root) plus the click position as a 0..1 fraction within it. A DOM
  // path is robust to sibling additions elsewhere in the tree (e.g. the
  // conversation growing) that would shift a flat descendant index.
  function elemInfo(e) {
    var root = pane.firstElementChild || pane;
    var target = (e.target && e.target !== pane) ? e.target : root;
    var path = [];
    var node = target;
    var underRoot = (target === root);
    while (node && node !== root && node !== pane) {
      var parent = node.parentElement;
      if (!parent) break;
      path.unshift(Array.prototype.indexOf.call(parent.children, node));
      node = parent;
    }
    if (node === root) underRoot = true;
    var tr = target.getBoundingClientRect();
    var rx = tr.width > 0 ? (e.clientX - tr.left) / tr.width : 0;
    var ry = tr.height > 0 ? (e.clientY - tr.top) / tr.height : 0;
    if (rx < 0) rx = 0; else if (rx > 1) rx = 1;
    if (ry < 0) ry = 0; else if (ry > 1) ry = 1;
    return { path: underRoot ? path : null, relX: rx, relY: ry };
  }
  function mods(e) {
    var m = 0;
    if (e.altKey) m |= 1;
    if (e.ctrlKey) m |= 2;
    if (e.metaKey) m |= 4;
    if (e.shiftKey) m |= 8;
    return m;
  }
  function btn(e) {
    return e.button === 0 ? 'left' : e.button === 1 ? 'middle' : e.button === 2 ? 'right' : 'left';
  }
  function send(o) {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify(o));
  }

  pane.addEventListener('mousedown', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e);
    send({ type: 'mouse', kind: 'pressed', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail });
  });
  pane.addEventListener('mouseup', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e);
    send({ type: 'mouse', kind: 'released', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail });
  });
  pane.addEventListener('mousemove', function (e) {
    var c = paneCoords(e);
    send({ type: 'mouse', kind: 'moved', x: c.x, y: c.y, buttons: e.buttons });
  });
  pane.addEventListener('wheel', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e);
    send({ type: 'mouse', kind: 'wheel', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, deltaX: e.deltaX, deltaY: e.deltaY, buttons: e.buttons });
  }, { passive: false });
  pane.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  pane.addEventListener('click', function () { pane.focus(); });
  pane.addEventListener('keydown', function (e) {
    e.preventDefault();
    send({ type: 'key', kind: 'down', key: e.key, code: e.code, keyCode: e.keyCode, modifiers: mods(e), text: e.key.length === 1 ? e.key : '' });
  });
  pane.addEventListener('keyup', function (e) {
    e.preventDefault();
    send({ type: 'key', kind: 'up', key: e.key, code: e.code, keyCode: e.keyCode, modifiers: mods(e) });
  });
  pane.addEventListener('paste', function (e) {
    e.preventDefault();
    var t = (e.clipboardData && e.clipboardData.getData('text')) || '';
    if (t) send({ type: 'key', kind: 'insert', text: t });
  });

  connect();
})();
</script>
</body>
</html>
`
