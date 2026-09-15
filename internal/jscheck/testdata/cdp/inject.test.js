// Unit tests for InjectJS (internal/cdp/inject.go): the versioned
// MutationObserver IIFE that pushes chat state through window.copilotBridge.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals, MutationObserver } = require('../domstub.js');

const src = fs.readFileSync(__dirname + '/../raw/inject_js.js', 'utf8');
const sleep = ms => new Promise(res => setTimeout(res, ms));
// The IIFE's completion value is only reachable via an explicit return
// (a bare expression statement discards it), matching how Runtime.evaluate
// surfaces it in the browser.
const run = () => new Function('return ' + src)();

// Async on purpose: a 50ms timer left pending by a PREVIOUS test's IIFE
// captures the bare `window` global, so it would fire into THIS test's
// window. Flushing 80ms and resetting the log makes each test hermetic.
async function setup() {
  const root = new El('div', { attrs: { class: 'monaco-workbench' } });
  const doc = makeDocument(root);
  const win = makeWindow(doc);
  const calls = [];
  win.copilotBridge = p => calls.push(p);
  installGlobals(doc, win);
  await sleep(80);
  calls.length = 0;
  return { doc, win, calls };
}

test('InjectJS: returns ok and emits an initial snapshot', async () => {
  const { calls } = await setup();
  try {
    assert.equal(run(), 'ok');
    await sleep(80); // initial push is debounced 50ms
    assert.equal(calls.length, 1);
    const st = JSON.parse(calls[0]);
    assert.equal(st.input, null);
    assert.equal(st.model, null);
    assert.equal(st.effort, null);
    assert.equal(st.mode, null);
  } finally { uninstallGlobals(); }
});

test('InjectJS: observes documentElement with the documented options', async () => {
  const { doc } = await setup();
  try {
    run();
    const mo = MutationObserver.instances.at(-1);
    assert.equal(mo.target, doc.documentElement);
    assert.deepEqual(mo.options, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['aria-label'],
    });
  } finally { uninstallGlobals(); }
});

test('InjectJS: DOM mutations trigger exactly one debounced re-push', async () => {
  const { calls } = await setup();
  try {
    run();
    await sleep(80);
    assert.equal(calls.length, 1);
    const mo = MutationObserver.instances.at(-1);
    mo.trigger();
    mo.trigger(); // within the same 50ms window: must coalesce
    mo.trigger();
    await sleep(80);
    assert.equal(calls.length, 2);
  } finally { uninstallGlobals(); }
});

test('InjectJS: re-injection at the same version does not stack observers', async () => {
  const { calls } = await setup();
  try {
    run();
    await sleep(80);
    const before = MutationObserver.instances.length;
    run();
    assert.equal(MutationObserver.instances.length, before, 'no second observer');
    await sleep(80);
    assert.equal(calls.length, 2, 're-injection emits exactly one snapshot');
  } finally { uninstallGlobals(); }
});

test('InjectJS: payload is valid JSON stringifying the full state shape', async () => {
  const { calls } = await setup();
  try {
    run();
    await sleep(80);
    const st = JSON.parse(calls[0]);
    assert.deepEqual(Object.keys(st).sort(), ['effort', 'input', 'inputVisible', 'mode', 'model', 'modelVisible']);
  } finally { uninstallGlobals(); }
});
