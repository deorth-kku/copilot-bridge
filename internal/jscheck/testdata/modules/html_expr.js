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

  let inputEditor = null, inputFocused = false;
  try {
    inputEditor = el.querySelector('.chat-input-container .interactive-input-editor .monaco-editor')
                 || el.querySelector('.interactive-input-editor .monaco-editor');
    if (inputEditor) {
      // Walk up from the active element (contains() is not spliced in).
      let n = document.activeElement;
      while (n) { if (n === inputEditor) { inputFocused = true; break; } n = n.parentElement; }
    }
  } catch (e) {}

  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
  const rootStyle = ((document.documentElement.getAttribute('style') || '') + ';' +
    (document.body ? (document.body.getAttribute('style') || '') : '')).trim();
  // Capture every CSS custom property computed at the pane root. VS Code
  // defines its palette as hundreds of --vscode-* variables on ancestor
  // selectors (e.g. .monaco-workbench) that are absent from the extracted
  // subtree; carrying the computed values over is what makes themed colors
  // render correctly in the mirror.
  let themeVars = '';
  let themeBg = '';
  try {
    const cs = getComputedStyle(el);
    for (let i = 0; i < cs.length; i++) {
      const p = cs[i];
      if (p.indexOf('--') !== 0) continue;
      const v = cs.getPropertyValue(p).trim();
      if (v) themeVars += p + ': ' + v + ';';
    }
    // Also carry over the inherited text-metric properties computed at the
    // pane root. VS Code sets font-family / font-size / line-height on
    // ancestor selectors (e.g. .monaco-workbench) that are absent from the
    // extracted subtree; without them the mirror falls back to the browser
    // default font and line-height, which vertically compresses the content
    // and offsets every forwarded click. These are inherited properties, so
    // applying them to the mirror root reproduces the live text environment.
    const textProps = ['font-family', 'font-size', 'line-height', 'font-weight', 'font-style', 'letter-spacing'];
    for (const p of textProps) {
      const v = cs.getPropertyValue(p).trim();
      if (v) themeVars += p + ': ' + v + ';';
    }
    // Carry over the pane root's effective background color: the live page
    // paints the pane's visible background with an ancestor-scoped rule
    // (e.g. the side-bar part) that the extracted subtree cannot match, so
    // pin the computed value onto the mirror root (#pane via themeVars, the
    // page body via --mirror-bg).
    let bgEl = el;
    while (bgEl) {
      const b = getComputedStyle(bgEl).backgroundColor.trim();
      if (b && b !== 'rgba(0, 0, 0, 0)') { themeBg = b; break; }
      bgEl = bgEl.parentElement;
    }
    if (themeBg) themeVars += 'background-color: ' + themeBg + ';';
  } catch (e) {}
  // Context views (popup menus, dropdowns, tooltips) render OUTSIDE the
  // pane root — in .context-view containers that are children of the
  // workbench root — so the pane subtree alone never contains them. There
  // can be MORE THAN ONE: the action-list menus live in one (which stays in
  // the DOM, hidden, between openings) while hover-style popups such as the
  // context-usage "Session Info" widget render in a separate one. Capture
  // the currently visible one (the last visible in DOM order, i.e. the one
  // painted on top), positioned relative to the pane root, so the mirror can
  // render it in the same pane-relative spot.
  let popup = null;
  try {
    let cv = null;
    for (const c of document.querySelectorAll('.context-view')) {
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) cv = c;
    }
    if (cv) {
      const cr = cv.getBoundingClientRect();
      popup = {
        html: cv.outerHTML,
        left: cr.left - r.left, top: cr.top - r.top,
        width: cr.width, height: cr.height,
      };
    }
  } catch (e) {}
  return {
    html: el.outerHTML,
    rootStyle: rootStyle,
    themeVars: themeVars,
    themeBg: themeBg,
    inputFocused: inputFocused,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, scrollH: sc.scrollHeight || 0, offset: scrollOffset, w: sc.clientWidth, h: sc.clientHeight },
    scrollPath: scrollPath,
    scrollRows: scrollRows,
    nestedScrolls: nestedScrolls,
    cssFP: cssFP,
    popup: popup,
  };
};
