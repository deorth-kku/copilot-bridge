package cdp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
)

// PaneRect is the pane root's bounding box in the VS Code viewport
// (CSS pixels). The mirror uses it to map its own coordinates back onto
// the live page when forwarding input.
type PaneRect struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// PaneScroll is the scroll state of the pane's nearest scrollable
// ancestor, used to keep the mirror's scroll position in sync.
type PaneScroll struct {
	Left    float64 `json:"left"`
	Top     float64 `json:"top"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	ScrollH float64 `json:"scrollH"`
	// Offset is the .monaco-list-rows container's offsetTop within the
	// scroll element. Bottom-anchored monaco-lists keep scrollTop at 0 and
	// encode the scroll position in this negative offset instead, so the
	// mirror needs it to map the live scroll state onto its own (static,
	// top-anchored) layout.
	Offset float64 `json:"offset"`
	// AnchorKind reports how the live list encodes its scroll position:
	// "bottom" for bottom-anchored monaco-lists (the chat message list,
	// .interactive-list — scrollTop stays 0, position in the rows' negative
	// offsetTop) and "top" for ordinary top-anchored lists (the
	// agent-sessions picker — position in scrollTop, offsetTop stays 0).
	// The mirror uses it to choose which end of the live viewport to keep
	// when the live viewport's mirror height exceeds the mirror scroller's
	// own height. The end is a STATIC property of the list: switching ends
	// between state messages would teleport the mirror's viewport.
	AnchorKind string `json:"anchorKind,omitempty"`
}

// NestedScroll is the scroll state of a nested scroll container inside the
// pane that the main pane scroller does not cover. Currently: the
// reasoning-trace list of a .chat-thinking-box (.chat-used-context-list,
// ~200px max-height while streaming). While the trace streams, VS Code pins
// its scrollTop to the bottom (DomScrollableElement applies
// setScrollPosition to the wrapped list element), but the extracted HTML
// carries no scrollTop, so the mirror maps the live ratio
// (Top / (ScrollH - ClientH)) onto its own copy of the list.
type NestedScroll struct {
	// Path is the DOM path from the pane root to the list element.
	Path    []int   `json:"path"`
	Top     float64 `json:"top"`
	ScrollH float64 `json:"scrollH"`
	ClientH float64 `json:"clientH"`
}

// layoutState is the pane layout metadata shared by the cheap fingerprint
// probe (FingerprintState) and the full HTML extract (HTMLState).
type layoutState struct {
	Rect PaneRect `json:"rect"`
	// Scroll is the scroll state of the pane's nearest scrollable ancestor
	// (see PaneScroll).
	Scroll PaneScroll `json:"scroll"`
	// ScrollPath identifies the measured scroll container as a DOM path from
	// the pane root (no omitempty: an empty path means "the root itself",
	// null means "no scroller").
	ScrollPath []int `json:"scrollPath"`
	// ScrollRows is each currently rendered row's [offsetTop, offsetHeight]
	// pair in full-content coordinates (see scrollPathJS). The mirror uses it
	// to align its viewport with the live viewport inside the rendered row
	// window, because the mirror's content is only that window.
	ScrollRows []float64 `json:"scrollRows,omitempty"`
	// NestedScrolls carries the scroll state of nested scroll containers the
	// main scroller does not cover (see NestedScroll).
	NestedScrolls []NestedScroll `json:"nestedScrolls,omitempty"`
}

// HTMLState is the result of ExtractHTML.
type HTMLState struct {
	Err       string `json:"err"`
	HTML      string `json:"html"`
	RootStyle string `json:"rootStyle"`
	ThemeVars string `json:"themeVars"`
	// InputFocused reports whether the chat input's Monaco editor holds the
	// page focus. The mirror uses it to decide whether to show (and blink)
	// the input cursor: live keeps the cursor hidden when the input is not
	// focused.
	InputFocused bool `json:"inputFocused"`
	// ThemeBg is the pane root's effective background color (the nearest
	// non-transparent ancestor's computed value); the mirror pins it on its
	// own root and page body.
	ThemeBg string `json:"themeBg"`
	layoutState
	CSSFP string `json:"cssFP"`
	// Popup is the currently visible context view (nil when none is open).
	Popup *PopupState `json:"popup"`
}

// CSSState is the result of ExtractCSS.
type CSSState struct {
	CSS string `json:"css"`
}

// PopupState is one visible context view (popup menu, dropdown, tooltip)
// captured alongside the pane. VS Code renders context views OUTSIDE the
// pane root — in a single .context-view container that is a child of the
// workbench root — so the pane subtree alone never contains them. Left/Top
// are the container's offset relative to the pane root's bounding box, so
// the mirror can place it in the same pane-relative spot.
type PopupState struct {
	HTML   string  `json:"html,omitempty"`
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	// Anchor is the live bounding box of the control that opened the popup
	// (the last clicked button), in the same pane-relative coordinates as
	// Left/Top. The mirror's responsive layout does not preserve live
	// pane-relative offsets, so it positions the popup by applying the
	// (popup - anchor) offset to its own copy of that control. Nil when the
	// anchor is unknown (e.g. the popup was opened via the keyboard).
	Anchor *PaneRect `json:"anchor,omitempty"`
}

// inputEditorJS is spliced into both fpExpr and htmlExpr (they are
// evaluated as separate programs, so the helper must be inlined). It
// locates the chat input's Monaco editor and reports whether it holds
// the page focus (document.activeElement inside it — with the native edit
// context that is a .native-edit-context div, not the old textarea). The
// mirror needs the focus state because live keeps the input cursor hidden
// whenever the input is not focused, and a static HTML snapshot cannot
// tell the two apart (the cursor element exists in both states). It also
// reports the cursor's CHARACTER INDEX (count of characters before the
// caret), computed from the live layout: the cursor's inline top picks the
// wrapped line, its left the offset within it. The mirror re-wraps the
// input text at its own width (responsive layout), so it needs the index —
// a size-independent identity — not the live cursor pixels.
const inputEditorJS = `
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
`

// FingerprintState is the result of Fingerprint: a cheap probe run every
// poll that avoids the costly outerHTML dump until something actually
// changed.
type FingerprintState struct {
	Err   string `json:"err"`
	FP    string `json:"fp"`
	CSSFP string `json:"cssFP"`
	// InputFocused is part of the fp (focus changes must trigger a
	// re-extract) and is reported here so the cheap probe can carry it to
	// the state message even when only focus changed.
	InputFocused bool `json:"inputFocused"`
	// CursorChar is the chat-input cursor's character index (count of
	// characters before the caret) in the live layout; -1 when the input
	// has no cursor. The mirror re-wraps the text at its own width, so it
	// re-seats the cursor by this index instead of the live pixels.
	CursorChar int `json:"cursorChar"`
	// InputBreaks is one bool per boundary between the chat input's
	// consecutive live view lines: true = hard newline (a text-line end),
	// false = soft wrap. The mirror re-groups the view lines across the
	// soft boundaries so the text re-flows continuously at the mirror
	// width. Nil when the input has fewer than two view lines.
	InputBreaks []bool `json:"inputBreaks,omitempty"`
	layoutState
}

// findRootJS is spliced into every pane-root expression (they are evaluated
// as separate programs, so the helper must be inlined). It tries the
// candidate selectors in order and returns the first match (or null).
const findRootJS = `
  const findRoot = (sels) => {
    for (const sel of sels) {
      try { const e = document.querySelector(sel); if (e) return e; } catch (err) {}
    }
    return null;
  };
`

// cssFPJS is spliced into both htmlExpr and fpExpr (same inlining
// constraint). It computes the cheap CSS fingerprint: the stylesheet count
// plus the inline <style> lengths on documentElement and body. VS Code adds
// inline <style> elements (e.g. when opening a context view), so any of
// those flips the fingerprint and triggers a CSS re-extract.
const cssFPJS = `
  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
`

// scrollObjJS is spliced into both htmlExpr and fpExpr (same inlining
// constraint). It builds the scroll state object from the measured
// scroller (sc) and the rows' offset (scrollOffset). When no real scroller
// exists (sc is null), it reports the pane root's own geometry as a neutral
// state: the mirror early-returns on the null scrollPath, so those values
// are informational only.
const scrollObjJS = `
  const scroll = sc ? { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, scrollH: sc.scrollHeight || 0, offset: scrollOffset, w: sc.clientWidth, h: sc.clientHeight, anchorKind: anchorKind }
    : { left: 0, top: 0, scrollH: el.scrollHeight || 0, offset: 0, w: el.clientWidth, h: el.clientHeight, anchorKind: anchorKind };
`

// htmlExpr extracts the pane subtree's outerHTML plus the layout metadata
// the mirror needs (bounding box, scroll, theme inline styles, and a cheap
// CSS fingerprint). It is an arrow function (NOT invoked); ExtractHTML
// passes the candidate selectors as its single argument.
const htmlExpr = `(selectors) => {
  ` + findRootJS + `
  const el = findRoot(selectors);
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
` + scrollContainerJS + inputEditorJS + cssFPJS + scrollObjJS + `
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
    scroll: scroll,
    scrollPath: scrollPath,
    scrollRows: scrollRows,
    nestedScrolls: nestedScrolls,
    cssFP: cssFP,
    popup: popup,
  };
}`

// scrollContainerJS is spliced into both htmlExpr and fpExpr (they are
// evaluated as separate programs, so the helper must be inlined). It locates
// the element that actually scrolls the active view: monaco-lists scroll
// their .monaco-scrollable-element DIRECT CHILD (overflow:hidden, scrolled
// programmatically via scrollTop), so a "walk up for overflow:auto/scroll"
// heuristic never finds it. The pane root contains TWO such lists — the chat
// message list and the agent-sessions list (session-selection view) — and
// exactly one of them is expanded at a time, so the one with the most
// scrollable room (scrollHeight - clientHeight) is the active one. The
// ancestor walk remains as a fallback for other pane shapes.
const scrollContainerJS = `
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
` + scrollPathJS

// scrollPathJS is spliced right after scrollContainerJS (same inlining
// constraint). It records the measured scroll container as a DOM path from
// the pane root, so the mirror can restore the scroll position on the SAME
// element the server measured — independent of the mirror's own size (the
// responsive layout does not preserve live pixel dimensions). null when the
// container is not a descendant of the pane root (ancestor-walk fallback).
const scrollPathJS = `
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
` + nestedScrollJS

// nestedScrollJS is spliced right after scrollPathJS (same inlining
// constraint). It records the scroll state of each nested scroll container
// the main scroller never covers — currently the reasoning-trace list of
// every .chat-thinking-box (.chat-used-context-list, ~200px max-height while
// streaming). While the trace streams, VS Code pins the list's scrollTop to
// the bottom (DomScrollableElement applies setScrollPosition to the wrapped
// list element), but the extracted HTML carries no scrollTop, so without
// this the mirror would always show the TOP of the trace. The mirror maps
// the live ratio (top / (scrollH - clientH)) onto its own copy of the list.
const nestedScrollJS = `
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
`

// fpExpr is the cheap per-poll probe: it returns a content fingerprint, a
// CSS fingerprint, and the layout metadata (bounding box + scroll) without
// dumping the pane's outerHTML.
const fpExpr = `(selectors) => {
  ` + findRootJS + `
  const el = findRoot(selectors);
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
` + scrollContainerJS + inputEditorJS + cssFPJS + scrollObjJS + `
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
}`

// healthExpr samples the live page's size and memory: total/pane DOM node
// counts, stylesheet count, and the JS heap (when the Chromium build exposes
// performance.memory). The mirror evaluates it once a minute so the log can
// correlate refresh latency with renderer pressure over long sessions.
const healthExpr = `(selectors) => {
  let heap = null;
  try { if (performance.memory) heap = { used: performance.memory.usedJSHeapSize, total: performance.memory.totalJSHeapSize }; } catch (e) {}
  ` + findRootJS + `
  const el = findRoot(selectors);
  return {
    nodes: document.getElementsByTagName('*').length,
    paneNodes: el ? el.getElementsByTagName('*').length : null,
    sheets: document.styleSheets.length,
    heap: heap,
  };
}`

// PageHealth is one live-page size/memory sample.
type PageHealth struct {
	Nodes     int  `json:"nodes"`
	PaneNodes *int `json:"paneNodes"`
	Sheets    int  `json:"sheets"`
	Heap      *struct {
		Used  float64 `json:"used"`
		Total float64 `json:"total"`
	} `json:"heap"`
}

// Health returns a DOM/JS-heap sample of the live page.
func Health(s *Session, selectors []string) (*PageHealth, error) {
	raw, err := callExpr(s, healthExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	return decodeEval[PageHealth](raw)
}

// cssExpr dumps every accessible stylesheet and rewrites each @font-face
// font URL into an inlined base64 data URI, so the returned CSS is fully
// self-contained (no external font requests). It is async because it fetches
// the font bytes in the page context. Each font URL is resolved against its
// own stylesheet's base (sheet.href) before fetching, which is required for
// VS Code's vscode-file:// resources.
const cssExpr = `async () => {
  const toB64 = (buf) => {
    let s = '';
    const bytes = new Uint8Array(buf);
    for (let i = 0; i < bytes.length; i += 0x8000) {
      s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(s);
  };
  const mime = (u) => {
    const m = u.toLowerCase();
    if (m.includes('.woff2')) return 'font/woff2';
    if (m.includes('.woff')) return 'font/woff';
    if (m.includes('.ttf')) return 'font/ttf';
    if (m.includes('.otf')) return 'font/otf';
    if (m.includes('.eot')) return 'application/vnd.ms-fontobject';
    return 'application/octet-stream';
  };
  let css = '';
  for (const sheet of document.styleSheets) {
    let rules;
    try { rules = Array.from(sheet.cssRules); } catch (e) { continue; }
    let text = rules.map(rule => rule.cssText).join('\n');
    const base = sheet.href || location.href;
    for (const m of text.matchAll(/url\(([^)]+)\)/g)) {
      const raw = m[1].replace(/^["']|["']$/g, '').split(/[?#]/)[0];
      if (!/\.(woff2?|ttf|otf|eot)([?#]|$)/i.test(raw)) continue;
      let abs;
      try { abs = new URL(raw, base).href; } catch (e) { continue; }
      let b64;
      try {
        const resp = await fetch(abs);
        if (!resp.ok) continue;
        b64 = toB64(await resp.arrayBuffer());
      } catch (e) { continue; }
      if (!b64) continue;
      const dataURI = 'data:' + mime(abs) + ';base64,' + b64;
      // Re-wrap in the ORIGINAL quotes. m[1] may carry a ?query/#hash tail
      // that was stripped from raw; splicing raw into m[1] would leave
      // that tail AFTER the base64 payload and corrupt the data URI.
      const q = /^["']/.test(m[1]) ? m[1].charAt(0) : '';
      text = text.split(m[1]).join(q + dataURI + q);
    }
    css += text + '\n';
  }
  return { css: css };
}`

// callExpr wraps an arrow-function expression in an IIFE and evaluates it,
// passing argsJSON (a JS array literal, or "" for none) as the argument.
// Runtime.evaluate does NOT invoke a bare function expression with "args",
// so the IIFE form is required.
//
// The wrapper is an async IIFE that stamps the result object with
// __cdp_ms, the page-side execution time (performance.now delta). It splits
// the total round-trip logged by Session.Call into two parts: __cdp_ms =
// probe cost on the page (DOM size / JS heap pressure), the remainder =
// CDP transport + browser main-thread queue latency.
func callExpr(s *Session, expr, argsJSON string) (jsontext.Value, error) {
	full := "(async () => { const __t0 = performance.now(); const __r = await (" + expr + ")(" + argsJSON + ");" +
		" if (__r && typeof __r === 'object') __r.__cdp_ms = Math.round((performance.now() - __t0) * 100) / 100;" +
		" return __r; })()"
	return s.Call("Runtime.evaluate", map[string]any{
		"expression":    full,
		"awaitPromise":  true,
		"returnByValue": true,
	})
}

// evalMs extracts the page-side execution time (__cdp_ms, in ms) stamped by
// callExpr into the evaluate result. Returns -1 when the field is absent
// (null result or a failed eval).
func evalMs(raw jsontext.Value) float64 {
	var v struct {
		CdpMs float64 `json:"__cdp_ms"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return -1
	}
	return v.CdpMs
}

// selectorsJSON marshals the candidate selectors as a JS array literal.
func selectorsJSON(selectors []string) string {
	b, _ := json.Marshal(selectors)
	return string(b)
}

// ExtractHTML returns the pane subtree's outerHTML plus layout metadata.
// selectors are tried in order; the first that matches wins.
func ExtractHTML(s *Session, selectors []string) (*HTMLState, error) {
	raw, err := callExpr(s, htmlExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	s.log.Debug("extractHTML", "pageMs", evalMs(raw))
	return decodeEval[HTMLState](raw)
}

// Fingerprint returns a cheap probe of the pane (content + CSS fingerprint
// plus layout metadata) without dumping the outerHTML.
func Fingerprint(s *Session, selectors []string) (*FingerprintState, error) {
	raw, err := callExpr(s, fpExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	s.log.Debug("fingerprint", "pageMs", evalMs(raw))
	return decodeEval[FingerprintState](raw)
}

// ExtractCSS returns the full workbench CSS with @font-face fonts inlined
// as base64 data URIs (self-contained).
func ExtractCSS(s *Session) (*CSSState, error) {
	raw, err := callExpr(s, cssExpr, "")
	if err != nil {
		return nil, err
	}
	s.log.Debug("extractCSS", "pageMs", evalMs(raw))
	return decodeEval[CSSState](raw)
}

// clickPointExpr resolves a DOM path (child indices from the pane root) plus a
// relative (0..1) position within the target element to an absolute live-page
// coordinate. Mapping by element identity (rather than by absolute pane offset)
// keeps forwarded clicks accurate even when the mirror's rendering is not
// pixel-identical to the live layout. A DOM path is also robust to sibling
// additions elsewhere in the tree (e.g. the conversation growing), which would
// shift a flat descendant index.
// NOTE: takes a single args array (not 4 params) so it composes with callExpr,
// which wraps the expression in an IIFE and passes one argument.
const clickPointExpr = `(args) => {
  const sels = args[0], path = args[1], rx = args[2], ry = args[3];
  ` + findRootJS + `
  let root = null;
  let p = path;
  // A path starting with -1 is rooted at the live context view (popup)
  // instead of the pane root; the remaining indices are relative to it.
  // There can be several .context-view elements (the hidden menu one plus
  // hover popups such as the context-usage widget); use the visible one.
  if (Array.isArray(p) && p.length > 0 && p[0] === -1) {
    const cvs = document.querySelectorAll('.context-view');
    for (const c of cvs) {
      const cr = c.getBoundingClientRect();
      if (cr.width > 0 && cr.height > 0) root = c;
    }
    if (root) p = p.slice(1);
  } else {
    root = findRoot(sels);
  }
  if (!root) return null;
  let el = root;
  if (Array.isArray(p)) {
    for (const i of p) {
      if (!el.children[i]) { el = null; break; }
      el = el.children[i];
    }
  }
  if (!el) el = root;
  const r = el.getBoundingClientRect();
  return { x: r.left + rx * r.width, y: r.top + ry * r.height };
}`

// EvalClickPoint resolves a DOM path (child indices from the pane root) plus a
// relative (0..1) position within the target element to an absolute live-page
// coordinate. An empty path means the root itself; navigation failure falls
// back to the root.
func EvalClickPoint(s *Session, selectors []string, path []int, relX, relY float64) (float64, float64, bool) {
	args, _ := json.Marshal([]any{selectors, path, relX, relY})
	raw, err := callExpr(s, clickPointExpr, string(args))
	if err != nil {
		return 0, 0, false
	}
	v, err := decodeEval[struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}](raw)
	if err != nil {
		return 0, 0, false
	}
	return v.X, v.Y, true
}

// charPointExpr resolves a chat-input caret character index (count of
// characters before the caret, same identity as FingerprintState.CursorChar)
// to an absolute live-page coordinate: the click point on the caret's
// boundary. The mirror re-wraps the input text at its own width, so a
// pixel mapping (path + relative position) cannot reach the wrapped lines;
// the character index is size-independent, and this eval re-seats it on the
// LIVE layout, where the measurement is exact.
// NOTE: takes a single args array (not 2 params) so it composes with
// callExpr, which wraps the expression in an IIFE and passes one argument.
const charPointExpr = `(args) => {
  const sels = args[0], char = args[1];
  ` + findRootJS + `
  const root = findRoot(sels);
  if (!root) return null;
  const ed = root.querySelector('.chat-input-container .interactive-input-editor .monaco-editor')
             || root.querySelector('.interactive-input-editor .monaco-editor');
  if (!ed) return null;
  const er = ed.getBoundingClientRect();
  const vlines = ed.querySelector('.view-lines');
  if (!vlines) return null;
  const nodes = [];
  (function w(n) { for (let i = 0; i < n.childNodes.length; i++) { const c = n.childNodes[i]; if (c.nodeType === 3) nodes.push(c); else if (c.nodeType === 1) w(c); } })(vlines);
  if (!nodes.length) {
    // Empty input: the first line's start (content top padding).
    const f = vlines.querySelector('.view-line');
    const padTop = f ? (parseFloat(f.style.top) || 0) : 0;
    const lineH = f ? (parseFloat(f.style.height) || 20) : 20;
    return { x: er.left + 4, y: er.top + padTop + lineH / 2 };
  }
  let total = 0;
  for (const n of nodes) total += n.length;
  if (char < 0) char = 0;
  if (char > total) char = total;
  // The caret sits at boundary char: click the left edge of the first
  // character for char 0, otherwise the right edge of character char-1
  // (the boundary between char-1 and char).
  let ci = char - 1;
  if (ci < 0) ci = 0;
  if (ci >= total) ci = total - 1;
  let acc = 0, tn = null, off = 0;
  for (const n of nodes) {
    const l = n.length;
    if (ci >= acc && ci < acc + l) { tn = n; off = ci - acc; break; }
    acc += l;
  }
  if (!tn) return null;
  const range = document.createRange();
  range.setStart(tn, off);
  range.setEnd(tn, off + 1);
  const r = range.getBoundingClientRect();
  if (!r.width && !r.height) return null;
  return { x: (char === 0) ? r.left : r.right, y: r.top + r.height / 2 };
}`

// EvalCharPoint resolves a chat-input caret character index to an absolute
// live-page coordinate (see charPointExpr). ok is false when the input
// editor or its text cannot be resolved.
func EvalCharPoint(s *Session, selectors []string, char int) (float64, float64, bool) {
	args, _ := json.Marshal([]any{selectors, char})
	raw, err := callExpr(s, charPointExpr, string(args))
	if err != nil {
		return 0, 0, false
	}
	v, err := decodeEval[struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}](raw)
	if err != nil {
		return 0, 0, false
	}
	return v.X, v.Y, true
}

// rectExpr resolves a DOM path (child indices from the pane root) to the
// target element's bounding box in viewport coordinates. ok:false (via a
// null return) when the path does not resolve.
const rectExpr = `(args) => {
  const sels = args[0], path = args[1];
  ` + findRootJS + `
  const root = findRoot(sels);
  if (!root) return null;
  let el = root;
  if (Array.isArray(path) && path.length) {
    for (const i of path) {
      if (!el.children[i]) return null;
      el = el.children[i];
    }
  }
  const r = el.getBoundingClientRect();
  return { ok: true, left: r.left, top: r.top, width: r.width, height: r.height };
}`

// EvalRect resolves a DOM path to the element's bounding box in viewport
// coordinates. ok is false when the path does not resolve.
func EvalRect(s *Session, selectors []string, path []int) (PaneRect, bool) {
	args, _ := json.Marshal([]any{selectors, path})
	raw, err := callExpr(s, rectExpr, string(args))
	if err != nil {
		return PaneRect{}, false
	}
	v, err := decodeEval[struct {
		OK     bool    `json:"ok"`
		Left   float64 `json:"left"`
		Top    float64 `json:"top"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}](raw)
	if err != nil || !v.OK {
		return PaneRect{}, false
	}
	return PaneRect{Left: v.Left, Top: v.Top, Width: v.Width, Height: v.Height}, true
}

// evalResp is a Runtime.evaluate response: the result value plus the
// page-side exception (if any).
type evalResp struct {
	Result struct {
		Value jsontext.Value `json:"value"`
	} `json:"result"`
	Exception struct {
		Text string `json:"text"`
		Obj  struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// exception returns the page-side exception description of a response (""
// when none). Note: CDP puts the description at
// exceptionDetails.exception.description, with a human-readable fallback at
// exceptionDetails.text.
func (r evalResp) exception() string {
	if r.Exception.Obj.Description != "" {
		return r.Exception.Obj.Description
	}
	return r.Exception.Text
}

// decodeEval unwraps a Runtime.evaluate response and decodes result.value
// into T. Page exceptions are surfaced as errors. A rejected promise
// (awaitPromise) also carries result.value = {}, so the exception check
// must happen before decoding the value.
func decodeEval[T any](raw jsontext.Value) (*T, error) {
	var resp evalResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if desc := resp.exception(); desc != "" {
		return nil, errors.New("page exception: " + desc)
	}
	var out T
	if err := json.Unmarshal(resp.Result.Value, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// workspaceExpr asks the workbench which workspace this window has open.
// The main process hands every renderer its own window configuration
// (window.vscode.context.resolveConfiguration); its workspace field is
// the single-folder URI (w.uri) or the .code-workspace file (w.configPath)
// in the same percent-encoded format storage.json uses as its keys.
const workspaceExpr = `(async () => {
  try {
    const c = await window.vscode.context.resolveConfiguration();
    const w = c && c.workspace;
    if (!w) return null;
    const pick = (u) => u ? (u._formatted || (u.scheme + '://' + (u.authority || '') + u.path)) : null;
    return w.uri ? pick(w.uri) : pick(w.configPath);
  } catch (e) { return null; }
})()`

// WorkspaceURI returns the workspace URI the window currently has open —
// in storage.json key format (file:///c%3A/... or
// vscode-remote://ssh-remote%2Bhost/...) — or "" for empty windows and
// failed probes.
func WorkspaceURI(s *Session) string {
	raw, err := s.Call("Runtime.evaluate", map[string]any{
		"expression":    workspaceExpr,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return ""
	}
	uri, err := decodeEval[string](raw)
	if err != nil || *uri == "" {
		return ""
	}
	return *uri
}
