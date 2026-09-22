// Unit tests for fpExpr (internal/cdp/extract.go, with spliced
// scrollContainerJS + scrollPathJS): the cheap per-poll fingerprint probe.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, Text, makeDocument, makeWindow, makeGetComputedStyle, installGlobals, uninstallGlobals } = require('../domstub.js');
const fpExpr = require('../modules/fp_expr.js');

function buildPane({ popup = false } = {}) {
  const root = new El('div', { attrs: { class: 'monaco-workbench', style: 'color: white' } });
  const body = new El('body', { attrs: { style: 'background: black' } });
  const pane = new El('div', {
    attrs: { class: 'chat-viewpane-container' },
    rect: { left: 10, top: 20, width: 400, height: 600 },
    innerText: 'abc',
  });
  // The chat message list sits inside .interactive-list (bottom-anchored in
  // live).
  const chatWrap = new El('div', { attrs: { class: 'interactive-list' } });
  const chatList = new El('div', { attrs: { class: 'monaco-list' } });
  const chatSc = new El('div', {
    attrs: { class: 'monaco-scrollable-element' },
    scrollHeight: 2000,
    clientHeight: 500,
    clientWidth: 380,
  });
  chatList.appendChild(chatSc);
  chatWrap.appendChild(chatList);
  pane.appendChild(chatWrap);
  pane.appendChild(new El('a', { attrs: { class: 'model-picker-name' }, text: 'Qwen' }));
  root.appendChild(pane);
  if (popup) {
    root.appendChild(new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } }));
    const cv = new El('div', { attrs: { class: 'context-view' }, rect: { left: 110, top: 80, width: 120, height: 60 } });
    cv.innerText = 'menu';
    cv.appendChild(new El('div'));
    root.appendChild(cv);
  }
  const doc = makeDocument(root, { body });
  const win = makeWindow(doc);
  installGlobals(doc, win, { getComputedStyle: makeGetComputedStyle(el => (el === pane ? { '--vscode-foreground': '#DDD' } : {})) });
  return { root, doc, win };
}

test('fpExpr: pane not found -> err', () => {
  const root = new El('div');
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    assert.equal(fpExpr(['.nope']).err, 'pane not found');
  } finally { uninstallGlobals(); }
});

test('fpExpr: fingerprint composition (content, children, model, theme)', () => {
  buildPane();
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    // innerText len 3 : childElementCount 2 : model len 4 : themeInd : popFP(empty) : curFP(empty, no input editor) : inputFP(empty, no input editor)
    assert.equal(r.fp, '3:2:4:#DDD:::');
    assert.equal(r.cssFP, '0:' + 'color: white'.length + ':' + 'background: black'.length);
    assert.deepEqual(r.rect, { left: 10, top: 20, width: 400, height: 600 });
    assert.equal(r.scroll.scrollH, 2000);
    assert.equal(r.scroll.anchorKind, 'bottom'); // chat list is .interactive-list
    assert.deepEqual(r.scrollPath, [0, 0, 0]);
  } finally { uninstallGlobals(); }
});

test('fpExpr: visible popup folded into popFP', () => {
  buildPane({ popup: true });
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    // popFP = innerText len 4 : childElementCount 1 : round(left) : round(top) ; curFP empty ; inputFP empty
    assert.ok(r.fp.endsWith(':4:1:110:80::'), r.fp);
  } finally { uninstallGlobals(); }
});

test('fpExpr: hidden context views do not contribute to popFP', () => {
  const { root } = buildPane();
  root.appendChild(new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } }));
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.ok(r.fp.endsWith(':'), r.fp);
  } finally { uninstallGlobals(); }
});

test('fpExpr: missing model picker contributes empty model to fp', () => {
  const root = new El('div');
  const pane = new El('div', { attrs: { class: 'p' }, rect: { left: 0, top: 0, width: 10, height: 10 }, innerText: 'x' });
  root.appendChild(pane);
  const doc = makeDocument(root);
  const win = makeWindow(doc);
  installGlobals(doc, win);
  try {
    const r = fpExpr(['.p']);
    // '1' : '1' : model '' (len 0) : themeInd '' : popFP '' : curFP '' : inputFP ''
    assert.equal(r.fp, '1:0:0::::');
  } finally { uninstallGlobals(); }
});

test('fpExpr: nestedScrolls captures the reasoning-trace list scroll state', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  const chatSc = pane.children[0].children[0].children[0];
  const rows = new El('div', { attrs: { class: 'monaco-list-rows' } });
  const row = new El('div', { attrs: { class: 'monaco-list-row' } });
  rows.appendChild(row);
  chatSc.appendChild(rows);
  const box = new El('div', { attrs: { class: 'chat-used-context chat-thinking-box' } });
  const list = new El('div', {
    attrs: { class: 'chat-used-context-list chat-thinking-streaming' },
    scrollTop: 400,
    scrollHeight: 600,
    clientHeight: 200,
  });
  box.appendChild(list);
  row.appendChild(box);
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    // pane > interactiveList(0) > chatList(0) > chatSc(0) > rows(0) > row(0) > box(0) > list(0)
    assert.deepEqual(r.nestedScrolls, [
      { path: [0, 0, 0, 0, 0, 0, 0], top: 400, scrollH: 600, clientH: 200 },
    ]);
  } finally { uninstallGlobals(); }
});

test('fpExpr: no thinking box -> nestedScrolls null', () => {
  buildPane();
  try {
    assert.equal(fpExpr(['.chat-viewpane-container']).nestedScrolls, null);
  } finally { uninstallGlobals(); }
});

// Append the live chat-input editor subtree (chat-input-container >
// interactive-input-editor > monaco-editor > cursors-layer > cursor) to the
// pane and return the cursor element.
function addInputEditor(pane) {
  const container = new El('div', { attrs: { class: 'chat-input-container' } });
  const editor = new El('div', { attrs: { class: 'interactive-input-editor' } });
  const monaco = new El('div', { attrs: { class: 'monaco-editor' } });
  const layer = new El('div', { attrs: { class: 'cursors-layer' } });
  const cursor = new El('div', { attrs: { class: 'cursor' } });
  layer.appendChild(cursor);
  monaco.appendChild(layer);
  editor.appendChild(monaco);
  container.appendChild(editor);
  pane.appendChild(container);
  return cursor;
}

test('fpExpr: chat-input cursor position is folded into curFP', () => {
  const { root } = buildPane();
  const cursor = addInputEditor(root.querySelector('.chat-viewpane-container'));
  try {
    cursor.style.setProperty('top', '12px');
    cursor.style.setProperty('left', '197px');
    const a = fpExpr(['.chat-viewpane-container']);
    // curFP = top|left|height|cursorCount|focused ; inputFP empty (no view-lines)
    assert.ok(a.fp.endsWith(':12px|197px||1|0:'), a.fp);
    cursor.style.setProperty('top', '24px');
    const b = fpExpr(['.chat-viewpane-container']);
    assert.notEqual(a.fp, b.fp, 'a caret move must flip the fp');
  } finally { uninstallGlobals(); }
});

test('fpExpr: cursor visibility is NOT in the fp (the JS blink must not force extracts)', () => {
  const { root } = buildPane();
  const cursor = addInputEditor(root.querySelector('.chat-viewpane-container'));
  try {
    cursor.style.setProperty('top', '12px');
    const a = fpExpr(['.chat-viewpane-container']);
    cursor.style.setProperty('visibility', 'hidden');
    const b = fpExpr(['.chat-viewpane-container']);
    assert.equal(a.fp, b.fp);
  } finally { uninstallGlobals(); }
});

test('fpExpr: chat-input focus state is folded into curFP', () => {
  const { root, doc } = buildPane();
  const cursor = addInputEditor(root.querySelector('.chat-viewpane-container'));
  try {
    cursor.style.setProperty('top', '12px');
    const unfocused = fpExpr(['.chat-viewpane-container']);
    assert.ok(unfocused.fp.endsWith('|1|0:'), unfocused.fp);
    cursor.focus(); // activeElement inside the input editor
    const focused = fpExpr(['.chat-viewpane-container']);
    assert.ok(focused.fp.endsWith('|1|1:'), focused.fp);
    assert.equal(focused.inputFocused, true);
    doc.activeElement = root; // focus elsewhere
    const back = fpExpr(['.chat-viewpane-container']);
    assert.ok(back.fp.endsWith('|1|0:'), back.fp);
    assert.equal(back.inputFocused, false);
  } finally { doc.activeElement = null; uninstallGlobals(); }
});

// Append a chat-input editor with view-lines to the pane. Each line's
// rect models its TEXT's range extent (in the real DOM a range over the
// line's contents returns the text's box, not the full-width div);
// characters are 13px wide (the monospace CJK case).
function addInputLines(pane, spec) {
  const container = new El('div', { attrs: { class: 'chat-input-container' } });
  const editor = new El('div', { attrs: { class: 'interactive-input-editor' } });
  const monaco = new El('div', { attrs: { class: 'monaco-editor' }, rect: { left: 0, top: 0, width: 545, height: 124 } });
  const vlines = new El('div', { attrs: { class: 'view-lines' } });
  spec.forEach((s, i) => {
    const rect = { left: 0, top: 12 + i * 20, width: s.chars * 13, height: 20 };
    const line = new El('div', { attrs: { class: 'view-line' }, text: '工'.repeat(s.chars), rect });
    const span = new El('span', { attrs: { class: 'mtk1' }, rect });
    span.appendChild(new Text('工'.repeat(s.chars)));
    line.appendChild(span);
    vlines.appendChild(line);
  });
  monaco.appendChild(vlines);
  editor.appendChild(monaco);
  container.appendChild(editor);
  pane.appendChild(container);
}

test('fpExpr: one continuous line -> the wrap boundary is soft', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  // A 56-char line wraps at 40 chars (520px): the boundary is a soft wrap.
  addInputLines(pane, [{ chars: 40 }, { chars: 16 }]);
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.deepEqual(r.inputBreaks, [false]);
    // inputFP = editor width : per-line character counts
    assert.ok(r.fp.endsWith(':545:40,16'), r.fp);
  } finally { uninstallGlobals(); }
});

test('fpExpr: hard newline after a short line', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  // Two short text lines: the boundary is a hard newline.
  addInputLines(pane, [{ chars: 10 }, { chars: 16 }]);
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.deepEqual(r.inputBreaks, [true]);
  } finally { uninstallGlobals(); }
});

test('fpExpr: mixed soft and hard boundaries', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  // A 70-char line wraps 40 + 30 (soft), then a newline, then a short line.
  addInputLines(pane, [{ chars: 40 }, { chars: 30 }, { chars: 8 }]);
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.deepEqual(r.inputBreaks, [false, true]);
  } finally { uninstallGlobals(); }
});

test('fpExpr: single view line -> no breaks', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  addInputLines(pane, [{ chars: 10 }]);
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.equal(r.inputBreaks, null);
  } finally { uninstallGlobals(); }
});

// A single view line whose ONE text run mixes CJK (26px) and Latin (13px)
// glyphs, like plain chat input (no decorations -> one span per line). The
// old proportional (uniform-width) estimate of the caret's character index
// skews on such text (it would report 6 for the caret below); the
// per-character Range boundary search must land exactly on 5. The editor
// content origin is offset (left: 100) because Range rects are in viewport
// coordinates while the cursor's inline left is line-relative: the search
// must convert before comparing.
test('fpExpr: cursorChar exact on mixed CJK/Latin text (no proportional skew)', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  const container = new El('div', { attrs: { class: 'chat-input-container' } });
  const editor = new El('div', { attrs: { class: 'interactive-input-editor' } });
  const monaco = new El('div', { attrs: { class: 'monaco-editor' }, rect: { left: 100, top: 0, width: 545, height: 44 } });
  const vlines = new El('div', { attrs: { class: 'view-lines' } });
  const text = '你好你好abcdefg你好'; // 4 CJK + 7 Latin + 2 CJK (13 chars)
  const widths = Array.from(text).map(ch => (ch.codePointAt(0) > 0x2e7f ? 26 : 13));
  const total = widths.reduce((a, b) => a + b, 0); // 6*26 + 7*13 = 247
  const line = new El('div', { attrs: { class: 'view-line' }, text, rect: { left: 100, top: 12, width: total, height: 20 } });
  line.style.setProperty('top', '12px');
  line.style.setProperty('height', '20px');
  const span = new El('span', { attrs: { class: 'mtk1' }, text, rect: { left: 100, top: 12, width: total, height: 20 } });
  span.charWidths = widths; // CJK glyphs 2x the Latin advance
  span.appendChild(new Text(text));
  line.appendChild(span);
  vlines.appendChild(line);
  monaco.appendChild(vlines);
  const layer = new El('div', { attrs: { class: 'cursors-layer' } });
  const cursor = new El('div', { attrs: { class: 'cursor' } });
  layer.appendChild(cursor);
  monaco.appendChild(layer);
  editor.appendChild(monaco);
  container.appendChild(editor);
  pane.appendChild(container);
  cursor.style.setProperty('top', '12px');
  // Caret after 'a' (true index 5): 4 CJK (26px) + 1 Latin (13px) = 117px.
  // Proportional would give round(117/247 * 13) = 6.
  cursor.style.setProperty('left', '117px');
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    assert.equal(r.cursorChar, 5);
  } finally { uninstallGlobals(); }
});
