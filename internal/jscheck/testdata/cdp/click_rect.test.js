// Unit tests for clickPointExpr and rectExpr (internal/cdp/extract.go):
// DOM-path + relative-position resolution to live-page coordinates.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const clickPoint = require('../modules/click_point.js');
const rect = require('../modules/rect.js');

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

test('clickPoint: -1 path roots at the VISIBLE context view', () => {
  const { root } = build();
  try {
    const hidden = new El('div', { attrs: { class: 'context-view' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
    const cv = new El('div', { attrs: { class: 'context-view' }, rect: { left: 50, top: 60, width: 100, height: 40 } });
    const row = new El('div', { rect: { left: 55, top: 65, width: 90, height: 10 } });
    cv.appendChild(row);
    root.appendChild(hidden);
    root.appendChild(cv);
    // path [-1, 0]: visible context view > row; row rect (55,65,90,10)
    const p = clickPoint([['.pane'], [-1, 0], 0.5, 1]);
    assert.deepEqual(p, { x: 100, y: 75 });
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
