module.exports = (selectors) => {
  let el = null;
  for (const sel of selectors) {
    try { el = document.querySelector(sel); } catch (e) {}
    if (el) break;
  }
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
    const cands = el.querySelectorAll('.monaco-list > .monaco-scrollable-element');
    let best = 0;
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
  }

  let scrollPath = null;
  try {
    if (sc === el) {
      scrollPath = [];
    } else {
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
    const rc = sc.querySelector('.monaco-list-rows');
    if (rc) {
      scrollOffset = rc.offsetTop;
      // Each rendered row's [offsetTop, offsetHeight] in full-content
      // coordinates. The rows container is full-content-sized and its own
      // offsetTop encodes the scroll position, so a row's offsetTop within it
      // is its position in the full conversation (0 = top). The mirror needs
      // this because its content is only the currently rendered rows (a
      // sliding window), not the full conversation.
      if (rc.children.length) {
        scrollRows = [];
        for (const r of rc.children) scrollRows.push(r.offsetTop, r.offsetHeight);
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

  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
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
  const fp = el.innerText.length + ':' + el.childElementCount + ':' + model.length + ':' + themeInd + ':' + popFP;
  return {
    fp: fp,
    cssFP: cssFP,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, scrollH: sc.scrollHeight || 0, offset: scrollOffset, w: sc.clientWidth, h: sc.clientHeight },
    scrollPath: scrollPath,
    nestedScrolls: nestedScrolls,
    scrollRows: scrollRows,
  };
};
