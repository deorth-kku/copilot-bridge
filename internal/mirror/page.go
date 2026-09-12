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
    /* Truncate with an ellipsis on narrow viewports (e.g. the integrated
       browser panel) instead of clipping mid-glyph. */
    white-space: nowrap !important; overflow: hidden !important; text-overflow: ellipsis !important;
  }
  /* Responsive: the mirror fills the browser viewport (below the status bar)
     instead of the live pane's pixel size. The extracted subtree was laid out
     for the live pane, so re-fit it: the pane root fills #pane, the chat list
     (sized by an inline height in the live DOM) becomes a flex child filling
     the remaining space, and monaco-list rows (absolutely positioned with
     live-computed offsets/heights) reflow at the mirror's width. */
  #pane {
    position: relative; margin-top: 26px;
    width: 100%; height: calc(100vh - 26px);
    box-sizing: border-box; overflow: hidden;
  }
  /* Clicking the chat area (anything outside the chat input) moves the
     browser's focus to #pane itself (tabindex=0), and Chromium then paints
     its UA default focus ring (outline: auto) around the whole pane —
     VS Code's orange focusBorder color on desktop, white on mobile Chrome.
     Only the top edge is visible; the other edges sit at the viewport
     boundary and are clipped. Suppress the ring: the mirror needs no
     visible focus indicator. */
  #pane:focus, #pane:focus-visible { outline: none !important; }
  #pane > * { width: 100% !important; height: 100% !important; min-height: 0 !important; }
  #pane .interactive-list { height: auto !important; flex: 1 1 0% !important; min-height: 0 !important; }
  #pane .monaco-list-rows { position: static !important; transform: none !important; top: auto !important; left: auto !important; height: auto !important; overflow: visible !important; contain: none !important; }
  #pane .monaco-list-row { position: static !important; top: auto !important; height: auto !important; }
  /* Safety net: the chat find widget is hidden in the live page (visibility:hidden)
     by a rule scoped to .monaco-workbench. The #pane.monaco-workbench class below
     normally makes that rule apply, but keep an explicit hide in case it is not. */
  #pane .simple-find-part-wrapper, #pane .monaco-findInput, #pane .simple-find-part { display: none !important; }
</style>
</head>
<body>
<div id="status">connecting…</div>
<!-- #pane carries ancestor classes the extracted subtree is missing, so that
     workbench CSS rules scoped to those ancestors still match:
     - monaco-workbench: the ~1900 rules scoped to ".monaco-workbench
       <descendant>" (input box box-sizing, border-radius, background, the
       --vscode-* palette vars, pill text colors, etc.).
     - monaco-pane-view: pane-section rules, e.g.
       ".monaco-pane-view .pane > .pane-header.hidden { display: none }"
       (hides the "Chat" section header in merged-header mode) and the
       ".monaco-pane-view .pane > .pane-header" flex layout.
     It is the direct parent of the extracted pane root (the full chat pane
     container: title bar + session list + conversation + input), so
     pane.firstElementChild is the pane root and the DOM-path click mapping is
     unaffected. -->
<div id="pane" class="monaco-workbench monaco-pane-view" tabindex="0"></div>
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
      // While an IME composition is in progress, re-rendering the DOM would
      // destroy the composing input element and abort the composition, losing
      // the buffered (uncommitted) text. Defer updates until the composition
      // ends; the next state message will catch up.
      if (composing) return;
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
      if (m.html != null) {
        applyState(m);
      } else {
        // Layout/scroll-only update (e.g. the live page scrolled): no DOM
        // re-render, just keep the local scroll position in sync with the
        // live page.
        restoreScroll(m);
      }
    };
  }

  function applyState(m) {
    if (m.rootStyle) {
      var parts = m.rootStyle.split(';');
      for (var i = 0; i < parts.length; i++) {
        var idx = parts[i].indexOf(':');
        if (idx <= 0) continue;
        var p = parts[i].slice(0, idx).trim();
        var v = parts[i].slice(idx + 1).trim();
        // Responsive: never pin the pane to the live pane's pixel size.
        if (p === 'width' || p === 'height' || p === 'min-width' || p === 'min-height' || p === 'max-width' || p === 'max-height') continue;
        if (p) pane.style.setProperty(p, v);
      }
    }
    pane.innerHTML = m.html;
    // The live chat input's textarea (Monaco's ime-text-area) is readonly. In
    // the mirror it must be editable, otherwise: focusing it does not bring
    // up the mobile soft keyboard, and IME composition (e.g. Chinese pinyin)
    // never starts, so compositionend never fires.
    var box = pane.querySelector('.chat-input-container');
    if (box) {
      var inputs = box.querySelectorAll('textarea, [contenteditable="true"]');
      for (var i = 0; i < inputs.length; i++) {
        inputs[i].removeAttribute('readonly');
        inputs[i].removeAttribute('aria-hidden');
        if (inputs[i].getAttribute('tabindex') === '-1') inputs[i].setAttribute('tabindex', '0');
      }
      // Re-rendering replaces the previously focused input element and would
      // drop focus (killing the soft keyboard / IME session); restore it.
      if (inputFocusWanted) {
        var el = findInputIn(box);
        if (el) {
          inputFocusWanted = true;
          try { el.focus({ preventScroll: true }); } catch (err) { el.focus(); }
        }
      }
    }
    restoreScroll(m);
  }

  // Restore the live scroll position on the SAME element the server measured,
  // identified by its DOM path from the pane root (m.scrollPath). Path-based
  // resolution is size-independent, which the responsive layout requires:
  // the mirror is not the live pane's pixel size, so a "biggest scroller"
  // heuristic could pick a different element than the server measured.
  //
  // The live chat list is bottom-anchored: it keeps scrollTop at 0 and
  // encodes the scroll position in the rows container's negative top offset
  // (m.scroll.offset). The distance from the live visible window's bottom to
  // the content bottom is (scrollH - clientH) + offset — 0 when live is at
  // the bottom. The mirror's static layout is top-anchored, so show our own
  // content at the same distance from our own bottom instead of copying the
  // (always-zero) live scrollTop.
  function restoreScroll(m) {
    if (!m.scroll || !m.scrollPath) return;
    var el = pane.firstElementChild || pane;
    for (var i = 0; i < m.scrollPath.length; i++) {
      el = el.children[m.scrollPath[i]];
      if (!el) return;
    }
    el.scrollLeft = m.scroll.left;
    var max = el.scrollHeight - el.clientHeight;
    var target = m.scroll.top;
    if (m.scroll.offset < 0) {
      var distBottom = (m.scroll.scrollH - m.scroll.h) + m.scroll.offset;
      target = distBottom <= 0 ? max : max - distBottom;
    }
    el.scrollTop = Math.max(0, Math.min(max, target));
  }

  function paneCoords(e) {
    var r = pane.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }
  // Identify the element under the cursor by its DOM path (child indices from
  // the pane root) plus the click position as a 0..1 fraction within it. A DOM
  // path is robust to sibling additions elsewhere in the tree (e.g. the
  // conversation growing) that would shift a flat descendant index.
  function elemInfo(target, cx, cy) {
    var root = pane.firstElementChild || pane;
    var t = (target && target !== pane) ? target : root;
    var path = [];
    var node = t;
    var underRoot = (t === root);
    while (node && node !== root && node !== pane) {
      var parent = node.parentElement;
      if (!parent) break;
      path.unshift(Array.prototype.indexOf.call(parent.children, node));
      node = parent;
    }
    if (node === root) underRoot = true;
    var tr = t.getBoundingClientRect();
    var rx = tr.width > 0 ? (cx - tr.left) / tr.width : 0;
    var ry = tr.height > 0 ? (cy - tr.top) / tr.height : 0;
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

  // Mobile soft-keyboard support: the chat input in the live page is a Monaco
  // editor whose real input element is a textarea (.ime-text-area). Mobile
  // browsers only show the soft keyboard when such an element is focused, so
  // on tap we focus the mirror's copy of it (the tap is also forwarded as
  // mouse events, which focuses the live input in VS Code).
  var inputFocusWanted = false;
  var composing = false;
  function findInputIn(box) {
    if (!box) return null;
    var cands = box.querySelectorAll('textarea, [contenteditable="true"]');
    for (var i = 0; i < cands.length; i++) {
      var el = cands[i];
      var cs = window.getComputedStyle(el);
      if (cs.display !== 'none' && cs.visibility !== 'hidden') return el;
    }
    return null;
  }
  function focusInputAt(target) {
    var box = (target && target.closest) ? target.closest('.chat-input-container') : null;
    var el = findInputIn(box);
    if (!el) return false;
    inputFocusWanted = true;
    try { el.focus({ preventScroll: true }); } catch (err) { el.focus(); }
    return true;
  }

  pane.addEventListener('mousedown', function (e) {
    e.preventDefault();
    focusInputAt(e.target);
    var c = paneCoords(e);
    var ei = elemInfo(e.target, e.clientX, e.clientY);
    send({ type: 'mouse', kind: 'pressed', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail });
  });
  pane.addEventListener('mouseup', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e.target, e.clientX, e.clientY);
    send({ type: 'mouse', kind: 'released', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail });
  });
  // Hover is forwarded with the same element-identity mapping as clicks
  // (needed for the responsive layout, where pane-relative offsets no longer
  // line up with the live pane). Throttled: each forwarded hover is a CDP
  // round-trip, and hover only drives hover effects.
  var lastMoved = 0;
  pane.addEventListener('mousemove', function (e) {
    var now = Date.now();
    if (now - lastMoved < 50) return;
    lastMoved = now;
    var c = paneCoords(e);
    var ei = elemInfo(e.target, e.clientX, e.clientY);
    send({ type: 'mouse', kind: 'moved', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, buttons: e.buttons });
  });
  pane.addEventListener('wheel', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e.target, e.clientX, e.clientY);
    send({ type: 'mouse', kind: 'wheel', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, deltaX: e.deltaX, deltaY: e.deltaY, buttons: e.buttons });
  }, { passive: false });
  // Touch scrolling: touch swipes do not produce wheel events, so forward the
  // finger movement as wheel deltas and let the live page's message history
  // scroll (the mirror's DOM is a static snapshot; native scrolling of it
  // would diverge from the live page). Signs are flipped: finger up scrolls
  // the page down, matching native touch behavior.
  var touchState = null;
  var touchMoved = 0;
  pane.addEventListener('touchstart', function (e) {
    touchMoved = 0;
    if (e.touches.length !== 1) { touchState = null; return; }
    var t = e.touches[0];
    touchState = { x: t.clientX, y: t.clientY, target: e.target };
  }, { passive: true });
  pane.addEventListener('touchmove', function (e) {
    if (!touchState || e.touches.length !== 1) return;
    e.preventDefault();
    var t = e.touches[0];
    var dx = t.clientX - touchState.x;
    var dy = t.clientY - touchState.y;
    touchState.x = t.clientX;
    touchState.y = t.clientY;
    touchMoved += Math.abs(dx) + Math.abs(dy);
    if (touchMoved <= 4) return; // ignore micro-jitter
    var c = paneCoords(t);
    var ei = elemInfo(touchState.target, t.clientX, t.clientY);
    send({ type: 'mouse', kind: 'wheel', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, deltaX: -dx, deltaY: -dy, buttons: 0 });
  }, { passive: false });
  pane.addEventListener('touchend', function () { touchState = null; });
  pane.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  // Fallback focus for browsers where the mousedown focus did not stick.
  // Clicking outside the input area drops the focus flag so re-renders do not
  // steal focus back into the input. A swipe that ends as a click is ignored.
  pane.addEventListener('click', function (e) {
    if (touchMoved > 10) { touchMoved = 0; return; }
    if (!focusInputAt(e.target)) {
      inputFocusWanted = false;
      pane.focus();
    }
  });
  // Keyboard forwarding design (cross-browser IME safe):
  // - NEVER call preventDefault() on keydown/keyup. Blocking the default
  //   action prevents IME composition from starting in Firefox and breaks
  //   committing (上屏) in older Android WebViews.
  // - Printable single characters without modifiers are NOT forwarded as key
  //   events; they arrive as text via the input / compositionend events,
  //   which is the only path that works consistently across browsers.
  // - Everything else (Enter, Backspace, Tab, arrows, modifier combos) is
  //   forwarded as a raw key event.
  function isPrintableKey(e) {
    return e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey;
  }
  pane.addEventListener('keydown', function (e) {
    // Keys during composition (including Enter/space used to commit a pinyin
    // candidate) must not reach the live page, or they would trigger the
    // live send action before the composed text arrives.
    if (composing || e.isComposing) return;
    if (isPrintableKey(e)) return;
    send({ type: 'key', kind: 'down', key: e.key, code: e.code, keyCode: e.keyCode, modifiers: mods(e), text: '' });
  });
  pane.addEventListener('keyup', function (e) {
    if (composing || e.isComposing) return;
    if (isPrintableKey(e)) return;
    send({ type: 'key', kind: 'up', key: e.key, code: e.code, keyCode: e.keyCode, modifiers: mods(e) });
  });
  // Track IME composition so state updates can be deferred while it is in
  // progress (re-rendering would abort it and lose the buffered text). A
  // watchdog clears the flag if compositionend is ever lost.
  var composingTimer = null;
  pane.addEventListener('compositionstart', function () {
    composing = true;
    if (composingTimer) clearTimeout(composingTimer);
    composingTimer = setTimeout(function () { composing = false; }, 15000);
  });
  // Plain text input (regular typing, swipe keyboards, paste) arrives as an
  // input event. Forward it as text and clear the local copy so the mirror's
  // textarea does not accumulate characters (live content returns via the
  // next state sync).
  pane.addEventListener('input', function (e) {
    if (composing || (e.inputType && e.inputType.indexOf('Composition') >= 0)) return;
    var t = e.data || '';
    if (t) {
      send({ type: 'key', kind: 'insert', text: t });
      var el = e.target;
      if (el && 'value' in el) el.value = '';
    }
  });
  // Forward IME-committed text (e.g. a completed pinyin word) to the live page.
  pane.addEventListener('compositionend', function (e) {
    composing = false;
    if (composingTimer) { clearTimeout(composingTimer); composingTimer = null; }
    var el = e.target;
    // Some engines (older Android WebViews) report empty data on
    // compositionend; fall back to the textarea's local value, which holds
    // the committed text.
    var t = e.data || (el && 'value' in el ? el.value : '');
    if (t) {
      send({ type: 'key', kind: 'insert', text: t });
      if (el && 'value' in el) el.value = '';
    }
  });

  connect();
})();
</script>
</body>
</html>
`
