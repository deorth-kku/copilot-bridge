// Unit tests for htmlExpr (internal/cdp/extract.go, with the spliced
// scrollContainerJS + scrollPathJS): full pane extraction for the mirror —
// html, rect, active scroll container selection, scroll path/rows, theme
// variables, and the visible context-view popup.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, makeGetComputedStyle, installGlobals, uninstallGlobals } = require('../domstub.js');
const htmlExpr = require('../modules/html_expr.js');

// Pane with TWO monaco-lists: the chat list (expanded, scrollable room)
// and the sessions list (collapsed, no room), plus a model picker.
function buildPane({ popup = false } = {}) {
  const root = new El('div', { attrs: { class: 'monaco-workbench', style: 'color: white' } });
  const body = new El('body', { attrs: { style: 'background: black' } });

  const pane = new El('div', {
    attrs: { class: 'chat-viewpane-container' },
    rect: { left: 10, top: 20, width: 400, height: 600 },
    offsetWidth: 400,
    offsetHeight: 600,
    innerText: 'abc',
  });
  pane.outerHTML = '<div class="chat-viewpane-container">…</div>';

  const chatList = new El('div', { attrs: { class: 'monaco-list' } });
  const chatSc = new El('div', {
    attrs: { class: 'monaco-scrollable-element' },
    scrollHeight: 2000,
    clientHeight: 500,
    clientWidth: 380,
    scrollTop: 0,
    scrollLeft: 0,
  });
  const rows = new El('div', { attrs: { class: 'monaco-list-rows' }, offsetTop: -1500 });
  rows.appendChild(new El('div', { attrs: { class: 'monaco-list-row' }, offsetTop: 0, offsetHeight: 80 }));
  rows.appendChild(new El('div', { attrs: { class: 'monaco-list-row' }, offsetTop: 80, offsetHeight: 60 }));
  chatSc.appendChild(rows);
  chatList.appendChild(chatSc);
  pane.appendChild(chatList);

  const sessList = new El('div', { attrs: { class: 'monaco-list' } });
  sessList.appendChild(new El('div', { attrs: { class: 'monaco-scrollable-element' }, scrollHeight: 500, clientHeight: 500 }));
  pane.appendChild(sessList);

  pane.appendChild(new El('a', { attrs: { class: 'model-picker-name' }, text: 'Qwen3.8 27B' }));
  root.appendChild(pane);

  if (popup) {
    const hidden = new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
    hidden.outerHTML = '<div class="context-view hidden"></div>';
    const cv = new El('div', { attrs: { class: 'context-view' }, rect: { left: 110, top: 80, width: 120, height: 60 } });
    cv.outerHTML = '<div class="context-view">menu</div>';
    root.appendChild(hidden);
    root.appendChild(cv);
  }

  const csMap = el => {
    if (el === pane) {
      return { '--vscode-foreground': '#DDD', 'font-family': 'Camelia', 'font-size': '13px', backgroundColor: 'rgba(0, 0, 0, 0)' };
    }
    if (el === root) return { backgroundColor: '#111827' };
    return {};
  };
  const doc = makeDocument(root, { body });
  const win = makeWindow(doc);
  installGlobals(doc, win, { getComputedStyle: makeGetComputedStyle(csMap) });
  return { root, pane, doc, win };
}

test('htmlExpr: pane not found -> err', () => {
  const root = new El('div');
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    assert.equal(htmlExpr(['.does-not-exist']).err, 'pane not found');
  } finally { uninstallGlobals(); }
});

test('htmlExpr: selector list — first match wins, later ones tried on miss', () => {
  const { win } = buildPane();
  try {
    const r = htmlExpr(['.nope', '.chat-viewpane-container']);
    assert.equal(r.err, undefined);
    assert.equal(r.html, '<div class="chat-viewpane-container">…</div>');
  } finally { uninstallGlobals(); }
});

test('htmlExpr: rect, cssFP, rootStyle', () => {
  buildPane();
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    assert.deepEqual(r.rect, { left: 10, top: 20, width: 400, height: 600 });
    // 0 stylesheets : rootStyle attr len : body style attr len
    assert.equal(r.cssFP, '0:' + 'color: white'.length + ':' + 'background: black'.length);
    assert.equal(r.rootStyle, 'color: white;background: black');
  } finally { uninstallGlobals(); }
});

test('htmlExpr: active scroller = direct-child monaco scroller with most room', () => {
  buildPane();
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    assert.equal(r.scroll.scrollH, 2000); // chat scroller, not the collapsed one
    assert.equal(r.scroll.left, 0);
    assert.equal(r.scroll.top, 0);
    assert.equal(r.scroll.w, 380);
    assert.equal(r.scroll.h, 500);
    assert.equal(r.scroll.offset, -1500); // .monaco-list-rows offsetTop
    assert.deepEqual(r.scrollRows, [0, 80, 80, 60]);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: scrollPath is the child-index path from the pane root', () => {
  buildPane();
  try {
    // pane > chatList(child 0) > chatSc(child 0)
    assert.deepEqual(htmlExpr(['.chat-viewpane-container']).scrollPath, [0, 0]);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: theme vars include custom props, text metrics, walked background', () => {
  buildPane();
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    assert.ok(r.themeVars.includes('--vscode-foreground: #DDD;'));
    assert.ok(r.themeVars.includes('font-family: Camelia;'));
    assert.ok(r.themeVars.includes('font-size: 13px;'));
    assert.ok(r.themeVars.includes('background-color: #111827;'));
    assert.equal(r.themeBg, '#111827');
  } finally { uninstallGlobals(); }
});

test('htmlExpr: visible context view captured pane-relative; hidden ones ignored', () => {
  buildPane({ popup: true });
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    assert.equal(r.popup.html, '<div class="context-view">menu</div>');
    assert.equal(r.popup.left, 100); // 110 - pane.left(10)
    assert.equal(r.popup.top, 60); // 80 - pane.top(20)
    assert.equal(r.popup.width, 120);
    assert.equal(r.popup.height, 60);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: no popup -> popup null', () => {
  buildPane();
  try {
    assert.equal(htmlExpr(['.chat-viewpane-container']).popup, null);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: content fits (room 0) -> the visible scroller is still selected', () => {
  const root = new El('div', { attrs: { class: 'monaco-workbench' } });
  const pane = new El('div', {
    attrs: { class: 'chat-viewpane-container' },
    rect: { left: 0, top: 0, width: 400, height: 600 },
    innerText: 'abc',
  });
  pane.outerHTML = '<div class="chat-viewpane-container">…</div>';
  const chatList = new El('div', { attrs: { class: 'monaco-list' } });
  const chatSc = new El('div', {
    attrs: { class: 'monaco-scrollable-element' },
    scrollHeight: 500,
    clientHeight: 500, // fits exactly: room 0
    clientWidth: 380,
  });
  const rows = new El('div', { attrs: { class: 'monaco-list-rows' }, offsetTop: 0 });
  rows.appendChild(new El('div', { attrs: { class: 'monaco-list-row' }, offsetTop: 0, offsetHeight: 300 }));
  rows.appendChild(new El('div', { attrs: { class: 'monaco-list-row' }, offsetTop: 300, offsetHeight: 200 }));
  chatSc.appendChild(rows);
  chatList.appendChild(chatSc);
  pane.appendChild(chatList);
  // A hidden sibling list (clientHeight 0) must not win the selection.
  const sessList = new El('div', { attrs: { class: 'monaco-list' } });
  sessList.appendChild(new El('div', { attrs: { class: 'monaco-scrollable-element' }, scrollHeight: 900, clientHeight: 0 }));
  pane.appendChild(sessList);
  root.appendChild(pane);
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    assert.deepEqual(r.scrollPath, [0, 0], 'the fitting scroller is still reported');
    assert.equal(r.scroll.scrollH, 500);
    assert.equal(r.scroll.h, 500);
    assert.equal(r.scroll.offset, 0);
    assert.deepEqual(r.scrollRows, [0, 300, 300, 200]);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: no scroller and no overflow ancestor -> neutral state (null path, pane geometry)', () => {
  const root = new El('div');
  const pane = new El('div', {
    attrs: { class: 'p' },
    rect: { left: 0, top: 0, width: 100, height: 400 },
    scrollHeight: 900,
    clientHeight: 400,
    clientWidth: 100,
    innerText: 'x',
  });
  pane.outerHTML = '<div class="p">…</div>';
  root.appendChild(pane);
  const doc = makeDocument(root);
  const win = makeWindow(doc);
  installGlobals(doc, win);
  try {
    const r = htmlExpr(['.p']);
    assert.equal(r.scrollPath, null, 'no scroller: null path, not the documentElement');
    assert.deepEqual(r.scroll, { left: 0, top: 0, scrollH: 900, offset: 0, w: 100, h: 400 }, 'neutral pane geometry, not the window geometry');
    assert.equal(r.scrollRows, null);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: ancestor-walk fallback when no monaco-list scroller exists', () => {
  const root = new El('div');
  const pane = new El('div', {
    attrs: { class: 'p' },
    rect: { left: 0, top: 0, width: 100, height: 400 },
    scrollHeight: 1200,
    clientHeight: 400,
    clientWidth: 100,
    scrollTop: 300,
  });
  root.appendChild(pane);
  const doc = makeDocument(root);
  const win = makeWindow(doc);
  installGlobals(doc, win, { getComputedStyle: makeGetComputedStyle(el => (el === pane ? { overflowY: 'auto' } : {})) });
  try {
    const r = htmlExpr(['.p']);
    assert.equal(r.scroll.top, 300);
    assert.equal(r.scroll.scrollH, 1200);
    assert.deepEqual(r.scrollPath, []); // sc === pane root
    assert.equal(r.scrollRows, null);
    assert.equal(r.scroll.offset, 0);
  } finally { uninstallGlobals(); }
});

test('htmlExpr: nestedScrolls captures the reasoning-trace list scroll state', () => {
  const { root } = buildPane();
  const pane = root.querySelector('.chat-viewpane-container');
  const row = pane.children[0].children[0].children[0].children[0]; // rows > row
  const box = new El('div', { attrs: { class: 'chat-used-context chat-thinking-box' } });
  const list = new El('div', {
    attrs: { class: 'chat-used-context-list chat-thinking-streaming' },
    scrollTop: 500,
    scrollHeight: 800,
    clientHeight: 200,
  });
  box.appendChild(list);
  row.appendChild(box);
  try {
    const r = htmlExpr(['.chat-viewpane-container']);
    // pane > chatList(0) > chatSc(0) > rows(0) > row(0) > box(0) > list(0)
    assert.deepEqual(r.nestedScrolls, [
      { path: [0, 0, 0, 0, 0, 0], top: 500, scrollH: 800, clientH: 200 },
    ]);
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

test('htmlExpr: inputFocused tracks the chat-input editor focus', () => {
  const { root, doc } = buildPane();
  const cursor = addInputEditor(root.querySelector('.chat-viewpane-container'));
  try {
    assert.equal(htmlExpr(['.chat-viewpane-container']).inputFocused, false);
    cursor.focus(); // activeElement inside the input editor
    assert.equal(htmlExpr(['.chat-viewpane-container']).inputFocused, true);
    doc.activeElement = root; // focus elsewhere
    assert.equal(htmlExpr(['.chat-viewpane-container']).inputFocused, false);
    doc.activeElement = null;
    assert.equal(htmlExpr(['.chat-viewpane-container']).inputFocused, false);
  } finally { doc.activeElement = null; uninstallGlobals(); }
});

test('htmlExpr: no chat input editor -> inputFocused false', () => {
  buildPane();
  try {
    assert.equal(htmlExpr(['.chat-viewpane-container']).inputFocused, false);
  } finally { uninstallGlobals(); }
});
