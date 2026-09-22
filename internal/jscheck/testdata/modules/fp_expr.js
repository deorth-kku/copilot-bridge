module.exports = (selectors) => {
  
  const findRoot = (sels) => {
    for (const sel of sels) {
      try { const e = document.querySelector(sel); if (e) return e; } catch (err) {}
    }
    return null;
  };

  const el = findRoot(selectors);
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();

  let sc = null;
  try {
    // Only the DIRECT-CHILD scroller of a .monaco-list that is actually
    // visible:
    // - the chat input's own Monaco editor also has a
    //   .monaco-scrollable-element, but it reports a sentinel scrollHeight
    //   (~2^24) that would always win the max-room comparison;
    // - chat messages embed Monaco editors (code blocks) INSIDE list rows,
    //   so their scrollers are .monaco-list DESCENDANTS carrying the same
    //   sentinel scrollHeight — a descendant match lets them win whenever
    //   a code-block editor is rendered/expanded;
    // - messages embed nested lists (file-review widgets, collapsed steps)
    //   whose scrollers are hidden (clientHeight 0) but still report large
    //   scrollHeights.
    // best starts at -1 so a VISIBLE scroller with zero room (the content
    // fits exactly) is still selected: the mirror needs its path and row
    // geometry even when live cannot scroll it, otherwise the fallback
    // below would report the documentElement's state instead.
    const cands = el.querySelectorAll('.monaco-list > .monaco-scrollable-element');
    let best = -1;
    for (const c of cands) {
      const room = (c.scrollHeight || 0) - (c.clientHeight || 0);
      if (c.clientHeight > 0 && room > best) { best = room; sc = c; }
    }
  } catch (e) {}
  if (!sc) {
    sc = el;
    while (sc && sc !== document.documentElement) {
      const ov = getComputedStyle(sc).overflowY;
      if ((ov === 'auto' || ov === 'scroll') && sc.scrollHeight > sc.clientHeight) break;
      sc = sc.parentElement;
    }
    // The walk only ends AT documentElement when no real scroller exists
    // (the content fits everywhere). documentElement is not a usable
    // scroller: its geometry is the whole window's, and its
    // .monaco-list-rows query would grab an unrelated list (e.g. the
    // explorer's), polluting the mirror's offset/rows. Report "no scroller"
    // instead (sc stays null: neutral scroll state, null path).
    if (sc === document.documentElement) sc = null;
  }
  // anchorKind: how the live list encodes its scroll position. The chat
  // message list (.interactive-list) is bottom-anchored (scrollTop stays 0,
  // position in the rows' negative offsetTop); the agent-sessions picker is
  // a plain top-anchored monaco-list (position in scrollTop). The mirror
  // uses it to choose which end of the live viewport to keep when its own
  // content is shorter than the live viewport — a static property of the
  // list, so the end never flips between state messages (a flip would
  // teleport the viewport).
  let anchorKind = 'top';
  try {
    if (sc && sc.closest('.interactive-list')) anchorKind = 'bottom';
  } catch (e) {}

  let scrollPath = null;
  try {
    if (sc === el) {
      scrollPath = [];
    } else if (sc) {
      scrollPath = [];
      let n = sc;
      while (n && n !== el) {
        const p = n.parentElement;
        if (!p) { scrollPath = null; break; }
        scrollPath.unshift(Array.prototype.indexOf.call(p.children, n));
        n = p;
      }
      if (n !== el) scrollPath = null;
    }
  } catch (e) { scrollPath = null; }
  let scrollOffset = 0;
  let scrollRows = null;
  try {
    // sc is null when no real scroller exists (content fits everywhere):
    // keep the offset/rows neutral instead of querying the whole document
    // (which would grab an unrelated list's rows, e.g. the explorer's).
    if (sc) {
      const rc = sc.querySelector('.monaco-list-rows');
      if (rc) {
        scrollOffset = rc.offsetTop;
        // Each rendered row's [offsetTop, offsetHeight] in full-content
        // coordinates. The rows container is full-content-sized and its own
        // offsetTop encodes the scroll position, so a row's offsetTop within
        // it is its position in the full conversation (0 = top). The mirror
        // needs this because its content is only the currently rendered
        // rows (a sliding window), not the full conversation.
        if (rc.children.length) {
          scrollRows = [];
          for (const r of rc.children) scrollRows.push(r.offsetTop, r.offsetHeight);
        }
      }
    }
  } catch (e) { scrollOffset = 0; }

  let nestedScrolls = null;
  try {
    const boxes = el.querySelectorAll('.chat-thinking-box');
    if (boxes.length) {
      nestedScrolls = [];
      for (const box of boxes) {
        const list = box.querySelector('.chat-used-context-list');
        if (!list) continue;
        let path = [];
        let n = list;
        while (n && n !== el) {
          const p = n.parentElement;
          if (!p) { path = null; break; }
          path.unshift(Array.prototype.indexOf.call(p.children, n));
          n = p;
        }
        if (!path) continue;
        nestedScrolls.push({ path: path, top: list.scrollTop || 0, scrollH: list.scrollHeight || 0, clientH: list.clientHeight || 0 });
      }
    }
  } catch (e) { nestedScrolls = null; }

  let inputEditor = null, inputFocused = false, cursorChar = -1, inputBreaks = null, inputFP = '';
  try {
    inputEditor = el.querySelector('.chat-input-container .interactive-input-editor .monaco-editor')
                 || el.querySelector('.interactive-input-editor .monaco-editor');
    if (inputEditor) {
      // Walk up from the active element (contains() is not spliced in).
      let n = document.activeElement;
      while (n) { if (n === inputEditor) { inputFocused = true; break; } n = n.parentElement; }
      const cur = inputEditor.querySelector('.cursor');
      const vlines = inputEditor.querySelector('.view-lines');
      let lines = null;
      if (vlines) {
        lines = vlines.querySelectorAll('.view-line');
        // inputFP: the editor width plus each line's character count. A
        // live re-wrap (window resize) changes these WITHOUT changing the
        // pane's innerText, yet it changes the break structure below, so
        // both must be part of the fingerprint.
        try {
          inputFP = Math.round(inputEditor.getBoundingClientRect().width) + ':';
          for (let i = 0; i < lines.length; i++) {
            inputFP += lines[i].textContent.length;
            if (i < lines.length - 1) inputFP += ',';
          }
        } catch (e) {}
        // inputBreaks: one entry per boundary between consecutive view
        // lines; true = a hard newline (a text-line end), false = a soft
        // wrap. The mirror re-groups the live view lines across the soft
        // boundaries so the text re-flows continuously at the mirror
        // width. A hard line can never exceed the live wrap width, and a
        // wrapped line's non-final segments always reach it, so a line
        // whose text falls short of the widest line by more than one
        // character was not wrapped: the boundary after it is hard. A
        // hard line ending exactly at the wrap column is indistinguishable
        // from a wrapped segment in the DOM (the rare miss).
        if (lines.length > 1) {
          const widths = [];
          let maxW = 0, wi = 0;
          for (let i = 0; i < lines.length; i++) {
            const range = document.createRange();
            range.selectNodeContents(lines[i]);
            const rr = range.getBoundingClientRect();
            widths.push(rr.width);
            if (rr.width > maxW) { maxW = rr.width; wi = i; }
          }
          if (maxW > 0) {
            // One character's width, from the widest line's first
            // character (exact for the chat input's monospace font).
            let wmax = 0;
            let fn = null;
            (function w(node) {
              if (fn) return;
              if (node.nodeType === 3) { if (node.textContent.length) { fn = node; return; } return; }
              for (let i = 0; i < node.childNodes.length; i++) w(node.childNodes[i]);
            })(lines[wi]);
            if (fn) {
              const cr = document.createRange();
              cr.setStart(fn, 0);
              cr.setEnd(fn, 1);
              wmax = cr.getBoundingClientRect().width;
            }
            const thr = maxW - wmax;
            inputBreaks = [];
            for (let i = 0; i < lines.length - 1; i++) inputBreaks.push(widths[i] <= thr);
          }
        }
      }
      if (cur && vlines && lines) {
        if (lines.length) {
          const cTop = parseFloat(cur.style.top) || 0;
          const cLeft = parseFloat(cur.style.left) || 0;
          // Line metrics from the first view-line: its inline top is the
          // content top padding, its inline height the line height.
          const f = lines[0];
          const padTop = parseFloat(f.style.top) || 0;
          const lineH = parseFloat(f.style.height) || 20;
          let li = Math.round((cTop - padTop) / lineH);
          if (li < 0) li = 0;
          if (li > lines.length) li = lines.length;
          let idx = 0;
          for (let i = 0; i < li; i++) idx += lines[i].textContent.length;
          if (li < lines.length) {
            // Characters of the cursor's own line before the caret: each
            // direct child span is one text run (absolute left + width in
            // the live layout). A caret inside a run is located by its
            // per-character Range boundaries, NOT proportionally: plain
            // input text is one span per line, and CJK glyphs fall back
            // to a ~2x-wide font, so a uniform-width (proportional)
            // estimate skews the index on mixed CJK/Latin text.
            const line = lines[li];
            const lr = line.getBoundingClientRect();
            for (const s of line.children) {
              if (s.nodeType !== 1) continue;
              const t = s.textContent;
              if (!t.length) continue;
              const sr = s.getBoundingClientRect();
              if (!sr.width) continue;
              const sl = sr.left - lr.left;
              if (cLeft >= sl + sr.width) idx += t.length;
              else if (cLeft > sl) {
                // The caret is inside this run. leftOf(i) is the left edge
                // of character i (i == length: the run's right edge); the
                // boundaries are monotonic, so binary-search the largest
                // one at/before cLeft and compare it with its successor.
                const tn = (function findText(node) {
                  if (node.nodeType === 3) return node;
                  for (let i = 0; i < node.childNodes.length; i++) {
                    const r = findText(node.childNodes[i]);
                    if (r) return r;
                  }
                  return null;
                })(s);
                if (tn) {
                  const txt = tn.textContent;
                  // Line-relative: the Range rect is in viewport coordinates,
                  // but cLeft (the cursor's inline left) is relative to the
                  // editor's content origin, i.e. the line's left edge.
                  const leftOf = (i) => {
                    if (i >= txt.length) return sl + sr.width;
                    const cr = document.createRange();
                    cr.setStart(tn, i);
                    cr.setEnd(tn, i + 1);
                    return cr.getBoundingClientRect().left - lr.left;
                  };
                  let lo = 0, hi = txt.length;
                  while (lo < hi) {
                    const mid = (lo + hi + 1) >> 1;
                    if (leftOf(mid) <= cLeft + 0.5) lo = mid;
                    else hi = mid - 1;
                  }
                  let k = lo;
                  if (lo < txt.length &&
                      Math.abs(leftOf(lo + 1) - cLeft) < Math.abs(leftOf(lo) - cLeft)) k = lo + 1;
                  idx += k;
                }
              }
              else break;
            }
          }
          cursorChar = idx;
        }
      }
    }
  } catch (e) {}

  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}

  const scroll = sc ? { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, scrollH: sc.scrollHeight || 0, offset: scrollOffset, w: sc.clientWidth, h: sc.clientHeight, anchorKind: anchorKind }
    : { left: 0, top: 0, scrollH: el.scrollHeight || 0, offset: 0, w: el.clientWidth, h: el.clientHeight, anchorKind: anchorKind };

  const model = (el.querySelector('.model-picker-name') || { textContent: '' }).textContent;
  // Include a cheap theme indicator so a theme change (which does not change
  // text content) still triggers a full re-extract of themeVars.
  let themeInd = '';
  try { themeInd = getComputedStyle(el).getPropertyValue('--vscode-foreground').trim(); } catch (e) {}
  // The context view (popup menus) sits OUTSIDE the pane root, so the pane's
  // own content never changes when a popup opens, closes, or moves — fold a
  // cheap popup fingerprint into fp so any of those triggers a re-extract.
  // As in htmlExpr, pick the visible .context-view (there can be several:
  // the hidden menu one plus hover popups such as the context-usage widget).
  let popFP = '';
  try {
    let cv = null;
    for (const c of document.querySelectorAll('.context-view')) {
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) cv = c;
    }
    if (cv) {
      const cr = cv.getBoundingClientRect();
      popFP = cv.innerText.length + ':' + cv.childElementCount + ':' +
        Math.round(cr.left) + ':' + Math.round(cr.top);
    }
  } catch (e) {}
  // Chat-input cursor state, folded into the fp so caret moves and focus
  // changes trigger a full re-extract: neither alters innerText or the
  // child count (the cursor's position is an inline style, and focus is a
  // class + activeElement). The cursor's visibility is deliberately NOT
  // included: live toggles it every 500ms (the JS-driven blink), and it
  // would force a full extract twice a second.
  let curFP = '';
  try {
    if (inputEditor) {
      const cur = inputEditor.querySelector('.cursor');
      curFP = (cur ? (cur.style.top || '') + '|' + (cur.style.left || '') + '|' + (cur.style.height || '') : 'x') +
        '|' + inputEditor.querySelectorAll('.cursor').length +
        '|' + (inputFocused ? 1 : 0);
    }
  } catch (e) {}
  const fp = el.innerText.length + ':' + el.childElementCount + ':' + model.length + ':' + themeInd + ':' + popFP + ':' + curFP + ':' + inputFP;
  return {
    fp: fp,
    cssFP: cssFP,
    inputFocused: inputFocused,
    cursorChar: cursorChar,
    inputBreaks: inputBreaks,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: scroll,
    scrollPath: scrollPath,
    nestedScrolls: nestedScrolls,
    scrollRows: scrollRows,
  };
};
