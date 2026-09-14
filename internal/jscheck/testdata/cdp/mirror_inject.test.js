// Unit tests for MirrorInjectJS (internal/cdp/inject.go): the versioned
// observer that wakes the mirror through window.vscodeLoadLlamaMirror on
// DOM mutations, descendant scrolls (capture), and window resize.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { El, Event, makeDocument, makeWindow, installGlobals, uninstallGlobals, MutationObserver } = require('../domstub.js');

const src = fs.readFileSync(__dirname + '/../raw/mirror_inject_js.js', 'utf8');
const sleep = ms => new Promise(res => setTimeout(res, ms));
const run = () => new Function('return ' + src)();

// Async on purpose: see inject.test.js — flushes timers leaked from
// previous tests so each test starts with an empty, quiescent window.
async function setup() {
  const root = new El('div', { attrs: { class: 'monaco-workbench' } });
  const doc = makeDocument(root);
  const win = makeWindow(doc);
  const wakes = [];
  win.vscodeLoadLlamaMirror = p => wakes.push(p);
  installGlobals(doc, win);
  await sleep(80);
  wakes.length = 0;
  return { doc, win, root, wakes };
}

test('MirrorInjectJS: emits an initial wake', async () => {
  const { wakes } = await setup();
  try {
    assert.equal(run(), 'ok');
    await sleep(80);
    assert.deepEqual(wakes, ['1']);
  } finally { uninstallGlobals(); }
});

test('MirrorInjectJS: scroll on a descendant wakes via the capture listener', async () => {
  const { doc, root, wakes } = await setup();
  try {
    run();
    await sleep(80);
    const sc = new El('div', { attrs: { class: 'monaco-scrollable-element' } });
    root.appendChild(sc);
    doc.dispatchEvent(new Event('scroll', sc));
    await sleep(80);
    assert.equal(wakes.length, 2);
  } finally { uninstallGlobals(); }
});

test('MirrorInjectJS: window resize wakes', async () => {
  const { win, wakes } = await setup();
  try {
    run();
    await sleep(80);
    win.dispatchEvent(new Event('resize'));
    await sleep(80);
    assert.equal(wakes.length, 2);
  } finally { uninstallGlobals(); }
});

test('MirrorInjectJS: rapid wakes coalesce to one per 50ms window', async () => {
  const { doc, win, root, wakes } = await setup();
  try {
    run();
    await sleep(80);
    doc.dispatchEvent(new Event('scroll', root));
    doc.dispatchEvent(new Event('scroll', root));
    win.dispatchEvent(new Event('resize'));
    await sleep(80);
    assert.equal(wakes.length, 2, 'three changes within one window -> one wake');
  } finally { uninstallGlobals(); }
});

test('MirrorInjectJS: re-injection does not stack observers or listeners', async () => {
  const { doc, root, wakes } = await setup();
  try {
    run();
    await sleep(80);
    const before = MutationObserver.instances.length;
    run();
    assert.equal(MutationObserver.instances.length, before, 'no second observer');
    await sleep(80);
    assert.equal(wakes.length, 2, 're-injection emits exactly one wake');
    // One scroll must produce exactly ONE wake (no stacked capture handlers).
    doc.dispatchEvent(new Event('scroll', root));
    await sleep(80);
    assert.equal(wakes.length, 3);
  } finally { uninstallGlobals(); }
});
