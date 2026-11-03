(selectors) => {
  const P = window.__copilotBridgeProbes;
  const el = P.findRoot(selectors);
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
  const m = P.measureScroll(el);
  const { scrollPath, scrollRows, nestedScrolls } = m;
  const { inputFocused } = P.inputEditor(el);
  const cssFP = P.cssFP();
  const scroll = P.scrollObj(el, m);
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
  // can be MORE THAN ONE visible at once: the action-list menus live in one
  // while hover-style popups (tooltips, the context-usage "Session Info"
  // widget) render in separate containers. Capture ALL currently visible
  // ones, positioned relative to the pane root, so the mirror can render
  // each in the same pane-relative spot. Each carries a stable id: its
  // index in the FULL (including hidden) .context-view list. The containers
  // are persistent DOM nodes, so the id does not shift when an earlier
  // popup closes and the mirror can match its copies across frames.
  let popups = null;
  try {
    const cvs = document.querySelectorAll('.context-view');
    const visible = [];
    for (let i = 0; i < cvs.length; i++) {
      const c = cvs[i];
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) {
        visible.push({
          id: i,
          html: safeHTML(c),
          left: cr.left - r.left, top: cr.top - r.top,
          width: cr.width, height: cr.height,
        });
      }
    }
    if (visible.length > 0) popups = visible;
  } catch (e) {}
  // The modal editor (image carousel, Markdown preview, ...) renders
  // OUTSIDE the pane root in a .part.editor.modal-editor-part floating
  // overlay that blocks interaction with the rest of the workbench. Capture
  // it — including its title bar and close button — so the mirror can render
  // it as a full-viewport overlay and forward clicks (notably close) to
  // live.
  let modalEditor = null;
  try {
    const me = document.querySelector('.part.editor.modal-editor-part');
    if (me) {
      const mr = me.getBoundingClientRect();
      if (mr.width > 0 && mr.height > 0) {
        modalEditor = {
          html: safeHTML(me),
          left: mr.left, top: mr.top,
          width: mr.width, height: mr.height,
        };
      }
    }
  } catch (e) {}
  // Serialize the pane subtree with the HTML parser's structural hazards
  // escaped: <a>, <button> and <form> are emitted as x-a / x-button /
  // x-form. The HTML parser re-parents nested <a> (adoption agency) and
  // force-closes nested <button>/<form>, so a plain outerHTML round-trip
  // does NOT reproduce the live tree whenever one of these tags is nested
  // (VS Code's "Collapse Todos" <a> wraps a nested <a class="monaco-button">
  // — the clear-all button). The mirror's click mapping resolves DOM paths
  // against the LIVE tree, so the mirror's parsed tree must equal the live
  // tree; custom element names have no special parsing rules and round-trip
  // exactly. The mirror renames the placeholders back after parsing
  // (restoreEscaped in the page JS).
  function safeHTML(el) {
    var out = '';
    (function walk(n) {
      if (n.nodeType === 1) {
        var t = n.tagName.toLowerCase();
        // Emit lowercase names (HTML source form): the parser's foreign-
        // content adjustment table restores standard SVG/MathML casing.
        var name = (t === 'a' || t === 'button' || t === 'form') ? 'x-' + t : t;
        out += '<' + name;
        for (var i = 0; i < n.attributes.length; i++) {
          var a = n.attributes[i];
          // <input>'s live value is a property, not an attribute: emit it
          // once below (the attribute may be stale), so skip it here.
          if (t === 'input' && a.name === 'value') continue;
          out += ' ' + a.name + '="' + String(a.value).replace(/&/g, '&amp;').replace(/"/g, '&quot;') + '"';
        }
        // <input>/<textarea> carry their text in the value property, which
        // attributes do not reflect: emit it so the mirror's copy shows the
        // typed text (the askQuestion custom-answer box is a <textarea>;
        // without this the mirror's copy is always empty).
        if (t === 'input' && n.value) {
          out += ' value="' + String(n.value).replace(/&/g, '&amp;').replace(/"/g, '&quot;') + '"';
        }
        out += '>';
        if (t === 'textarea' && n.value) {
          out += String(n.value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
        }
        // <template> keeps its children in a separate content fragment.
        var kids = (t === 'template') ? n.content : n;
        var cs = kids.childNodes;
        for (var ci = 0; ci < cs.length; ci++) walk(cs[ci]);
        out += '</' + name + '>';
      } else if (n.nodeType === 3) {
        out += String(n.data).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      }
    })(el);
    return out;
  }
  return {
    html: safeHTML(el),
    rootStyle: rootStyle,
    themeVars: themeVars,
    themeBg: themeBg,
    inputFocused: inputFocused,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: scroll,
    scrollPath: scrollPath,
    scrollRows: scrollRows,
    nestedScrolls: nestedScrolls,
    cssFP: cssFP,
    popups: popups,
    modalEditor: modalEditor,
  };
}