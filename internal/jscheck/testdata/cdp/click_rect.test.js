// Unit tests for clickPointExpr and rectExpr (internal/cdp/extract.go):
// DOM-path + relative-position resolution to live-page coordinates.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const { loadExpr } = require('./loadexpr.js');
const clickPoint = loadExpr('click_point_expr.js');
const rect = loadExpr('rect_expr.js');

function build() {
  const root = new El('div', { attrs: { class: 'w' } });
  const pane = new El('div', { attrs: { class: 'pane' }, rect: { left: 10, top: 20, width: 100, height: 50 } });
  const a = new El('div', { rect: { left: 15, top: 25, width: 40, height: 20 } });
  const b = new El('div', { rect: { left: 20, top: 30, width: 10, height: 10 } });
  a.appendChild(b);
  pane.appendChild(a);
  root.appendChild(pane);
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  return { root, pane };
}

test('clickPoint: maps relative position on a path element', () => {
  build();
  try {
    // path [0,0]: pane > a > b; b rect (20,30,10,10)
    const p = clickPoint([['.pane'], [0, 0], 0.5, 0.5]);
    assert.deepEqual(p, { x: 25, y: 35 });
  } finally { uninstallGlobals(); }
});

test('clickPoint: empty path targets the root', () => {
  build();
  try {
    const p = clickPoint([['.pane'], [], 1, 1]);
    assert.deepEqual(p, { x: 110, y: 70 });
  } finally { uninstallGlobals(); }
});

test('clickPoint: unresolvable path falls back to the root', () => {
  build();
  try {
    const p = clickPoint([['.pane'], [5], 0, 0]);
    assert.deepEqual(p, { x: 10, y: 20 });
  } finally { uninstallGlobals(); }
});

test('clickPoint: no root -> null', () => {
  build();
  try {
    assert.equal(clickPoint([['.nope'], [], 0, 0]), null);
  } finally { uninstallGlobals(); }
});

test('clickPoint: negative path roots at the context view with that stable id', () => {
  const { root } = build();
  try {
    const hidden0 = new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
    const hidden1 = new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
    const cv = new El('div', { attrs: { class: 'context-view' }, rect: { left: 50, top: 60, width: 100, height: 40 } });
    const row = new El('div', { rect: { left: 55, top: 65, width: 90, height: 10 } });
    cv.appendChild(row);
    root.appendChild(hidden0);
    root.appendChild(hidden1);
    root.appendChild(cv);
    // The visible cv is id 2 in the FULL list (the hidden ones are 0, 1):
    // path [-3, 0] = cv > row (-(id+1); -2 is reserved for the modal
    // editor, so id 1 is unaddressable); row rect (55,65,90,10)
    const p = clickPoint([['.pane'], [-3, 0], 0.5, 1]);
    assert.deepEqual(p, { x: 100, y: 75 });
    // The hidden cv (id 0) is not visible -> null.
    assert.equal(clickPoint([['.pane'], [-1, 0], 0.5, 1]), null);
    // -2 is the modal editor (absent here) -> null.
    assert.equal(clickPoint([['.pane'], [-2, 0], 0.5, 1]), null);
    // Out-of-range id -> null.
    assert.equal(clickPoint([['.pane'], [-4, 0], 0.5, 1]), null);
  } finally { uninstallGlobals(); }
});

test('clickPoint: second visible context view addressed by its own id', () => {
  const { root } = build();
  try {
    const cv1 = new El('div', { attrs: { class: 'context-view' }, rect: { left: 50, top: 60, width: 100, height: 40 } });
    const row1 = new El('div', { rect: { left: 55, top: 65, width: 90, height: 10 } });
    cv1.appendChild(row1);
    const cv2 = new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
    const cv3 = new El('div', { attrs: { class: 'context-view' }, rect: { left: 200, top: 300, width: 80, height: 20 } });
    const row2 = new El('div', { rect: { left: 205, top: 305, width: 70, height: 10 } });
    cv3.appendChild(row2);
    root.appendChild(cv1);
    root.appendChild(cv2);
    root.appendChild(cv3);
    // cv3 is id 2 (cv2, id 1, is hidden and unaddressable anyway):
    // path [-3, 0] = cv3 > row2 (-(id+1)); row2 rect (205,305,70,10)
    const p = clickPoint([['.pane'], [-3, 0], 0.5, 1]);
    assert.deepEqual(p, { x: 240, y: 315 });
  } finally { uninstallGlobals(); }
});

test('rect: resolves a path to the element bounding box', () => {
  build();
  try {
    assert.deepEqual(rect([['.pane'], [0, 0]]), { ok: true, left: 20, top: 30, width: 10, height: 10 });
  } finally { uninstallGlobals(); }
});

test('rect: root itself for empty path', () => {
  build();
  try {
    assert.deepEqual(rect([['.pane'], []]), { ok: true, left: 10, top: 20, width: 100, height: 50 });
  } finally { uninstallGlobals(); }
});

test('rect: unresolvable path -> null', () => {
  build();
  try {
    assert.equal(rect([['.pane'], [0, 7]]), null);
  } finally { uninstallGlobals(); }
});

test('rect: no root -> null', () => {
  build();
  try {
    assert.equal(rect([['.nope'], []]), null);
  } finally { uninstallGlobals(); }
});
