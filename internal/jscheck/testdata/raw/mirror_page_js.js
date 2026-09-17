
(function () {
  var status = document.getElementById('status');
  var pane = document.getElementById('pane');
  var cssEl = document.getElementById('mirror-css');
  var themeEl = document.getElementById('mirror-theme');
  var ws = null;
  var lastCSSVer = '';
  var lastThemeVer = '';
  var lastWinVer = '';
  // Last live scroll state (m.scroll: live scroller's h/scrollH/offset) and
  // the DOM path of the scroller the server measured (m.scrollPath). Used to
  // mirror the live-drawn scrollbar's position/size onto the mirror scroller.
  var lastScroll = null;
  var lastScrollPath = null;
  // Last live nested-scroll state (m.nestedScrolls): the reasoning-trace
  // list of each .chat-thinking-box. Used to restore the list's scrollTop
  // (VS Code pins it to the bottom while streaming) and to redraw its
  // self-drawn slider, which arrives with live inline geometry.
  var lastNestedScrolls = null;
  // The scroller element last anchored by restoreScroll, the window it
  // belonged to, and the live viewport it was aligned to (v0/v1 in
  // full-content coordinates). A state message that did NOT move the live
  // viewport (streaming growth, nested-scroll changes) must not yank the
  // user's local reading position back to the anchor: the current scrollTop
  // is kept (the browser clamps it to the new content height). Re-anchoring
  // happens when the live viewport moved (a pane resize changes v1 too), on
  // the first state, on a window switch, or when the patch replaced the
  // scroller node.
  var lastAnchoredEl = null;
  var lastAnchoredWin = null;
  var lastV0 = null;
  var lastV1 = null;
  // Per-path record of the live nested-scroll state last applied by
  // restoreNestedScrolls ({ el, top, scrollH, clientH }). A state message
  // that did not change the live state of a nested container (the
  // reasoning-trace list) must not re-apply it: the user may have scrolled
  // that container locally, and updates elsewhere in the pane would yank
  // the reading position away. Same preserve rule as restoreScroll.
  var lastNestedApplied = null;

  // The status bar is a <select> (window picker). setStatus replaces its
  // options with a single placeholder (connecting / error states).
  function setStatus(t) {
    status.length = 0;
    var o = document.createElement('option');
    o.textContent = t;
    status.appendChild(o);
    // The placeholder destroyed the real options; force syncWindows to
    // rebuild them on the next state (otherwise the ver-unchanged early
    // return would leave the picker stuck on the placeholder, e.g. after
    // a server restart).
    lastWinVer = '';
  }

  // Rebuild the picker options only when the window set changes — state
  // messages arrive on every poll while content streams, and rebuilding on
  // each one would close an open dropdown mid-selection. Otherwise just keep
  // the selection following the server's current window.
  function syncWindows(m) {
    var wins = m.windows || [];
    var ver = '';
    for (var i = 0; i < wins.length; i++) ver += wins[i].id + ':' + wins[i].title + ';';
    if (ver === lastWinVer) {
      if (m.windowId) status.value = m.windowId;
      return;
    }
    lastWinVer = ver;
    var dup = {};
    for (var i = 0; i < wins.length; i++) dup[wins[i].title] = (dup[wins[i].title] || 0) + 1;
    status.length = 0;
    for (var i = 0; i < wins.length; i++) {
      var o = document.createElement('option');
      o.value = wins[i].id;
      // Disambiguate same-titled windows with a short id suffix.
      o.textContent = wins[i].title + (dup[wins[i].title] > 1 ? ' (' + String(wins[i].id).slice(-6) + ')' : '');
      status.appendChild(o);
    }
    if (m.windowId) status.value = m.windowId;
  }

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
      if (m.scroll) lastScroll = m.scroll;
      lastScrollPath = m.scrollPath || null;
      lastNestedScrolls = m.nestedScrolls || null;
      syncWindows(m);
      if (m.cssVersion !== lastCSSVer) {
        lastCSSVer = m.cssVersion || '';
        if (m.css != null) cssEl.textContent = m.css;
      }
      if (m.themeVer !== lastThemeVer) {
        lastThemeVer = m.themeVer || '';
        if (m.themeVars != null) themeEl.textContent = ':root { --mirror-bg: ' + (m.themeBg || '#1e1e1e') + '; } #pane { ' + m.themeVars + ' }';
      }
      if (m.html != null) {
        applyState(m);
      } else {
        // Layout/scroll-only update (e.g. the live page scrolled): no DOM
        // re-render, just keep the local scroll position in sync with the
        // live page.
        restoreScroll(m);
        restoreNestedScrolls(m);
        syncDrawnScrollbars();
      }
      updatePopup(m);
      syncCursor(m);
    };
  }

  // ---- Incremental DOM patching ------------------------------------
  // A new state's HTML is parsed into a detached <template> tree and diffed
  // against the live pane DOM instead of pane.innerHTML = html. Matching
  // nodes are updated in place (attributes, text), mismatching subtrees are
  // replaced, surplus old children are removed, new children are appended.
  // Unchanged subtrees are never touched, so the browser only re-lays-out
  // and repaints what actually changed, and the focused input element
  // survives the update (no soft-keyboard flicker, no IME interruption).
  //
  // Children are matched by position. That is optimal for the chat pane,
  // where updates are in-place text changes (streaming) and appends at the
  // bottom (new messages). A mid-list insertion degrades gracefully: from
  // the insertion point on, subtrees are replaced wholesale — still correct,
  // just less efficient.
  function parseFragment(html) {
    var tpl = document.createElement('template');
    tpl.innerHTML = html;
    return tpl.content.firstElementChild;
  }

  function patchNode(oldEl, newEl) {
    if (oldEl.nodeType !== newEl.nodeType ||
        (oldEl.nodeType === 1 && oldEl.nodeName !== newEl.nodeName)) {
      // Different kind of node: swap the whole subtree. newEl is detached
      // (inside the template), so this is a move, not a clone.
      oldEl.replaceWith(newEl);
      return;
    }
    if (oldEl.nodeType !== 1) {
      // Text/comment node: update the content in place.
      if (oldEl.data !== newEl.data) oldEl.data = newEl.data;
      return;
    }
    syncAttrs(oldEl, newEl);
    patchChildren(oldEl, newEl);
  }

  function syncAttrs(oldEl, newEl) {
    var i, a;
    // Drop attributes the new node no longer has (iterate backwards).
    for (i = oldEl.attributes.length - 1; i >= 0; i--) {
      a = oldEl.attributes[i];
      if (newEl.getAttribute(a.name) === null) oldEl.removeAttribute(a.name);
    }
    // Add or update attributes.
    for (i = 0; i < newEl.attributes.length; i++) {
      a = newEl.attributes[i];
      if (oldEl.getAttribute(a.name) !== a.value) oldEl.setAttribute(a.name, a.value);
    }
    // Properties that outerHTML does not carry as attributes.
    if (oldEl.tagName === 'INPUT' || oldEl.tagName === 'TEXTAREA') {
      if (oldEl.checked !== newEl.checked) oldEl.checked = newEl.checked;
      if (oldEl.value !== newEl.value) oldEl.value = newEl.value;
    }
    if (oldEl.tagName === 'SELECT' && oldEl.selectedIndex !== newEl.selectedIndex) {
      oldEl.selectedIndex = newEl.selectedIndex;
    }
  }

  function patchChildren(oldEl, newEl) {
    // Snapshot both child lists: patching mutates oldEl's live childNodes.
    var oldKids = Array.prototype.slice.call(oldEl.childNodes);
    var newKids = Array.prototype.slice.call(newEl.childNodes);
    var i;
    for (i = 0; i < oldKids.length && i < newKids.length; i++) {
      patchNode(oldKids[i], newKids[i]);
    }
    for (i = oldKids.length - 1; i >= newKids.length; i--) {
      oldEl.removeChild(oldKids[i]);
    }
    for (i = oldKids.length; i < newKids.length; i++) {
      oldEl.appendChild(newKids[i]);
    }
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
    var newRoot = parseFragment(m.html);
    var oldRoot = pane.firstElementChild;
    if (newRoot && oldRoot) {
      try {
        patchNode(oldRoot, newRoot);
      } catch (err) {
        // Defensive: a full rebuild is always correct.
        pane.innerHTML = m.html;
      }
    } else if (newRoot) {
      pane.appendChild(newRoot);
    }
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
      // If the patch replaced the previously focused input element (a large
      // restructure), focus would be dropped (killing the soft keyboard /
      // IME session); restore it. Usually the input survives in place and
      // this is a no-op.
      if (inputFocusWanted) {
        var el = findInputIn(box);
        if (el) {
          inputFocusWanted = true;
          try { el.focus({ preventScroll: true }); } catch (err) { el.focus(); }
        }
      }
    }
    restoreNestedScrolls(m);
    restoreScroll(m);
    syncDrawnScrollbars();
  }

  // The live chat-input cursor blinks via a 500ms JS timer that toggles the
  // cursor element's inline visibility (Monaco ViewCursors, default 'blink'
  // style) — a static HTML snapshot cannot reproduce that, and it may even
  // capture the hidden phase (the cursor vanishes until the next extract).
  // When the state message reports the live input as focused, force the
  // cursor visible and blink it with the CSS animation; otherwise leave the
  // snapshot's visibility alone (hidden when unfocused, exactly like live).
  // Runs on BOTH update paths: the cursor element survives DOM patches in
  // place, and scroll-only updates must not drop the blink.
  function syncCursor(m) {
    var box = pane.querySelector('.chat-input-container');
    if (!box) return;
    var ed = box.querySelector('.interactive-input-editor .monaco-editor') || box.querySelector('.monaco-editor');
    var cur = ed ? ed.querySelector('.cursor') : null;
    if (!cur) return;
    var cls = (cur.getAttribute('class') || '').split(/\s+/).filter(function (c) { return c !== ''; });
    var has = cls.indexOf('mirror-cursor-blink') >= 0;
    if (m.inputFocused) {
      if (cur.style.visibility !== 'inherit') cur.style.visibility = 'inherit';
      if (!has) cls.push('mirror-cursor-blink');
    } else if (has) {
      cls = cls.filter(function (c) { return c !== 'mirror-cursor-blink'; });
    } else {
      return; // nothing to change
    }
    cur.setAttribute('class', cls.join(' '));
  }

  // ---- Popup (context view) rendering ---------------------------------
  // Popup menus (agent/mode picker, model picker, reasoning effort, ...)
  // render in the live page's .context-view container, which is OUTSIDE the
  // pane root, so the pane HTML alone never contains them. The server
  // extracts the visible container (m.popup) and the mirror renders it as a
  // sibling of the pane root inside #pane. It is positioned FIXED in
  // viewport coordinates (pane origin + the server's pane-relative offset):
  // fixed positioning escapes #pane's overflow clipping (the popup can sit
  // lower than the mirror viewport) while the element stays inside #pane in
  // the DOM, so workbench CSS scoping and event bubbling keep working.
  var popupEl = null;
  var lastPopup = null;
  // lastAnchorPath is the DOM path (from the pane root) of the control the
  // user last clicked (mousedown). The next popup is positioned relative to
  // the mirror's copy of that control, NOT at the live pane-relative
  // position: the mirror's responsive layout does not preserve live pixel
  // offsets, so a copied position lands the popup in the wrong place.
  var lastAnchorPath = null;
  // Resolve a DOM path (child indices from the pane root) to a current
  // element, or null when the path no longer resolves (DOM re-patched).
  function resolvePath(path) {
    var el = pane.firstElementChild;
    if (!el) return null;
    if (!path || !path.length) return el;
    for (var i = 0; i < path.length; i++) {
      el = el.children[path[i]];
      if (!el) return null;
    }
    return el;
  }
  // Validate the live anchor (popup rect vs live anchor rect, both in LIVE
  // pane-relative coordinates). A popup opens flush against one of its
  // anchor's horizontal edges (VS Code aligns the popup's bottom with the
  // anchor's top when it opens above, and vice versa), and its horizontal
  // span overlaps the anchor's span or extends only slightly beyond it: a
  // popup may be anchored to a WIDER group than the clicked control (the
  // model picker spans the model-name button AND the effort button, so its
  // right edge sits ~40px right of the clicked button's right edge). A
  // stale anchor (the last click was on a different control, or the popup
  // was keyboard-opened) is neither vertically flush with nor horizontally
  // near the open popup, no matter its size.
  function popupNearAnchor(p, a) {
    var pT = p.top, pB = p.top + p.height;
    var pL = p.left, pR = p.left + p.width;
    var aT = a.top, aB = a.top + a.height;
    var aL = a.left, aR = a.left + a.width;
    var vGap = Math.min(Math.abs(pB - aT), Math.abs(pT - aB));
    if (vGap > 40) return false;
    var hGap = pR < aL ? aL - pR : (aR < pL ? pL - aR : 0);
    return hGap <= 150;
  }
  // The mirror-resolved anchor must be the same control the server resolved
  // live: a stale path that resolves to a sibling control has a very
  // different size (the responsive layout preserves control widths).
  function anchorSizeMatches(mirror, live) {
    return Math.abs(mirror.width - live.width) <= Math.max(24, live.width * 0.5) &&
           Math.abs(mirror.height - live.height) <= Math.max(8, live.height * 0.5);
  }
  function positionPopup() {
    if (!popupEl || !lastPopup) return;
    var pr = pane.getBoundingClientRect();
    var left, top;
    // Anchor-relative placement: the server sends the live popup rect and
    // the live anchor rect, both pane-relative; their difference is a pure
    // offset that transfers to the mirror. Apply it to the mirror's own
    // copy of the anchor (its current rect in mirror viewport coordinates).
    var used = false;
    var ar = null;
    if (lastAnchorPath && lastPopup.anchor) {
      var a = resolvePath(lastAnchorPath);
      if (a) {
        var ar0 = a.getBoundingClientRect();
        if (popupNearAnchor(lastPopup, lastPopup.anchor) &&
            anchorSizeMatches(ar0, lastPopup.anchor)) {
          ar = ar0;
          left = ar.left + (lastPopup.left - lastPopup.anchor.left);
          top = ar.top + (lastPopup.top - lastPopup.anchor.top);
          used = true;
        }
      }
    }
    if (!used) {
      left = pr.left + lastPopup.left;
      top = pr.top + lastPopup.top;
    }
    // The mirror viewport is not the live window: a transferred position can
    // overflow it. Flip the popup to the other side of the anchor (keeping
    // the live gap) when anchor-based placement overflows.
    var vh = window.innerHeight, vw = window.innerWidth;
    if (used && (top < 0 || top + lastPopup.height > vh)) {
      if (lastPopup.top < lastPopup.anchor.top) {
        // Live popup sat above the anchor; move it below.
        var gap = lastPopup.anchor.top - (lastPopup.top + lastPopup.height);
        top = ar.top + ar.height + Math.max(0, gap);
      } else {
        // Live popup sat below the anchor; move it above.
        var gap2 = lastPopup.top - (lastPopup.anchor.top + lastPopup.anchor.height);
        top = ar.top - lastPopup.height - Math.max(0, gap2);
      }
    }
    // The final clamps apply to BOTH branches: the fallback position is a
    // live pane-relative offset, which in the responsive mirror can land far
    // outside the viewport (e.g. a bottom-right popup like the context-usage
    // widget pushed below it). Clamping keeps it on screen near its anchor
    // area.
    if (top < 0) top = 0;
    if (top + lastPopup.height > vh) top = Math.max(0, vh - lastPopup.height);
    if (left < 0) left = 0;
    if (left + lastPopup.width > vw) {
      left = Math.max(0, vw - lastPopup.width);
    }
    popupEl.style.position = 'fixed';
    popupEl.style.left = left + 'px';
    popupEl.style.top = top + 'px';
  }
  // Live closes a dropdown with a 150ms CSS animation (actionWidget.ts):
  // it stamps the widget's CURRENT opacity/transform as the close keyframes'
  // start values (so a close interrupted mid-open animates from where it
  // was), adds the -closing class (close keyframes + pointer-events: none),
  // and removes the element after the animation duration. Reproduce that
  // here so the mirror's close matches live. Non-dropdown popups (hover
  // widgets, tooltips) have no live close animation: remove them at once.
  var popupCloseTimer = null;
  function ensureCloseRemoval() {
    // The close state persists across many state messages (m.popup stays
    // null), so the removal timer must be armed exactly once.
    if (popupCloseTimer != null) return;
    popupCloseTimer = setTimeout(function () {
      popupCloseTimer = null;
      if (popupEl) { popupEl.remove(); popupEl = null; }
    }, 150);
  }
  function closePopup() {
    if (!popupEl) return;
    var w = popupEl.querySelector('.action-widget.action-widget-dropdown');
    if (!w) { popupEl.remove(); popupEl = null; return; }
    if (w.classList.contains('action-widget-dropdown-closing')) {
      ensureCloseRemoval();
      return;
    }
    var cs = getComputedStyle(w);
    w.style.setProperty('--action-widget-close-start-opacity', cs.opacity);
    w.style.setProperty('--action-widget-close-start-transform', cs.transform);
    w.classList.add('action-widget-dropdown-closing');
    ensureCloseRemoval();
  }
  // Restart the open animation on a widget that was patched IN PLACE instead
  // of inserted fresh (a reopen landing while the close animation is still
  // running): the reflow trick resets the CSS animation to its first frame.
  function restartOpenAnimation() {
    var w = popupEl ? popupEl.querySelector('.action-widget.action-widget-dropdown') : null;
    if (!w) return;
    w.style.animation = 'none';
    void w.offsetWidth;
    w.style.animation = '';
  }
  function updatePopup(m) {
    // A defensive pane.innerHTML rebuild in applyState detaches the popup;
    // drop the stale reference so it is re-appended on the next HTML update.
    if (popupEl && !popupEl.isConnected) {
      popupEl = null;
      if (popupCloseTimer != null) { clearTimeout(popupCloseTimer); popupCloseTimer = null; }
    }
    if (!m.popup) {
      if (popupEl) closePopup();
      lastPopup = null;
      return;
    }
    // A new popup on top of a still-closing one: the widget below is patched
    // in place (not re-inserted), so the open animation must be restarted
    // by hand after the patch.
    var reopening = lastPopup === null && popupEl != null;
    lastPopup = m.popup;
    if (m.popup.html != null) {
      var want = parseFragment(m.popup.html);
      if (popupEl) {
        if (popupCloseTimer != null) { clearTimeout(popupCloseTimer); popupCloseTimer = null; }
        patchNode(popupEl, want);
      } else {
        pane.appendChild(want);
        popupEl = want;
      }
      if (reopening) restartOpenAnimation();
    }
    positionPopup();
  }

  // Resolve a DOM path from the pane root to the element it addresses
  // (null when the path no longer matches the current DOM, e.g. after a
  // restructure the mirror has not re-rendered yet).
  function pathEl(path) {
    var el = pane.firstElementChild || pane;
    for (var i = 0; i < path.length; i++) {
      el = el.children[path[i]];
      if (!el) return null;
    }
    return el;
  }
  // Restore the live scroll position on the SAME element the server measured,
  // identified by its DOM path from the pane root (m.scrollPath). Path-based
  // resolution is size-independent, which the responsive layout requires:
  // the mirror is not the live pane's pixel size, so a "biggest scroller"
  // heuristic could pick a different element than the server measured.
  //
  // The live chat list is bottom-anchored: it keeps scrollTop at 0 and
  // encodes the scroll position in the rows container's negative top offset
  // (m.scroll.offset). The session-picker list is top-anchored: offsetTop
  // stays 0 and the position is the scroller's scrollTop. Both are mapped
  // onto the mirror's (static, top-anchored) layout by aligning the mirror's
  // viewport with the live viewport inside the rendered row window.
  //
  // Re-anchoring happens ONLY when the live viewport moved (v0/v1 changed),
  // on the first state, on a window switch, or when the scroller node was
  // replaced. Content-only updates (streaming tokens, nested scrolls) keep
  // the user's local scroll position, so a reading position reached by
  // wheeling the mirror up survives the constant html updates of a stream.
  function restoreScroll(m) {
    if (!m.scroll || !m.scrollPath) return;
    var el = pathEl(m.scrollPath);
    if (!el) return;
    el.scrollLeft = m.scroll.left;
    var max = el.scrollHeight - el.clientHeight;
    var v0 = m.scroll.top - m.scroll.offset;
    var v1 = v0 + m.scroll.h;
    // The live viewport did not move since the last anchor (and it is the
    // same scroller of the same window): this update only changed content
    // (streaming tokens, nested scrolls). Keep the user's local scroll
    // position instead of re-anchoring — re-anchoring here is what yanked
    // the mirror back to the bottom on every streamed token, making the
    // top unreachable while output streams.
    if (el === lastAnchoredEl && m.windowId === lastAnchoredWin &&
        v0 === lastV0 && v1 === lastV1) {
      return;
    }
    var target = m.scroll.top;
    var a = alignToLiveViewport(el, m.scroll, m.scrollRows);
    if (a != null) {
      // The mirror's content is only the live list's currently rendered
      // rows (a sliding window), not the full conversation, so align the
      // mirror's viewport with the live viewport inside the rendered
      // window.
      //
      // a.above / a.inside are the mirror pixels of the rendered content
      // that fall above / inside the live viewport. The live viewport's
      // mirror height (a.inside) can exceed the mirror scroller's own
      // height (the mirror is shorter than live, or its rows are taller),
      // in which case the whole live viewport does not fit and one end
      // must be cut.
      //
      // Always keep the BOTTOM of the live viewport on screen
      // (scrollTop = above + max(0, inside - clientH)); the hidden sliver
      // is the live viewport's top, reachable by scrolling up. The anchor
      // must NEVER switch between ends: switching teleports the mirror's
      // viewport — at the top of a bottom-anchored list (offset 0) the
      // top-anchor target (above) differs from the bottom-anchor target by
      // ~h - clientH, so the flip moved the mirror nearly a full screen in
      // one state message. With a fixed bottom anchor the target moves
      // continuously as live scrolls, and the cut sliver is always
      // reachable because a wheel that live cannot absorb (content fits, or
      // live is pinned at an end) still moves the mirror's own viewport
      // locally (see the wheel handler).
      target = a.above + Math.max(0, a.inside - el.clientHeight);
    } else {
      var distBottom = (m.scroll.scrollH - m.scroll.h) + m.scroll.offset;
      target = distBottom <= 0 ? max : max - distBottom;
    }
    el.scrollTop = Math.max(0, Math.min(max, target));
    lastAnchoredEl = el;
    lastAnchoredWin = m.windowId;
    lastV0 = v0;
    lastV1 = v1;
  }

  // The reasoning-trace list (.chat-used-context-list inside a
  // .chat-thinking-box) is a NESTED scroll container: the main pane
  // scroller's state never covers it. While the trace streams, VS Code pins
  // its scrollTop to the bottom (DomScrollableElement applies
  // setScrollPosition to the wrapped list element), but the extracted HTML
  // carries no scrollTop, so the mirror's fresh copy would otherwise always
  // show the TOP of the trace. Map the live ratio (top / (scrollH -
  // clientH)) onto the mirror's own content, which is the same text at a
  // different (responsive) width.
  function restoreNestedScrolls(m) {
    if (!m.nestedScrolls || !m.nestedScrolls.length) return;
    var root = pane.firstElementChild;
    if (!root) return;
    if (!lastNestedApplied) lastNestedApplied = {};
    for (var i = 0; i < m.nestedScrolls.length; i++) {
      var ns = m.nestedScrolls[i];
      var el = root;
      for (var j = 0; j < ns.path.length; j++) {
        el = el.children[ns.path[j]];
        if (!el) { el = null; break; }
      }
      if (!el) continue;
      var liveMax = ns.scrollH - ns.clientH;
      var max = el.scrollHeight - el.clientHeight;
      if (max <= 0) continue;
      // The live state of this container did not change since the last
      // apply (and it is the same element): keep the user's local scroll
      // position instead of re-applying the live ratio. A replaced element
      // (patch, window switch) or a moved live scroll re-applies.
      var key = JSON.stringify(ns.path);
      var prev = lastNestedApplied[key];
      if (prev && prev.el === el && prev.top === ns.top &&
          prev.scrollH === ns.scrollH && prev.clientH === ns.clientH) {
        continue;
      }
      var ratio = liveMax > 0 ? Math.max(0, Math.min(1, ns.top / liveMax)) : 0;
      el.scrollTop = ratio * max;
      lastNestedApplied[key] = { el: el, top: ns.top, scrollH: ns.scrollH, clientH: ns.clientH };
    }
  }

  // Maps the live viewport onto the mirror's rendered rows. The live list's
  // rendered rows carry [offsetTop, offsetHeight] pairs in full-content
  // coordinates (rows); the live viewport spans [v0, v0 + scroll.h] in the
  // same coordinates. v0 combines the scroller's scrollTop with the rows
  // container's offsetTop: bottom-anchored lists (chat) keep scrollTop at 0
  // and encode the position in the rows' negative offsetTop (v0 = -offset);
  // top-anchored lists (session picker) keep offsetTop at 0 and use
  // scrollTop (v0 = top). The mirror shows the same rows in the same order
  // but at different heights (responsive width), so this returns the mirror
  // pixels of the rendered content that fall ABOVE the live viewport
  // (above) and INSIDE it (inside). The caller picks the scrollTop so the
  // end of the live viewport that must stay visible is on screen.
  function alignToLiveViewport(el, scroll, rows) {
    if (!rows || !rows.length) return null;
    var rowEls = el.querySelector('.monaco-list-rows');
    if (!rowEls || !rowEls.children.length) return null;
    var v0 = scroll.top - scroll.offset;
    var v1 = v0 + scroll.h;
    var n = Math.min(rowEls.children.length, rows.length / 2);
    var above = 0, inside = 0;
    for (var i = 0; i < n; i++) {
      var top = rows[i * 2], h = rows[i * 2 + 1];
      if (h <= 0) continue;
      if (top + h <= v0) { above += rowEls.children[i].offsetHeight; continue; }
      if (top >= v1) break;
      var mh = rowEls.children[i].offsetHeight;
      if (top < v0) above += mh * (v0 - top) / h;
      inside += mh * (Math.min(top + h, v1) - Math.max(top, v0)) / h;
    }
    return { above: above, inside: inside };
  }

  // The self-drawn scrollbar (.scrollbar > .slider inside a
  // .monaco-scrollable-element) arrives with inline top/height computed for
  // the live pane's geometry. The mirror scroller is a different height, and
  // its own content is only the currently rendered (virtualized) rows, so
  // its native scroll range is unstable and does not represent the chat's
  // total range. Instead, mirror the LIVE slider state, transformed to the
  // mirror's height:
  //   liveRatio = (top - offset) / (scrollH - h)  (0 = top, 1 = bottom;
  //     bottom-anchored lists have top = 0, top-anchored have offset = 0)
  //   sliderH   = clientH * h / scrollH        (live visible/total ratio,
  //   sliderTop = liveRatio * (clientH - sliderH)   scaled to mirror height)
  // The track is absolute inside the scroller, so it scrolls out of view with
  // the content; offset its top by the scroller's scrollTop to pin it to the
  // top of the visible area. Other (nested) scrollers fall back to their own
  // geometry — except the reasoning-trace wrap of a .chat-thinking-box, whose
  // live state is carried in lastNestedScrolls and handled after the loop.
  // Called after every DOM patch, scroll restoration, local scroll, and
  // viewport resize.
  function syncDrawnScrollbars() {
    var root = pane.firstElementChild;
    if (!root) return;
    var target = lastScrollPath && lastScrollPath.length ? pathEl(lastScrollPath) : null;
    var scrollers = root.querySelectorAll('.monaco-list > .monaco-scrollable-element');
    for (var i = 0; i < scrollers.length; i++) {
      var el = scrollers[i];
      var track = el.querySelector(':scope > .scrollbar.vertical');
      if (!track) continue;
      var slider = track.querySelector('.slider');
      if (!slider) continue;
      var clientH = el.clientHeight;
      if (clientH <= 0) continue;
      var ratio, sliderH;
      if (el === target && lastScroll && lastScroll.scrollH > lastScroll.h) {
        var liveMax = lastScroll.scrollH - lastScroll.h;
        ratio = Math.max(0, Math.min(1, (lastScroll.top - lastScroll.offset) / liveMax));
        sliderH = clientH * lastScroll.h / lastScroll.scrollH;
      } else {
        var max = el.scrollHeight - clientH;
        if (el.scrollHeight <= 0) continue;
        // Live is not scrolling this scroller (it is not the measured one),
        // so the track arrived with its live class: when the live content
        // FITS (no live scrollbar) the track is .invisible (opacity 0) even
        // though the mirror's copy overflows and scrolls locally. Show it
        // exactly when the mirror can scroll, hide it otherwise.
        track.style.opacity = max > 0 ? '1' : '';
        ratio = max > 0 ? Math.max(0, Math.min(1, el.scrollTop / max)) : 1;
        sliderH = clientH * clientH / el.scrollHeight;
      }
      if (sliderH < 20) sliderH = 20;
      track.style.height = clientH + 'px';
      track.style.top = el.scrollTop + 'px';
      slider.style.top = ratio * (clientH - sliderH) + 'px';
      slider.style.height = sliderH + 'px';
      // The live page pins the sticky (pinned) user message to the top of the
      // list viewport (its scrollTop is always 0, the sticky sits at top:0 in
      // scroller coordinates). The mirror scroller does scroll, so offset the
      // sticky by -scrollTop to keep it pinned to the top of the visible area.
      if (el === target) {
        var sticky = el.querySelector(':scope > .monaco-tree-sticky-container');
        if (sticky) sticky.style.top = -el.scrollTop + 'px';
      }
    }
    // The reasoning-trace wrap of each .chat-thinking-box is a nested
    // .monaco-scrollable-element (NOT a .monaco-list child, so the loop above
    // never touches it). Its slider arrives with live inline geometry (pinned
    // to the bottom while the trace streams) that matches neither the
    // mirror's (different) size nor the mirror list's own scrollTop before
    // restoreNestedScrolls ran, so recompute it from the live ratio.
    if (lastNestedScrolls && lastNestedScrolls.length) {
      for (var k = 0; k < lastNestedScrolls.length; k++) {
        var ns = lastNestedScrolls[k];
        var listEl = root;
        for (var j = 0; j < ns.path.length; j++) {
          listEl = listEl ? listEl.children[ns.path[j]] : null;
        }
        if (!listEl) continue;
        var wrap = listEl.parentElement;
        if (!wrap) continue;
        var vtrack = wrap.querySelector(':scope > .scrollbar.vertical');
        if (!vtrack) continue;
        var vslider = vtrack.querySelector('.slider');
        if (!vslider) continue;
        var vch = wrap.clientHeight;
        if (vch <= 0) continue;
        var vliveMax = ns.scrollH - ns.clientH;
        var vratio = vliveMax > 0 ? Math.max(0, Math.min(1, ns.top / vliveMax)) : 0;
        var vsliderH = ns.scrollH > 0 ? vch * ns.clientH / ns.scrollH : vch;
        if (vsliderH < 20) vsliderH = 20;
        vtrack.style.height = vch + 'px';
        vtrack.style.top = wrap.scrollTop + 'px';
        vslider.style.top = vratio * (vch - vsliderH) + 'px';
        vslider.style.height = vsliderH + 'px';
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
  // Wheel forwarding only needs to land INSIDE the live scrollable element,
  // not on the exact touched element. Chat list rows are virtualized: while a
  // swipe is in flight the live list re-renders and the touched row can move
  // out of the live viewport (a margin row above/below it), where its DOM
  // path resolves to an off-screen point and the wheel is silently dropped.
  // The scroller itself is a stable structural element, so dispatch wheels
  // at its center. Scrollers other than the measured chat list keep the
  // precise element mapping.
  function wheelPoint(target, c) {
    var sc = (target && target.closest) ? target.closest('.monaco-scrollable-element') : null;
    if (sc && lastScrollPath) {
      var ei = elemInfo(sc, 0, 0);
      if (ei.path && JSON.stringify(ei.path) === JSON.stringify(lastScrollPath)) {
        return { path: lastScrollPath, relX: 0.5, relY: 0.5 };
      }
    }
    var ei2 = elemInfo(target, c.x, c.y);
    return { path: ei2.path, relX: ei2.relX, relY: ei2.relY };
  }
  function elemInfo(target, cx, cy) {
    var root = pane.firstElementChild || pane;
    var t = (target && target !== pane) ? target : root;
    // The popup (.context-view) is a sibling of the pane root, so no path
    // from the pane root reaches it: prefix its path with -1 and the server
    // resolves it against the live .context-view instead.
    var pv = (t && t.closest) ? t.closest('.context-view') : null;
    if (pv) {
      var pidx = [];
      var n2 = t;
      while (n2 && n2 !== pv) {
        var p2 = n2.parentElement;
        if (!p2) break;
        pidx.unshift(Array.prototype.indexOf.call(p2.children, n2));
        n2 = p2;
      }
      if (n2 === pv) {
        var tr2 = t.getBoundingClientRect();
        var rx2 = tr2.width > 0 ? (cx - tr2.left) / tr2.width : 0;
        var ry2 = tr2.height > 0 ? (cy - tr2.top) / tr2.height : 0;
        if (rx2 < 0) rx2 = 0; else if (rx2 > 1) rx2 = 1;
        if (ry2 < 0) ry2 = 0; else if (ry2 > 1) ry2 = 1;
        return { path: [-1].concat(pidx), relX: rx2, relY: ry2 };
      }
    }
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

  // Window picker: switching the option tells the server which VS Code
  // window to mirror from now on.
  status.addEventListener('change', function () {
    send({ type: 'window', id: status.value });
  });

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
    // Anchor for the next popup: the nearest button-like ancestor of the
    // click target. VS Code anchors context views to the clicked control,
    // and the click may land on a child of it (icon span, label), so walk
    // up to the control itself. Clicks INSIDE the popup never update the
    // anchor: menu rows embed <a> elements (e.g. the model-picker pin
    // icons) that would corrupt it, and the open popup must not be
    // re-anchored (and re-positioned) while a click is in flight.
    var inPopup = (e.target && e.target.closest) ? e.target.closest('.context-view') : null;
    var anchorPath = null;
    if (!inPopup) {
      // VS Code labels its controls with role/aria-label, but not every one is
      // a semantic button: the model picker is a DIV with role="group" (a split
      // of the model-name button and the effort button), so match any element
      // carrying a role or aria-label (plus real buttons/links). closest()
      // returns the nearest such ancestor, i.e. the clicked control itself.
      var anc = e.target.closest('[role], [aria-label], button, a');
      anchorPath = (anc && anc !== pane) ? elemInfo(anc, e.clientX, e.clientY).path : null;
      lastAnchorPath = anchorPath;
    }
    send({ type: 'mouse', kind: 'pressed', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail, anchorPath: anchorPath });
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
  // The live chat list quantizes wheel input (measured on the live page):
  // any event with |delta| > 10px scrolls exactly one row (~50px) no matter
  // how large the delta is, and smaller deltas are ignored entirely.
  // Forwarding every raw event therefore makes touch swipes (dozens of small
  // high-frequency touchmove events) scroll many times faster than a desktop
  // wheel (one event per notch). Normalize instead: accumulate deltas per
  // scrollable element and emit one event per WHEEL_CHUNK px of accumulated
  // wheel movement (one desktop notch, keeps the wheel feel unchanged) or
  // TOUCH_CHUNK px of finger movement (1:1 finger tracking), flushing the
  // remainder when the stream stops.
  var WHEEL_CHUNK = 100, TOUCH_CHUNK = 50, FLUSH_MIN = 11, FLUSH_MS = 150;
  var wheelAcc = {}; // key: scroller identity -> { x, y, path, relX, relY, buttons, ax, ay, timer }
  function flushWheelAcc(key) {
    var a = wheelAcc[key];
    if (!a) return;
    if (a.timer) { clearTimeout(a.timer); a.timer = null; }
    // FLUSH_MIN is the live list's per-event threshold: below it the flush
    // would be ignored anyway, so dropping it is invisible.
    if (Math.abs(a.ay) >= FLUSH_MIN || Math.abs(a.ax) >= FLUSH_MIN) {
      send({ type: 'mouse', kind: 'wheel', x: a.x, y: a.y, path: a.path, relX: a.relX, relY: a.relY, deltaX: a.ax, deltaY: a.ay, buttons: a.buttons });
    }
    a.ax = 0; a.ay = 0;
    delete wheelAcc[key];
  }
  // Key the accumulator by the scrollable element, not by the element under
  // the cursor: chat list rows are virtualized and their DOM index changes
  // as the list scrolls, which would fragment the accumulation.
  function wheelAccKey(target) {
    var sc = (target && target.closest) ? target.closest('.monaco-scrollable-element') : null;
    if (!sc) sc = target;
    var ei = elemInfo(sc, 0, 0);
    return ei.path ? JSON.stringify(ei.path) : 'root';
  }
  // Some devices report wheel deltas in lines (deltaMode 1) or pages
  // (deltaMode 2); normalize to pixels.
  function normDelta(v, mode, pageH) {
    if (mode === 1) return v * 20;
    if (mode === 2) return v * pageH;
    return v;
  }
  function accumulateWheel(key, x, y, path, relX, relY, buttons, dx, dy, chunk) {
    var a = wheelAcc[key];
    if (!a) {
      a = { x: x, y: y, path: path, relX: relX, relY: relY, buttons: buttons, ax: 0, ay: 0, timer: null };
      wheelAcc[key] = a;
    }
    a.x = x; a.y = y; a.path = path; a.relX = relX; a.relY = relY; a.buttons = buttons;
    a.ax += dx; a.ay += dy;
    var n = 0;
    while (Math.abs(a.ay) >= chunk || Math.abs(a.ax) >= chunk) {
      var dY = a.ay >= chunk ? chunk : (a.ay <= -chunk ? -chunk : 0);
      var dX = a.ax >= chunk ? chunk : (a.ax <= -chunk ? -chunk : 0);
      send({ type: 'mouse', kind: 'wheel', x: a.x, y: a.y, path: a.path, relX: a.relX, relY: a.relY, deltaX: dX, deltaY: dY, buttons: a.buttons });
      a.ax -= dX; a.ay -= dY;
      if (++n > 100) break; // safety valve against pathological deltas
    }
    if (a.timer) clearTimeout(a.timer);
    a.timer = setTimeout(function () { flushWheelAcc(key); }, FLUSH_MS);
  }
  // The element whose scrollTop the mirror moves locally on a wheel/touch.
  // Prefer the element the server measured (lastScrollPath): restoreScroll
  // re-anchors that same element on every state message, so the local
  // movement and the periodic sync never fight over two different scrollers.
  // When the server measured no scroller (content fits in live), fall back
  // to the nearest visible monaco-list scroller that overflows its own box
  // — the mirror's copy can still overflow even when live's does not (the
  // mirror viewport is shorter, or wraps text differently). Returns null
  // when the mirror has no local scroll range: the movement is pure
  // forwarding.
  function localScrollEl(target) {
    // A wheel inside a reasoning-trace list (.chat-thinking-box
    // .chat-used-context-list) moves THAT list locally, never the main
    // viewport: the trace's live state is synced separately
    // (restoreNestedScrolls), and moving the main scroller here would yank
    // the reading position while the user is only scrolling the trace.
    var nested = (target && target.closest)
      ? target.closest('.chat-thinking-box .chat-used-context-list')
      : null;
    if (nested && nested.scrollHeight - nested.clientHeight > 0) return nested;
    if (lastScrollPath != null) {
      var p = pathEl(lastScrollPath);
      if (p && p.scrollHeight - p.clientHeight > 0) return p;
    }
    var sc = (target && target.closest) ? target.closest('.monaco-scrollable-element') : null;
    if (!sc || !sc.parentElement) return null;
    if (String(sc.parentElement.className).indexOf('monaco-list') < 0) return null;
    if (sc.scrollHeight - sc.clientHeight <= 0) return null;
    return sc;
  }
  // Every wheel/touch is forwarded to live (chunked) AND moves the mirror's
  // own viewport by the same delta, simultaneously:
  // - when live can absorb the wheel, the next state message re-anchors the
  //   mirror to live's new position — the local move is a transient visual
  //   placeholder until that sync lands;
  // - when live cannot (the content fits, or live is pinned at an end it
  //   cannot scroll away from), the local move persists, which is what makes
  //   the mirror's extra sliver reachable without any top/bottom
  //   special-casing.
  pane.addEventListener('wheel', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var p = wheelPoint(e.target, c);
    var dx = normDelta(e.deltaX, e.deltaMode, pane.clientHeight);
    var dy = normDelta(e.deltaY, e.deltaMode, pane.clientHeight);
    accumulateWheel(wheelAccKey(e.target), c.x, c.y, p.path, p.relX, p.relY, e.buttons, dx, dy, WHEEL_CHUNK);
    var local = localScrollEl(e.target);
    if (local) {
      var max = local.scrollHeight - local.clientHeight;
      local.scrollTop = Math.max(0, Math.min(max, local.scrollTop + dy));
    }
  }, { passive: false });
  // Touch scrolling: touch swipes do not produce wheel events, so forward the
  // finger movement as wheel deltas and let the live page's message history
  // scroll (the mirror's DOM is a static snapshot; native scrolling of it
  // would diverge from the live page). Signs are flipped: finger up scrolls
  // the page down, matching native touch behavior. Deltas are accumulated
  // (see accumulateWheel) so a swipe scrolls at ~1:1 finger speed instead of
  // one 50px row per touchmove event.
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
    // Same forward + local-move design as the wheel handler (signs flipped:
    // finger up scrolls the page down).
    var c = paneCoords(t);
    var p = wheelPoint(touchState.target, c);
    accumulateWheel(wheelAccKey(touchState.target), c.x, c.y, p.path, p.relX, p.relY, 0, -dx, -dy, TOUCH_CHUNK);
    var local = localScrollEl(touchState.target);
    if (local) {
      var max = local.scrollHeight - local.clientHeight;
      local.scrollTop = Math.max(0, Math.min(max, local.scrollTop - dy));
    }
  }, { passive: false });
  function endTouch() {
    touchState = null;
    // Flush pending touch deltas now instead of waiting for the idle timer.
    for (var k in wheelAcc) flushWheelAcc(k);
  }
  pane.addEventListener('touchend', endTouch);
  pane.addEventListener('touchcancel', endTouch);
  pane.addEventListener('contextmenu', function (e) { e.preventDefault(); });
  // Keep the self-drawn scrollbar in sync with any local (programmatic)
  // scrolling of the mirror scrollers and with viewport resizes (rotation).
  pane.addEventListener('scroll', syncDrawnScrollbars, true);
  window.addEventListener('resize', syncDrawnScrollbars);
  window.addEventListener('resize', positionPopup);
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
