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
  const chatList = new El('div', { attrs: { class: 'monaco-list' } });
  const chatSc = new El('div', {
    attrs: { class: 'monaco-scrollable-element' },
    scrollHeight: 2000,
    clientHeight: 500,
    clientWidth: 380,
  });
  chatList.appendChild(chatSc);
  pane.appendChild(chatList);
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
    // innerText len 3 : childElementCount 2 : model len 4 : themeInd : popFP(empty)
    assert.equal(r.fp, '3:2:4:#DDD:');
    assert.equal(r.cssFP, '0:' + 'color: white'.length + ':' + 'background: black'.length);
    assert.deepEqual(r.rect, { left: 10, top: 20, width: 400, height: 600 });
    assert.equal(r.scroll.scrollH, 2000);
    assert.deepEqual(r.scrollPath, [0, 0]);
  } finally { uninstallGlobals(); }
});

test('fpExpr: visible popup folded into popFP', () => {
  buildPane({ popup: true });
  try {
    const r = fpExpr(['.chat-viewpane-container']);
    // popFP = innerText len 4 : childElementCount 1 : round(left) : round(top)
    assert.ok(r.fp.endsWith(':4:1:110:80'), r.fp);
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
    // '1' : '1' : model '' (len 0) : themeInd '' : popFP ''
    assert.equal(r.fp, '1:0:0::');
  } finally { uninstallGlobals(); }
});
