// Unit tests for fpExpr (internal/cdp/extract.go, with spliced
// scrollContainerJS + scrollPathJS): the cheap per-poll fingerprint probe.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, makeGetComputedStyle, installGlobals, uninstallGlobals } = require('../domstub.js');
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
    // innerText len 3 : childElementCount 2 : model len 4 : themeInd : popFP(empty) : curFP(empty, no input editor)
    assert.equal(r.fp, '3:2:4:#DDD::');
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
    // popFP = innerText len 4 : childElementCount 1 : round(left) : round(top) ; curFP empty
    assert.ok(r.fp.endsWith(':4:1:110:80:'), r.fp);
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
    // '1' : '1' : model '' (len 0) : themeInd '' : popFP '' : curFP ''
    assert.equal(r.fp, '1:0:0:::');
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
    // curFP = top|left|height|cursorCount|focused
    assert.ok(a.fp.endsWith(':12px|197px||1|0'), a.fp);
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
    assert.ok(unfocused.fp.endsWith('|1|0'), unfocused.fp);
    cursor.focus(); // activeElement inside the input editor
    const focused = fpExpr(['.chat-viewpane-container']);
    assert.ok(focused.fp.endsWith('|1|1'), focused.fp);
    assert.equal(focused.inputFocused, true);
    doc.activeElement = root; // focus elsewhere
    const back = fpExpr(['.chat-viewpane-container']);
    assert.ok(back.fp.endsWith('|1|0'), back.fp);
    assert.equal(back.inputFocused, false);
  } finally { doc.activeElement = null; uninstallGlobals(); }
});
