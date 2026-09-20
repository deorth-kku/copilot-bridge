
(function () {
  var status = document.getElementById('status');
  var pane = document.getElementById('pane');
  var cssEl = document.getElementById('mirror-css');
  var themeEl = document.getElementById('mirror-theme');
  var ws = null;
  var lastCSSVer = '';
  var lastThemeVer = '';
  var lastWinVer = '';
  // The window id last selected by (or following) this tab. Remembered in
  // localStorage when the tab leaves for the workspaces page, so returning
  // can restore the same window.
  var lastWinId = '';
  // Set once the remembered window (if any) has been restored after a
  // (re)connect: only the first good state message may re-send it.
  var restoredWin = false;
  // Workspace URI this tab arrived with (?ws= from the workspaces page).
  // It is forwarded to the WS handshake so the server selects that window
  // BEFORE the first snapshot (no flash of the default window), and it
  // suppresses the remembered-window restore below.
  var pendingWs = '';
  try { pendingWs = new URLSearchParams(location.search).get('ws') || ''; } catch (e) {}
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
  // The live chat-input cursor's character index (m.cursorChar: count of
  // characters before the caret; -1 = no cursor). The mirror re-wraps the
  // input text at its own width, so the cursor is re-seated by this index
  // instead of the live cursor pixels.
  var lastCursorChar = -1;
  // The scroller element last anchored by restoreScroll and the window it
  // belonged to. A different element or window means the previous anchor is
  // stale (the patch replaced the scroller node, or the user switched
  // windows), so the viewport re-anchors to live.
  var lastAnchoredEl = null;
  var lastAnchoredWin = null;
  // The mirror's viewport is anchored to a MESSAGE ENTRY, not to live's
  // absolute scroll position. mirrorAnchor is the data-index of the row at
  // the top of the mirror's viewport plus the pixel offset (mirror content
  // coordinates) from that row's top to the viewport top. Anchoring to an
  // entry (instead of a live pixel position) is what removes the jitter: an
  // html update re-lays-out the rows and re-applies
  // scrollTop = mirrorTop(anchorRow) + offset, so the viewport stays pinned
  // to the same message even as rows above it grow/shrink or the rendered
  // window shifts. The anchor is only valid while its row is inside live's
  // rendered window (the mirror can only show rows live has rendered); when
  // it leaves, the viewport re-anchors to live.
  var mirrorAnchor = null; // { index: <data-index string>, offset: <px> } | null
  // While the user sits at the bottom (watching a stream), the viewport
  // follows live's bottom instead of pinning to an entry, so new tokens stay
  // visible. This requires BOTH the mirror at its own max AND live at its
  // content bottom (liveAtBottom): the mirror's content is only the rows
  // live has RENDERED (a sliding window), so sitting at the mirror's max may
  // mean "bottom of the rendered window" with unrendered messages still
  // below in live — following the bottom there would teleport the viewport
  // to the bottom of newly-rendered content. Cleared when the user wheels up
  // away from the bottom, or whenever live is not at its bottom.
  var pinnedToBottom = true;
  var BOTTOM_EPS = 2;
  // The live scroll state from the LAST state message (m.scroll), kept so
  // liveAtBottom() works between state messages (e.g. inside the wheel
  // handler, before the next sync lands).
  var lastLiveScroll = null;
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

  // Rebuild the picker options only when the window set (or the error
  // text) changes — state messages arrive on every poll while content
  // streams, and rebuilding on each one would close an open dropdown
  // mid-selection. Otherwise just keep the selection following the
  // server's current window. An error state (e.g. "pane not found")
  // renders as a selected placeholder option ABOVE the window list, so
  // the error stays visible while the user can still switch to a window
  // whose pane exists.
  function syncWindows(m) {
    var wins = m.windows || [];
    var err = m.err || '';
    var ver = err + '|';
    for (var i = 0; i < wins.length; i++) ver += wins[i].id + ':' + wins[i].title + ';';
    if (ver === lastWinVer) {
      if (m.windowId) { status.value = m.windowId; lastWinId = m.windowId; }
      return;
    }
    lastWinVer = ver;
    var dup = {};
    for (var i = 0; i < wins.length; i++) dup[wins[i].title] = (dup[wins[i].title] || 0) + 1;
    status.length = 0;
    if (err) {
      var pe = document.createElement('option');
      pe.value = '';
      pe.textContent = '! ' + err;
      status.appendChild(pe);
    }
    for (var i = 0; i < wins.length; i++) {
      var o = document.createElement('option');
      o.value = wins[i].id;
      // Disambiguate same-titled windows with a short id suffix.
      o.textContent = wins[i].title + (dup[wins[i].title] > 1 ? ' (' + String(wins[i].id).slice(-6) + ')' : '');
      status.appendChild(o);
    }
    // The special Workspaces entry always trails the window list: selecting
    // it leaves the mirror page for the workspace list (see the change
    // handler). It is static, so it never affects the ver rebuild trigger.
    var wo = document.createElement('option');
    wo.value = '__workspaces__';
    wo.textContent = '— Workspaces…';
    status.appendChild(wo);
    if (err) status.value = '';
    else if (m.windowId) { status.value = m.windowId; lastWinId = m.windowId; }
  }

  function connect() {
    var proto = location.protocol === 'https:' ? 'wss' : 'ws';
    // A ?ws= tab forwards its target workspace in the WS URL: the server
    // resolves it to the live window before the first snapshot, so this
    // tab's first paint is already the requested window.
    var wurl = proto + '://' + location.host + '/ws';
    if (pendingWs) wurl += '?ws=' + encodeURIComponent(pendingWs);
    ws = new WebSocket(wurl);
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
      if (m.err) {
        // An error state still carries the live window set: keep the
        // picker in sync (error as the selected placeholder) so a missing
        // pane in one window does not hide the other windows.
        if (m.windows && m.windows.length) syncWindows(m);
        else setStatus('! ' + m.err);
        return;
      }
      if (m.scroll) lastScroll = m.scroll;
      lastScrollPath = m.scrollPath || null;
      lastNestedScrolls = m.nestedScrolls || null;
      lastCursorChar = (typeof m.cursorChar === 'number') ? m.cursorChar : -1;
      syncWindows(m);
      // First good state after (re)connect: if this tab returned from the
      // workspaces page with a remembered window that still exists, ask
      // the server to mirror it again. A ?ws= tab skips this: the server
      // already applied that explicit selection at connect time.
      if (!restoredWin) {
        restoredWin = true;
        if (!pendingWs) {
          var saved = '';
          try { saved = localStorage.getItem('mirrorWin') || ''; } catch (e) {}
          if (saved) {
            var wins2 = m.windows || [];
            var found = false;
            for (var i = 0; i < wins2.length; i++) {
              if (wins2[i].id === saved) { found = true; break; }
            }
            if (found) send({ type: 'window', id: saved });
            try { localStorage.removeItem('mirrorWin'); } catch (e) {}
          }
        }
      }
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
      // Re-apply the live content paddings before the caret is re-seated,
      // so the caret's top measurement sees the padded layout. The re-flow
      // runs AFTER the fit (the fit reads the live inline line geometry,
      // which the regrouped blocks would no longer report) and BEFORE the
      // caret re-seating (which walks the regrouped text).
      fitInputEditor();
      reflowInputLines(m.inputBreaks);
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
    // The virtualized rows container is diffed by row identity (data-index)
    // instead of by position: when the rendered window shifts, rows enter at
    // one edge and leave at the other, and positional matching would replace
    // the whole window wholesale (flicker + churn). The render-distance diff
    // reuses each row element in place and only adds/discards the rows at the
    // edges.
    if (String(oldEl.className).indexOf('monaco-list-rows') >= 0 &&
        String(newEl.className).indexOf('monaco-list-rows') >= 0) {
      rowDiff(oldEl, newEl);
      return;
    }
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

  // Render-distance diff for the virtualized rows container. Live rows carry
  // a stable data-index (their index in the full list) that survives
  // re-renders, so when the rendered window shifts, every row that is still
  // rendered keeps its element in place, entering rows are adopted from the
  // fresh template, and leaving rows are discarded. Rows without a
  // data-index have no identity to match on, so fall back to the positional
  // diff (the old behavior).
  function rowDiff(oldRows, newRows) {
    var oldMap = {};
    var kids = oldRows.childNodes;
    var i, d;
    var anyIndexed = false;
    for (i = 0; i < kids.length; i++) {
      if (kids[i].nodeType !== 1) continue;
      d = kids[i].getAttribute('data-index');
      if (d !== null) {
        anyIndexed = true;
        if (oldMap[d] === undefined) oldMap[d] = kids[i];
      }
    }
    if (!anyIndexed) { patchChildren(oldRows, newRows); return; }
    var desired = [];
    var nk = newRows.childNodes;
    for (i = 0; i < nk.length; i++) {
      var r = nk[i];
      if (r.nodeType !== 1) { desired.push(r); continue; }
      d = r.getAttribute('data-index');
      var old = (d !== null) ? oldMap[d] : undefined;
      if (old !== undefined) {
        delete oldMap[d];
        patchNode(old, r); // patch in place; the element keeps its identity
        desired.push(old);
      } else {
        desired.push(r); // entering row: adopt the fresh element
      }
    }
    setChildren(oldRows, desired);
  }

  // Replace parent's children with exactly the desired nodes, in order,
  // moving them into place rather than cloning. Nodes already in place are
  // never touched, so the DOM churn is limited to the entering rows.
  function setChildren(parent, desired) {
    var i, j;
    for (i = 0; i < desired.length; i++) desired[i]._mirrorDesired = true;
    for (i = parent.childNodes.length - 1; i >= 0; i--) {
      if (!parent.childNodes[i]._mirrorDesired) parent.removeChild(parent.childNodes[i]);
    }
    for (i = 0; i < desired.length; i++) {
      var el = desired[i];
      if (el.parentNode === parent) continue;
      var ref = null;
      for (j = i + 1; j < desired.length; j++) {
        if (desired[j].parentNode === parent) { ref = desired[j]; break; }
      }
      if (ref) parent.insertBefore(el, ref); else parent.appendChild(el);
    }
    for (i = 0; i < desired.length; i++) delete desired[i]._mirrorDesired;
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

  // The live editor's inline height includes Monaco's content paddings:
  // the first .view-line's inline top is the top padding, and the editor's
  // inline height minus the live lines' total height is the bottom padding
  // (an empty live input: 44 = 12 + 20 + 12). The mirror's flow layout
  // (the responsive CSS puts the layers back in flow with height:auto)
  // drops those paddings, so an empty input renders 24px shorter than
  // live. Re-apply them on .view-lines, derived from the live inline
  // geometry: size-independent (they follow the font metrics, not the pane
  // width), so the mirror's auto height matches live line-for-line.
  function fitInputEditor() {
    var box = pane.querySelector('.chat-input-container');
    if (!box) return;
    var ed = box.querySelector('.interactive-input-editor .monaco-editor') || box.querySelector('.monaco-editor');
    if (!ed) return;
    var vl = ed.querySelector('.view-lines');
    if (!vl) return;
    var lines = vl.querySelectorAll('.view-line');
    if (!lines.length) return;
    var f = lines[0];
    var padTop = parseFloat(f.style.top) || 0;
    var lineH = parseFloat(f.style.height) || 0;
    var liveH = parseFloat(ed.style.height) || 0;
    var padBottom = liveH ? liveH - padTop - lines.length * lineH : 0;
    if (padBottom < 0) padBottom = 0;
    if (vl.style.paddingTop !== padTop + 'px') vl.style.paddingTop = padTop + 'px';
    if (vl.style.paddingBottom !== padBottom + 'px') vl.style.paddingBottom = padBottom + 'px';
  }

  // The live view lines are one block per LIVE visual line: a long text
  // line is split into several .view-line blocks at the live wrap width.
  // The mirror re-flows the input at its OWN width, so those live soft
  // wraps must not survive as block boundaries — each would re-wrap
  // again, leaving ragged tails. reflowInputLines merges the segments
  // joined by a SOFT boundary (breaks[i] === false) into one block and
  // keeps the hard-newline blocks separate. Character order is preserved,
  // so the cursorChar re-seating and the click character mapping stay
  // valid. The merge reuses each group's first line element (its inline
  // line-height survives). The incremental DOM patch self-heals the
  // regrouped structure on the next HTML update: the positional diff
  // trims a group's surplus spans and appends the missing lines with
  // their fresh text.
  function reflowInputLines(breaks) {
    var box = pane.querySelector('.chat-input-container');
    if (!box) return;
    var ed = box.querySelector('.interactive-input-editor .monaco-editor') || box.querySelector('.monaco-editor');
    if (!ed) return;
    var vl = ed.querySelector('.view-lines');
    if (!vl) return;
    // Static snapshot: the rebuild below detaches these elements.
    var lines = Array.prototype.slice.call(vl.querySelectorAll('.view-line'));
    if (lines.length < 2 || !breaks || breaks.length !== lines.length - 1) return;
    // Group indices: a soft boundary (false) joins the next line to the
    // current group; a hard boundary (true) starts a new group.
    var groups = [];
    var cur = [0];
    for (var i = 0; i < breaks.length; i++) {
      if (breaks[i]) { groups.push(cur); cur = [i + 1]; }
      else cur.push(i + 1);
    }
    groups.push(cur);
    if (groups.length === lines.length) return; // all hard: one block per line already
    vl.innerHTML = '';
    for (var g = 0; g < groups.length; g++) {
      var head = lines[groups[g][0]];
      for (var j = 1; j < groups[g].length; j++) {
        var l = lines[groups[g][j]];
        while (l.firstChild) head.appendChild(l.firstChild);
      }
      vl.appendChild(head);
    }
  }

  // The live cursor carries the LIVE layout's inline top/left, but the
  // mirror re-wraps the input text at its own width (the responsive CSS in
  // #mirror-css), so the live position no longer matches the mirror's text.
  // lastCursorChar (the cursor's character index, computed live) identifies
  // the caret size-independently: walk the mirror's view-lines text nodes,
  // find the node holding that character, and measure it with a Range.
  function repositionCursor() {
    if (!(lastCursorChar >= 0)) return;
    var box = pane.querySelector('.chat-input-container');
    if (!box) return;
    var ed = box.querySelector('.interactive-input-editor .monaco-editor') || box.querySelector('.monaco-editor');
    if (!ed) return;
    var cur = ed.querySelector('.cursor');
    if (!cur) return;
    var vlines = ed.querySelector('.view-lines');
    if (!vlines) return;
    // Text nodes in document order (spans may nest).
    var nodes = [];
    (function w(node) {
      for (var i = 0; i < node.childNodes.length; i++) {
        var c = node.childNodes[i];
        if (c.nodeType === 3) nodes.push(c);
        else if (c.nodeType === 1) w(c);
      }
    })(vlines);
    if (!nodes.length) return; // empty input: nothing to re-seat on
    var n = lastCursorChar;
    var tn = null, off = 0;
    var acc = 0;
    for (var i = 0; i < nodes.length; i++) {
      var l = nodes[i].length;
      if (n === 0 && acc === 0) {
        // Caret at the very start: the first non-empty text node.
        if (l > 0) { tn = nodes[i]; off = 0; break; }
        continue;
      }
      if (n > acc && n <= acc + l) { tn = nodes[i]; off = n - acc; break; }
      acc += l;
    }
    if (!tn) {
      // The caret is past the last character: the last text node's end.
      tn = nodes[nodes.length - 1];
      off = tn.length;
    }
    if (!tn || tn.length === 0) return;
    var range = document.createRange();
    if (off >= tn.length) {
      // The caret sits right after this character: use its right edge.
      range.setStart(tn, tn.length - 1);
      range.setEnd(tn, tn.length);
    } else {
      range.setStart(tn, off);
      range.setEnd(tn, off + 1);
    }
    var r = range.getBoundingClientRect();
    if (!r.width && !r.height) return;
    var er = ed.getBoundingClientRect();
    var caretX = (off >= tn.length) ? r.right : r.left;
    cur.style.left = Math.round(caretX - er.left) + 'px';
    cur.style.top = Math.round(r.top - er.top) + 'px';
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
    // The patch just applied the live cursor's inline top/left; re-seat the
    // caret on its character in the mirror's own (reflowed) layout.
    repositionCursor();
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
  // The mirror lays its rows out in normal (static) flow, so a row's content
  // y (the scrollTop that puts the row's top at the viewport top) is the sum
  // of the heights of the rows above it.
  function mirrorTop(row) {
    var top = 0;
    var sib = row.previousElementSibling;
    while (sib) {
      if (sib.nodeType === 1) top += sib.offsetHeight;
      sib = sib.previousElementSibling;
    }
    return top;
  }
  // The rendered row whose data-index matches the given index (null when the
  // row is not in the current rendered window).
  function rowByDataIndex(scroller, index) {
    var rowsEl = scroller.querySelector('.monaco-list-rows');
    if (!rowsEl) return null;
    for (var i = 0; i < rowsEl.children.length; i++) {
      var row = rowsEl.children[i];
      if (row.nodeType !== 1) continue;
      if (row.getAttribute('data-index') === index) return row;
    }
    return null;
  }
  // The entry anchor for a given scrollTop: the row containing that content
  // y, plus the offset from the row's top to that y. This is how the mirror
  // remembers its viewport position relative to a message entry.
  function anchorAt(scroller, scrollTop) {
    var rowsEl = scroller.querySelector('.monaco-list-rows');
    if (!rowsEl || !rowsEl.children.length) return null;
    var y = scrollTop;
    var top = 0;
    for (var i = 0; i < rowsEl.children.length; i++) {
      var row = rowsEl.children[i];
      if (row.nodeType !== 1) continue;
      var h = row.offsetHeight;
      if (y < top + h) {
        var idx = row.getAttribute('data-index');
        if (idx === null) idx = String(i);
        return { index: idx, offset: Math.max(0, y - top) };
      }
      top += h;
    }
    // y is at/below the last row's bottom: anchor to the last row.
    var last = null;
    for (var j = rowsEl.children.length - 1; j >= 0; j--) {
      if (rowsEl.children[j].nodeType === 1) { last = rowsEl.children[j]; break; }
    }
    if (!last) return null;
    var li = last.getAttribute('data-index');
    if (li === null) li = String(rowsEl.children.length - 1);
    return { index: li, offset: Math.max(0, y - (top - last.offsetHeight)) };
  }
  // Whether live's scroller is at (or within BOTTOM_EPS of) its content
  // bottom, per the LAST state message. This is the same condition that
  // hides live's scroll-to-bottom button. Distance of live's viewport bottom
  // from live's content bottom: works for bottom-anchored lists (top = 0,
  // position in the negative offset) and top-anchored lists (offset = 0,
  // position in scrollTop). The mirror may sit at the bottom of the rows
  // live has rendered while live still has unrendered content below; only
  // when live is truly at its bottom is "follow the bottom" safe.
  function liveAtBottom() {
    if (!lastLiveScroll) return false;
    var s = lastLiveScroll;
    var distBottom = (s.scrollH - s.h) - s.top + s.offset;
    return distBottom <= BOTTOM_EPS;
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
  // The mirror's viewport is anchored to a MESSAGE ENTRY (mirrorAnchor), not
  // to live's absolute scroll position. On each state message:
  //   - pinned to the bottom (watching a stream): follow live's bottom so
  //     new tokens stay visible;
  //   - otherwise, while the anchor row is still inside live's rendered
  //     window: re-apply scrollTop = mirrorTop(anchorRow) + offset, keeping
  //     the viewport pinned to the same message across html updates. This is
  //     what removes the jitter — the viewport is never re-anchored to a live
  //     pixel position while the user is reading;
  //   - otherwise (first state, window switch, scroller node replaced, or the
  //     anchor row left the rendered window): re-anchor to live's viewport,
  //     keeping the end of the live viewport the list's anchoring dictates
  //     (bottom for the chat list, top for the session picker), and derive
  //     a fresh anchor.
  function restoreScroll(m) {
    if (!m.scroll || !m.scrollPath) return;
    lastLiveScroll = m.scroll;
    var el = pathEl(m.scrollPath);
    if (!el) return;
    el.scrollLeft = m.scroll.left;
    var max = el.scrollHeight - el.clientHeight;
    // A replaced scroller node or a window switch invalidates the anchor.
    var isReset = (el !== lastAnchoredEl || m.windowId !== lastAnchoredWin);
    if (isReset) mirrorAnchor = null;
    lastAnchoredEl = el;
    lastAnchoredWin = m.windowId;
    // Pinned to the bottom (and not a fresh anchor): follow live's bottom so
    // streamed tokens stay visible. Stay pinned only while live is still at
    // its bottom; if live scrolled away, drop the pin and use the anchor.
    if (pinnedToBottom && !isReset) {
      el.scrollTop = max;
      mirrorAnchor = anchorAt(el, max);
      pinnedToBottom = liveAtBottom();
      return;
    }
    // Anchor row still inside the rendered window: keep the viewport pinned
    // to that entry (no re-anchor to a live pixel position -> no jitter).
    if (mirrorAnchor) {
      var row = rowByDataIndex(el, mirrorAnchor.index);
      if (row) {
        var keep = mirrorTop(row) + mirrorAnchor.offset;
        el.scrollTop = Math.max(0, Math.min(max, keep));
        pinnedToBottom = (el.scrollTop >= max - BOTTOM_EPS) && liveAtBottom();
        return;
      }
      // The anchor row was discarded (it left the rendered window): fall
      // through and re-anchor to live.
      mirrorAnchor = null;
    }
    // No valid anchor: re-anchor to live's viewport. The mirror's content is
    // only the live list's currently rendered rows (a sliding window), not
    // the full conversation, so align the mirror's viewport with the live
    // viewport inside the rendered window. a.above / a.inside are the mirror
    // pixels of the rendered content above / inside the live viewport. The
    // live viewport's mirror height (a.inside) can exceed the mirror
    // scroller's own height (the mirror is shorter than live, or its rows
    // are taller), in which case one end of the live viewport must be cut.
    // WHICH end is a static property of the live list (m.scroll.anchorKind),
    // so it never flips between state messages (a flip would teleport the
    // viewport): bottom-anchored lists (the chat list) keep the BOTTOM of
    // the live viewport — the hidden sliver is its top, reachable by
    // scrolling up; top-anchored lists (the agent-sessions picker) keep the
    // TOP, so returning to the list at its top shows the first row.
    var keepTop = m.scroll.anchorKind === 'top';
    var target = m.scroll.top;
    var a = alignToLiveViewport(el, m.scroll, m.scrollRows);
    if (a != null) {
      target = keepTop ? a.above : a.above + Math.max(0, a.inside - el.clientHeight);
    } else if (!keepTop) {
      var distBottom = (m.scroll.scrollH - m.scroll.h) + m.scroll.offset;
      target = distBottom <= 0 ? max : max - distBottom;
    }
    el.scrollTop = Math.max(0, Math.min(max, target));
    mirrorAnchor = anchorAt(el, el.scrollTop);
    pinnedToBottom = (el.scrollTop >= max - BOTTOM_EPS) && liveAtBottom();
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
  // Caret character index (count of characters before the caret) at a click
  // point inside the chat input editor. The mirror re-wraps the input text
  // at its own width, so a click on a wrapped line has no live pixel
  // equivalent: the element-identity mapping (DOM path + relative position)
  // lands on the wrong character. The character index is size-independent,
  // so the server re-seats it on the live layout (cdp.EvalCharPoint).
  // The scan measures every character's box (a Range per character) and
  // picks the boundary closest to the point — Monaco's own caret rule.
  // Scanning the boxes (instead of trusting the element under the point)
  // also covers clicks on the IME textarea overlay, which follows the live
  // caret and sits ON TOP of the editor's text. Returns -1 outside the
  // editor box (toolbar/attachment clicks keep the identity mapping).
  function inputCharAt(target, cx, cy) {
    var box = (target && target.closest) ? target.closest('.chat-input-container') : null;
    if (!box) return -1;
    var ed = box.querySelector('.interactive-input-editor .monaco-editor') || box.querySelector('.monaco-editor');
    if (!ed) return -1;
    var er = ed.getBoundingClientRect();
    if (cx < er.left || cx > er.right || cy < er.top || cy > er.bottom) return -1;
    var vlines = ed.querySelector('.view-lines');
    if (!vlines) return -1;
    var nodes = [];
    (function w(n) {
      for (var i = 0; i < n.childNodes.length; i++) {
        var c = n.childNodes[i];
        if (c.nodeType === 3) nodes.push(c);
        else if (c.nodeType === 1) w(c);
      }
    })(vlines);
    var total = 0;
    for (var i = 0; i < nodes.length; i++) total += nodes[i].length;
    if (!total) return 0; // empty editor: caret at the start
    var best = 0, bestD = Infinity, acc = 0;
    for (var i = 0; i < nodes.length; i++) {
      var tn = nodes[i], l = tn.length;
      for (var o = 0; o < l; o++) {
        var range = document.createRange();
        range.setStart(tn, o);
        range.setEnd(tn, o + 1);
        var r = range.getBoundingClientRect();
        var dx = cx < r.left ? r.left - cx : (cx > r.right ? cx - r.right : 0);
        var dy = cy < r.top ? r.top - cy : (cy > r.bottom ? cy - r.bottom : 0);
        var d = dx * dx + dy * dy;
        if (d < bestD) {
          bestD = d;
          // Nearest boundary of the character: its left edge when the point
          // is in the left half, its right edge otherwise.
          best = (cx <= (r.left + r.right) / 2) ? acc + o : acc + o + 1;
        }
      }
      acc += l;
    }
    return best;
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
  // window to mirror from now on. The special Workspaces entry instead
  // leaves the mirror page for the workspace list, remembering this tab's
  // window so returning restores it.
  status.addEventListener('change', function () {
    if (status.value === '__workspaces__') {
      if (lastWinId) {
        try { localStorage.setItem('mirrorWin', lastWinId); } catch (e) {}
      }
      location.href = '/workspaces';
      return;
    }
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
    var msg = { type: 'mouse', kind: 'pressed', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail, anchorPath: anchorPath };
    var ich = inputCharAt(e.target, e.clientX, e.clientY);
    if (ich >= 0) msg.char = ich;
    send(msg);
  });
  pane.addEventListener('mouseup', function (e) {
    e.preventDefault();
    var c = paneCoords(e);
    var ei = elemInfo(e.target, e.clientX, e.clientY);
    var msg = { type: 'mouse', kind: 'released', x: c.x, y: c.y, path: ei.path, relX: ei.relX, relY: ei.relY, button: btn(e), buttons: e.buttons, clickCount: e.detail };
    // Released lands on the SAME live point as pressed (the character
    // re-seated live), so Monaco sees a click, not a drag.
    var ich = inputCharAt(e.target, e.clientX, e.clientY);
    if (ich >= 0) msg.char = ich;
    send(msg);
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
      // The local move is the user's intent: record the entry anchor and the
      // pinned-to-bottom flag so the next state message maintains THIS
      // position instead of re-anchoring to live's pixel position (the
      // re-anchor is what caused the jitter). Only the main scroller has an
      // anchor — restoreScroll re-anchors that element on every state message;
      // other scrollers (the reasoning-trace list) are kept by
      // restoreNestedScrolls, and their rows' data-indexes must not leak
      // into the main anchor. Pinned requires live to be at ITS bottom too:
      // the mirror's max may only be the bottom of the rendered window, with
      // unrendered messages still below in live.
      var main = (lastScrollPath != null) ? pathEl(lastScrollPath) : null;
      if (local === main) {
        mirrorAnchor = anchorAt(local, local.scrollTop);
        pinnedToBottom = (local.scrollTop >= max - BOTTOM_EPS) && liveAtBottom();
      }
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
      // Same anchor maintenance as the wheel handler (main scroller only,
      // and pinned requires live at its bottom, not just the mirror's max).
      var main = (lastScrollPath != null) ? pathEl(lastScrollPath) : null;
      if (local === main) {
        mirrorAnchor = anchorAt(local, local.scrollTop);
        pinnedToBottom = (local.scrollTop >= max - BOTTOM_EPS) && liveAtBottom();
      }
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
  // A viewport resize re-wraps the reflowed chat-input text, moving the
  // caret's character to a new pixel position without any state message.
  window.addEventListener('resize', repositionCursor);
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
