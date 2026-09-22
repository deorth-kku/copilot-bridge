// Unit tests for the mirror page's inline client JS
// (internal/mirror/page.go): the IIFE that opens the /ws WebSocket,
// syncs the window picker + CSS/theme from state messages, patches the
// pane DOM incrementally, places popups, and forwards mouse/keyboard
// input to the server.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {
  El,
  Event,
  makeDocument,
  makeWindow,
  installGlobals,
  uninstallGlobals,
  WebSocketStub,
} = require('../domstub.js');

const src = fs.readFileSync(__dirname + '/../raw/mirror_page_js.js', 'utf8');
const sleep = ms => new Promise(res => setTimeout(res, ms));

// The pane sits below the 26px status bar and fills the 800x500 area;
// the window is 1280x800.
const PANE_RECT = { left: 0, top: 26, width: 800, height: 500 };

function setup(opts = {}) {
  const root = new El('div');
  const status = new El('select', { attrs: { id: 'status' } });
  status.appendChild(new El('option', { text: 'connecting…' }));
  const cssEl = new El('style', { attrs: { id: 'mirror-css' } });
  const themeEl = new El('style', { attrs: { id: 'mirror-theme' } });
  const pane = new El('div', {
    attrs: { id: 'pane', class: 'monaco-workbench monaco-pane-view' },
    rect: PANE_RECT,
    clientHeight: 500,
  });
  root.appendChild(status);
  root.appendChild(cssEl);
  root.appendChild(themeEl);
  root.appendChild(pane);
  const doc = makeDocument(root);
  const win = makeWindow(doc, { host: '127.0.0.1:8123', search: opts.search });
  WebSocketStub.instances.length = 0;
  installGlobals(doc, win);
  new Function(src)();
  const ws = WebSocketStub.instances.at(-1);
  ws.fireOpen();
  return { root, status, cssEl, themeEl, pane, doc, win, ws };
}

// Deliver one state message to the client.
function state(ws, m) {
  ws.fireMessage(JSON.stringify(Object.assign({ type: 'state' }, m)));
}

// The page JS talks to the bare `localStorage` global. Node may provide a
// native one (persisted across runs); a per-test stub keeps the behavior
// deterministic either way. restore() tears the stub down (or clears the
// native key) so tests cannot leak state into each other.
function withLocalStorage(init = {}) {
  const store = Object.assign({}, init);
  const stub = {
    getItem: k => (Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
    removeItem: k => { delete store[k]; },
  };
  try { globalThis.localStorage.removeItem('mirrorWin'); } catch (e) {} // native leftover
  let installed = false;
  try { globalThis.localStorage = stub; installed = true; } catch (e) {}
  return function restore() {
    if (installed) { try { delete globalThis.localStorage; } catch (e) {} }
    else { try { globalThis.localStorage.removeItem('mirrorWin'); } catch (e) {} }
  };
}

function sent(ws) {
  return ws.sent.map(s => JSON.parse(s));
}

test('mirror: connects to ws://<host>/ws and reports connected on open', () => {
  const { status, ws } = setup();
  try {
    assert.equal(ws.url, 'ws://127.0.0.1:8123/ws');
    assert.equal(status.children.length, 1);
    assert.equal(status.children[0].textContent, 'connected');
  } finally { uninstallGlobals(); }
});

test('mirror: reconnects one second after close and reports the transition', async () => {
  const { status, ws } = setup();
  try {
    ws.fireClose();
    assert.equal(status.children[0].textContent, 'disconnected, retrying…');
    await sleep(1100); // let the 1s retry timer fire (before uninstalling globals)
    assert.equal(WebSocketStub.instances.length, 2);
    const ws2 = WebSocketStub.instances.at(-1);
    assert.equal(ws2.url, 'ws://127.0.0.1:8123/ws');
    ws2.fireOpen();
    assert.equal(status.children[0].textContent, 'connected');
  } finally { uninstallGlobals(); }
});

test('mirror: window picker rebuilds options and disambiguates same titles', () => {
  const { status, ws } = setup();
  try {
    state(ws, {
      windows: [
        { id: 'win-123456', title: 'W' },
        { id: 'win-654321', title: 'W' },
        { id: 'win-999999', title: 'X' },
      ],
      windowId: 'win-654321',
    });
    assert.equal(status.children.length, 4, '3 windows + the Workspaces entry');
    assert.equal(status.children[0].value, 'win-123456');
    assert.equal(status.children[0].textContent, 'W (123456)');
    assert.equal(status.children[1].textContent, 'W (654321)');
    assert.equal(status.children[2].textContent, 'X');
    assert.equal(status.children[3].value, '__workspaces__');
    assert.equal(status.children[3].textContent, '— Workspaces…');
    assert.equal(status.value, 'win-654321');
  } finally { uninstallGlobals(); }
});

test('mirror: unchanged window set only moves the selection, keeps options', () => {
  const { status, ws } = setup();
  try {
    const wins = {
      windows: [
        { id: 'win-123456', title: 'W' },
        { id: 'win-654321', title: 'X' },
      ],
      windowId: 'win-123456',
    };
    state(ws, wins);
    const first = status.children[0];
    state(ws, Object.assign({}, wins, { windowId: 'win-654321' }));
    assert.equal(status.value, 'win-654321');
    assert.equal(status.children.length, 3, '2 windows + the Workspaces entry');
    assert.equal(status.children[0], first, 'options not rebuilt');
  } finally { uninstallGlobals(); }
});

test('mirror: error state is surfaced in the picker', () => {
  const { status, ws } = setup();
  try {
    state(ws, { err: 'boom' });
    assert.equal(status.children[0].textContent, '! boom');
  } finally { uninstallGlobals(); }
});

test('mirror: error state WITH windows keeps the picker populated', () => {
  const { status, ws } = setup();
  try {
    state(ws, {
      err: 'pane not found',
      windows: [
        { id: 'win-1', title: 'A' },
        { id: 'win-2', title: 'B' },
      ],
    });
    // The error is the selected placeholder; the window options remain.
    assert.equal(status.children.length, 4, 'err + 2 windows + the Workspaces entry');
    assert.equal(status.children[0].textContent, '! pane not found');
    assert.equal(status.children[0].value, '');
    assert.equal(status.children[1].value, 'win-1');
    assert.equal(status.children[2].value, 'win-2');
    assert.equal(status.children[3].value, '__workspaces__');
    assert.equal(status.value, '');
    // A repeated identical error state does not rebuild the options.
    const first = status.children[1];
    state(ws, {
      err: 'pane not found',
      windows: [
        { id: 'win-1', title: 'A' },
        { id: 'win-2', title: 'B' },
      ],
    });
    assert.equal(status.children[1], first, 'options not rebuilt');
    // A recovered state drops the placeholder and re-selects the window.
    state(ws, { windows: [{ id: 'win-1', title: 'A' }, { id: 'win-2', title: 'B' }], windowId: 'win-2' });
    assert.equal(status.children.length, 3, '2 windows + the Workspaces entry');
    assert.equal(status.children[0].textContent, 'A');
    assert.equal(status.value, 'win-2');
  } finally { uninstallGlobals(); }
});

test('mirror: CSS is applied only when cssVersion changes', () => {
  const { cssEl, ws } = setup();
  try {
    state(ws, { cssVersion: 'v1', css: '.a { color: red; }' });
    assert.equal(cssEl.textContent, '.a { color: red; }');
    state(ws, { cssVersion: 'v1', css: '.a { color: blue; }' });
    assert.equal(cssEl.textContent, '.a { color: red; }', 'same version: no re-apply');
    state(ws, { cssVersion: 'v2', css: '.b { margin: 0; }' });
    assert.equal(cssEl.textContent, '.b { margin: 0; }');
  } finally { uninstallGlobals(); }
});

test('mirror: theme is applied only when themeVer changes', () => {
  const { themeEl, ws } = setup();
  try {
    state(ws, { themeVer: 't1', themeBg: '#000001', themeVars: 'color: #fff; background: #111;' });
    assert.equal(
      themeEl.textContent,
      ':root { --mirror-bg: #000001; } #pane { color: #fff; background: #111; }',
    );
    state(ws, { themeVer: 't1', themeBg: '#000002', themeVars: 'color: #000;' });
    assert.equal(themeEl.textContent, ':root { --mirror-bg: #000001; } #pane { color: #fff; background: #111; }');
    state(ws, { themeVer: 't2', themeVars: 'color: #000;' });
    assert.equal(themeEl.textContent, ':root { --mirror-bg: #1e1e1e; } #pane { color: #000; }');
  } finally { uninstallGlobals(); }
});

test('mirror: first HTML state is appended and the chat input becomes editable', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="chat-input-container">' +
        '<textarea readonly aria-hidden="true" tabindex="-1" class="ime-text-area"></textarea>' +
        '</div></div>',
    });
    assert.equal(pane.children.length, 1);
    const ta = pane.querySelector('textarea');
    assert.ok(ta);
    assert.equal(ta.getAttribute('readonly'), null);
    assert.equal(ta.getAttribute('aria-hidden'), null);
    assert.equal(ta.getAttribute('tabindex'), '0');
  } finally { uninstallGlobals(); }
});

test('mirror: later HTML states patch in place, keeping unchanged node identity', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="msg" id="m1">hello</div><div class="msg" id="m2">world</div></div>' });
    const root1 = pane.children[0];
    const m1 = root1.children[0];
    const m2 = root1.children[1];
    m1._marker = 42;
    state(ws, { html: '<div class="root"><div class="msg" id="m1">hello again</div><div class="msg" id="m2">world</div></div>' });
    assert.equal(pane.children[0], root1, 'root patched in place');
    assert.equal(root1.children[0], m1, 'changed node keeps identity');
    assert.equal(m1._marker, 42);
    assert.equal(m1.children[0].data, 'hello again');
    assert.equal(root1.children[1], m2, 'unchanged node keeps identity');
    assert.equal(m2.children[0].data, 'world');
  } finally { uninstallGlobals(); }
});

test('mirror: rootStyle is applied to the pane except size properties', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      rootStyle: 'width: 800px; height: 600px; background-color: rgb(1, 2, 3); font-size: 13px',
      html: '<div class="root"></div>',
    });
    assert.equal(pane.style.width, undefined, 'width is never pinned');
    assert.equal(pane.style.height, undefined, 'height is never pinned');
    assert.equal(pane.style['background-color'], 'rgb(1, 2, 3)');
    assert.equal(pane.style['font-size'], '13px');
  } finally { uninstallGlobals(); }
});

test('mirror: popup without a known anchor falls back to pane-relative + viewport clamp', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      popup: { html: '<div class="context-view"><div class="monaco-list-row">A</div></div>', left: 1200, top: 750, width: 200, height: 100 },
    });
    const pop = pane.children[0];
    assert.equal(pop.getAttribute('class'), 'context-view');
    assert.equal(pop.style.position, 'fixed');
    // Fallback: pane origin (0,26) + (1200,750) = (1200,776); clamped to 1280x800.
    assert.equal(pop.style.left, '1080px');
    assert.equal(pop.style.top, '700px');
  } finally { uninstallGlobals(); }
});

test('mirror: mousedown forwards a press with the nearest control as anchor', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="monaco-toolbar"><div role="group"><span class="icon"></span></div></div></div>' });
    const group = pane.children[0].children[0].children[0];
    group.rect = { left: 100, top: 120, width: 120, height: 32 };
    const span = group.children[0];
    span.rect = { left: 108, top: 128, width: 16, height: 16 };
    span.dispatchEvent({ type: 'mousedown', target: span, clientX: 116, clientY: 136, button: 0, buttons: 1, detail: 1, preventDefault() {} });
    assert.deepEqual(sent(ws).at(-1), {
      type: 'mouse',
      kind: 'pressed',
      x: 116,
      y: 110, // pane-relative (viewport 136 - pane top 26)
      path: [0, 0, 0],
      relX: 0.5,
      relY: 0.5,
      button: 'left',
      buttons: 1,
      clickCount: 1,
      anchorPath: [0, 0],
    });
  } finally { uninstallGlobals(); }
});

test('mirror: popup anchors to the mirror copy of the last-clicked control', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="monaco-toolbar"><div role="group"><span class="icon"></span></div></div></div>' });
    const group = pane.children[0].children[0].children[0];
    group.rect = { left: 100, top: 120, width: 120, height: 32 };
    const span = group.children[0];
    span.rect = { left: 108, top: 128, width: 16, height: 16 };
    span.dispatchEvent({ type: 'mousedown', target: span, clientX: 116, clientY: 136, button: 0, buttons: 1, detail: 1, preventDefault() {} });
    // Live popup opens flush below the live anchor (same size as the mirror one).
    state(ws, {
      popup: {
        html: '<div class="context-view"><div class="monaco-list-row">M</div></div>',
        left: 100, top: 152, width: 160, height: 80,
        anchor: { left: 100, top: 120, width: 120, height: 32 },
      },
    });
    const pop = pane.children[1];
    assert.equal(pop.style.position, 'fixed');
    // Anchor-relative: mirror anchor (100,120) + live offset (0, 32).
    assert.equal(pop.style.left, '100px');
    assert.equal(pop.style.top, '152px');
  } finally { uninstallGlobals(); }
});

test('mirror: a state without a popup removes the rendered popup', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { popup: { html: '<div class="context-view"></div>', left: 10, top: 10, width: 50, height: 50 } });
    const pop = pane.children[0];
    state(ws, { html: '<div class="root"></div>' });
    assert.equal(pop.parentElement, null);
    assert.equal(pane.children.length, 0);
  } finally { uninstallGlobals(); }
});

test('mirror: mouseup forwards a release with the same element mapping', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="monaco-toolbar"><div role="group"><span class="icon"></span></div></div></div>' });
    const span = pane.children[0].children[0].children[0].children[0];
    span.rect = { left: 108, top: 128, width: 16, height: 16 };
    span.dispatchEvent({ type: 'mouseup', target: span, clientX: 116, clientY: 136, button: 0, buttons: 1, detail: 1, preventDefault() {} });
    assert.deepEqual(sent(ws).at(-1), {
      type: 'mouse',
      kind: 'released',
      x: 116,
      y: 110,
      path: [0, 0, 0],
      relX: 0.5,
      relY: 0.5,
      button: 'left',
      buttons: 1,
      clickCount: 1,
    });
  } finally { uninstallGlobals(); }
});

test('mirror: wheel on the chat scroller is chunked to 100px and flushed at 150ms', async () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: [0, 0],
    });
    const row = pane.querySelectorAll('.monaco-list-row')[0];
    row.rect = { left: 10, top: 100, width: 780, height: 40 };
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 250, deltaMode: 0, buttons: 0, preventDefault() {} });
    const wheels = sent(ws).filter(m => m.kind === 'wheel');
    assert.equal(wheels.length, 2, '250px -> two 100px chunks, remainder pending');
    // The scroller matches scrollPath: dispatch at its center, not the touched row.
    assert.deepEqual(wheels[0], {
      type: 'mouse',
      kind: 'wheel',
      x: 100,
      y: 114,
      path: [0, 0],
      relX: 0.5,
      relY: 0.5,
      deltaX: 0,
      deltaY: 100,
      buttons: 0,
    });
    assert.equal(wheels[1].deltaY, 100);
    await sleep(200); // FLUSH_MS = 150
    const after = sent(ws).filter(m => m.kind === 'wheel');
    assert.equal(after.length, 3);
    assert.equal(after[2].deltaY, 50, 'remainder flushed on idle');
  } finally { uninstallGlobals(); }
});

test('mirror: line-based wheel deltas (deltaMode 1) normalize to 20px/line', async () => {
  const { pane, ws } = setup();
  try {
    pane.dispatchEvent({ type: 'wheel', target: pane, clientX: 400, clientY: 300, deltaX: 0, deltaY: 3, deltaMode: 1, buttons: 0, preventDefault() {} });
    assert.equal(sent(ws).length, 0, '60px < 100px chunk: nothing sent yet');
    await sleep(200);
    const m = sent(ws).at(-1);
    assert.equal(m.kind, 'wheel');
    assert.equal(m.deltaY, 60);
    assert.equal(m.x, 400);
    assert.equal(m.y, 274);
  } finally { uninstallGlobals(); }
});

test('mirror: wheel moves the mirror locally AND forwards when live measured no scroller', async () => {
  const { pane, ws } = setup();
  try {
    // Live content fits (no live scrollbar): the server sends no scrollPath.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: null,
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    sc.scrollHeight = 700;
    sc.clientHeight = 500;
    sc.scrollTop = 200; // pinned at the bottom (max = 200)
    const row = pane.querySelector('.monaco-list-row');
    row.rect = { left: 10, top: 100, width: 780, height: 40 };
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: -300, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 0, 'scrolled up locally, clamped at the top');
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 3, 'still forwarded in 100px chunks (a no-op in live)');
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 400, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 200, 'scrolled down locally, clamped at the bottom');
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 7, 'forwarding continues');
  } finally { uninstallGlobals(); }
});

test('mirror: wheel is forwarded AND moves the mirror locally when live has a scroller', async () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    sc.scrollHeight = 900;
    sc.clientHeight = 500; // max = 400
    sc.scrollTop = 100;
    const row = pane.querySelector('.monaco-list-row');
    row.rect = { left: 10, top: 100, width: 780, height: 40 };
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 250, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 350, 'moved locally by the raw delta (the next sync re-anchors to live)');
    const wheels = sent(ws).filter(m => m.kind === 'wheel');
    assert.equal(wheels.length, 2, 'forwarded in 100px chunks');
    assert.equal(wheels[0].deltaY, 100);
    await sleep(200);
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 3, 'remainder flushed on idle');
    // A wheel past the bottom is clamped locally but still forwarded.
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 500, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 400, 'clamped at the bottom (max = 400)');
  } finally { uninstallGlobals(); }
});

test('mirror: fitting scroller (room 0) pins the mirror to the bottom; local wheel-up reveals the top', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500;
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Live content fits: scrollH == h, offset 0, every row rendered.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 700, offset: 0, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'pinned to the bottom (inside 700 - clientH 500)');
    rows[0].rect = { left: 10, top: 100, width: 780, height: 350 };
    rows[0].dispatchEvent({ type: 'wheel', target: rows[0], clientX: 100, clientY: 140, deltaX: 0, deltaY: -250, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 0, 'local wheel-up reveals the top (live cannot scroll)');
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 2, 'still forwarded (a no-op in live)');
  } finally { uninstallGlobals(); }
});

test('mirror: bottom-anchored list at the top keeps the bottom anchor (no teleport)', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Live list overflows (scrollH 1000 > h 700) and sits just below the
    // top: bottom-anchored, position in the negative offset.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'bottom anchor: above 8.75 + (inside 691.25 - 500), clamped to max 200');
    // Live reaches the very top (offset 0). The anchor must stay bottom:
    // the target stays 200 — a top anchor would flip it to 0 (a teleport).
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: 0, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'no teleport at the top (a top anchor would give 0)');
  } finally { uninstallGlobals(); }
});

test('mirror: top-anchored list (session picker) keeps the TOP of the live viewport', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Live session list at its very top: top-anchored, position in
    // scrollTop (0), offset 0. The mirror renders the live viewport's
    // content TALLER than its own viewport (inside 700 > clientH 500):
    // the TOP of the live viewport must stay on screen (scrollTop 0) —
    // the bottom anchor would cut the first row.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: 0, w: 800, h: 700, anchorKind: 'top' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 0, 'top anchor: above 0 (live at the top)');
    // Live scrolls down 100 and the rendered window shifts (row 0 leaves),
    // so the entry anchor is stale and the viewport re-anchors to live.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row" data-index="1"></div><div class="monaco-list-row" data-index="2"></div></div></div></div></div>',
    });
    const rowsNow = pane.querySelectorAll('.monaco-list-row');
    rowsNow[1].offsetHeight = 350; // the entering row's mirror geometry
    // Keep the TOP of the live viewport (v0 = 100, v1 = 800 -> above 87.5);
    // the bottom anchor would give 87.5 + (612.5 - 500) = 200 instead.
    state(ws, {
      scroll: { left: 0, top: 100, scrollH: 1000, offset: 0, w: 800, h: 700, anchorKind: 'top' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 87.5, 'top anchor: above 87.5 (live viewport top on screen)');
  } finally { uninstallGlobals(); }
});

test('mirror: html updates keep the viewport anchored to the message entry the user is reading', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '</div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Anchor with the live viewport stationary (v0 = 10, v1 = 710).
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'anchored to the bottom of the live viewport');
    // The user wheels up to read the first message (a local move + forward).
    rows[0].rect = { left: 10, top: 100, width: 780, height: 350 };
    rows[0].dispatchEvent({ type: 'wheel', target: rows[0], clientX: 100, clientY: 140, deltaX: 0, deltaY: -150, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 50, 'local wheel-up');
    // Streaming: an html update grows the content (a new row appended) but
    // the user is not at the bottom -> the viewport stays pinned to the
    // entry being read (no re-anchor to live's pixel position).
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '<div class="monaco-list-row" data-index="2"></div>' +
        '</div></div></div></div>',
      scroll: { left: 0, top: 0, scrollH: 1100, offset: -10, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 50, 'content-only html update keeps the reading position');
    // The live viewport moves (live scrolled down 100) while the user is
    // still reading the first message -> the mirror keeps its entry, not
    // live's new pixel position.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1100, offset: -110, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 50, 'a moved live viewport does not yank the reading position');
  } finally { uninstallGlobals(); }
});

test('mirror: pinned to the bottom, the viewport follows the stream across html updates', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '</div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Live is AT its bottom: distBottom = (scrollH - h) + offset = 0
    // (bottom-anchored, viewport [300, 1000] of a 1000px list).
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -300, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [300, 350, 650, 350],
    });
    assert.equal(sc.scrollTop, 200, 'anchored to the bottom (pinned)');
    // Streaming: the mirror's content grows (a new row, scrollHeight 800)
    // and live stays at the bottom -> the viewport follows the bottom.
    sc.scrollHeight = 800; // max is now 300
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '<div class="monaco-list-row" data-index="2"></div>' +
        '</div></div></div></div>',
      scroll: { left: 0, top: 0, scrollH: 1200, offset: -500, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [300, 350, 650, 350],
    });
    assert.equal(sc.scrollTop, 300, 'pinned: follows the bottom of the mirror content');
  } finally { uninstallGlobals(); }
});

test('mirror: at the bottom of the rendered window but NOT live bottom, a new message does not teleport the viewport', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '</div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    // Live is NOT at its bottom: distBottom = (1000 - 700) + (-10) = 290.
    // The mirror's rendered window is the top of the list; there are
    // unrendered messages below in live.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'at the bottom of the rendered window');
    // A new message appears below (live renders it): the mirror's content
    // grows (a new row, scrollHeight 800) but live is still not at its
    // bottom -> the viewport must STAY at the old position (200), not
    // teleport to the bottom of the new message (300).
    sc.scrollHeight = 800; // max is now 300
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '<div class="monaco-list-row" data-index="2"></div>' +
        '</div></div></div></div>',
      scroll: { left: 0, top: 0, scrollH: 1100, offset: -10, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'no teleport: stays at the old reading position');
  } finally { uninstallGlobals(); }
});

test('mirror: an anchor row that leaves the rendered window re-anchors the viewport to live', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '</div></div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'anchored to the bottom');
    // The user wheels up to read the first message (anchor row 0, offset 50).
    rows[0].rect = { left: 10, top: 100, width: 780, height: 350 };
    rows[0].dispatchEvent({ type: 'wheel', target: rows[0], clientX: 100, clientY: 140, deltaX: 0, deltaY: -150, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 50, 'local wheel-up');
    // Live scrolls down past the first message: the html update no longer
    // renders row 0, so the anchor is stale and the viewport re-anchors to
    // live's viewport (v0 = 400, v1 = 1100; above 0, inside 700 -> 200).
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '<div class="monaco-list-row" data-index="2"></div>' +
        '</div></div></div></div>',
    });
    const rowsNow = pane.querySelectorAll('.monaco-list-row');
    assert.equal(rowsNow[0].getAttribute('data-index'), '1', 'row 0 discarded');
    rowsNow[1].offsetHeight = 350; // the entering row's mirror geometry
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1100, offset: -400, w: 800, h: 700, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [400, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 're-anchored to the bottom of the live viewport');
  } finally { uninstallGlobals(); }
});

test('mirror: wheeling the trace list does not clobber the main scroller entry anchor', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="0"></div>' +
        '<div class="monaco-list-row" data-index="1"></div>' +
        '</div></div></div><div class="chat-thinking-box"><div class="chat-used-context-list">' +
        '<div class="monaco-list-row" data-index="0">t0</div>' +
        '<div class="monaco-list-row" data-index="1">t1</div>' +
        '</div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const rows = pane.querySelectorAll('.monaco-list-row');
    sc.scrollHeight = 700;
    sc.clientHeight = 500; // max = 200
    rows[0].offsetHeight = 350;
    rows[1].offsetHeight = 350;
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 200, 'anchored to the bottom');
    // Wheel up on the MAIN list: anchor row 0, offset 50.
    rows[0].rect = { left: 10, top: 100, width: 780, height: 350 };
    rows[0].dispatchEvent({ type: 'wheel', target: rows[0], clientX: 100, clientY: 140, deltaX: 0, deltaY: -150, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 50, 'local wheel-up on the main list');
    // Wheel the TRACE list: its rows also carry data-index 0/1, so a
    // careless anchor update would overwrite the main anchor with a trace
    // row. The main anchor must survive.
    const trace = pane.querySelector('.chat-used-context-list');
    trace.scrollHeight = 400;
    trace.clientHeight = 200; // max = 200
    const traceRows = trace.querySelectorAll('.monaco-list-row');
    traceRows[0].offsetHeight = 200;
    traceRows[1].offsetHeight = 200;
    trace.scrollTop = 100;
    traceRows[1].rect = { left: 10, top: 150, width: 780, height: 200 };
    traceRows[1].dispatchEvent({ type: 'wheel', target: traceRows[1], clientX: 100, clientY: 160, deltaX: 0, deltaY: -100, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(trace.scrollTop, 0, 'the trace list scrolled locally');
    // A state message: the main scroller keeps its entry anchor (row 0 at
    // offset 50), not the trace row the user just scrolled.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 1000, offset: -10, w: 800, h: 700 },
      scrollPath: [0, 0],
      scrollRows: [0, 400, 400, 300],
    });
    assert.equal(sc.scrollTop, 50, 'main anchor not clobbered by the trace wheel');
  } finally { uninstallGlobals(); }
});

test('mirror: html updates diff the virtualized rows by data-index (staying reused, entering added, leaving discarded)', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="2">a</div>' +
        '<div class="monaco-list-row" data-index="3">b</div>' +
        '</div></div></div></div>',
    });
    const rowsEl = pane.querySelector('.monaco-list-rows');
    const row2 = rowsEl.children[0];
    const row3 = rowsEl.children[1];
    row3._marker = 7;
    // The rendered window shifts down by one row: 2 leaves, 4 enters, 3 stays.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="3">b edited</div>' +
        '<div class="monaco-list-row" data-index="4">c</div>' +
        '</div></div></div></div>',
    });
    assert.equal(rowsEl.children.length, 2);
    assert.equal(rowsEl.children[0], row3, 'staying row keeps its element');
    assert.equal(row3._marker, 7);
    assert.equal(row3.children[0].data, 'b edited', 'staying row patched in place');
    assert.equal(rowsEl.children[1].getAttribute('data-index'), '4', 'entering row adopted');
    assert.equal(row2.parentElement, null, 'leaving row discarded');
  } finally { uninstallGlobals(); }
});

test('mirror: rows entering at the top are inserted before the staying rows', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="3">b</div>' +
        '<div class="monaco-list-row" data-index="4">c</div>' +
        '</div></div></div></div>',
    });
    const rowsEl = pane.querySelector('.monaco-list-rows');
    const row3 = rowsEl.children[0];
    const row4 = rowsEl.children[1];
    // The rendered window shifts up by one row: 2 enters at the top.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows">' +
        '<div class="monaco-list-row" data-index="2">a</div>' +
        '<div class="monaco-list-row" data-index="3">b</div>' +
        '<div class="monaco-list-row" data-index="4">c</div>' +
        '</div></div></div></div>',
    });
    assert.equal(rowsEl.children.length, 3);
    assert.equal(rowsEl.children[0].getAttribute('data-index'), '2', 'entering row inserted first');
    assert.equal(rowsEl.children[1], row3, 'staying row keeps its element');
    assert.equal(rowsEl.children[2], row4, 'staying row keeps its element');
  } finally { uninstallGlobals(); }
});

test('mirror: wheel is still forwarded when the mirror list does not overflow', async () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="monaco-list-rows"><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: null,
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    sc.scrollHeight = 500;
    sc.clientHeight = 500; // fits in the mirror too: nothing local to scroll
    const row = pane.querySelector('.monaco-list-row');
    row.rect = { left: 10, top: 100, width: 780, height: 40 };
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 250, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 0, 'no local scroll');
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 2, 'forwarded in 100px chunks');
    await sleep(200);
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 3, 'remainder flushed on idle');
  } finally { uninstallGlobals(); }
});

test('mirror: a locally scrollable list shows its drawn scrollbar track', () => {
  const { pane, ws } = setup();
  try {
    // The track arrives with the live .invisible class (opacity 0) because
    // the live content fits; the mirror must reveal it when it overflows.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element"><div class="scrollbar invisible vertical"><div class="slider"></div></div><div class="monaco-list-rows"><div class="monaco-list-row"></div></div></div></div></div>',
      scrollPath: null,
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    sc.scrollHeight = 700;
    sc.clientHeight = 500;
    // In-flow content height: the track-top clamp must see the rows, not 0.
    sc.querySelector('.monaco-list-rows').offsetHeight = 700;
    // A scroll-only state re-runs syncDrawnScrollbars with the real geometry.
    state(ws, { scroll: { left: 0, top: 0, scrollH: 700, offset: 0, w: 800, h: 500 }, scrollPath: null });
    const track = pane.querySelector('.scrollbar');
    assert.equal(track.style.opacity, '1', 'track visible when the mirror overflows');
    assert.ok(parseFloat(track.querySelector('.slider').style.height) >= 20, 'slider sized from the mirror geometry');
  } finally { uninstallGlobals(); }
});

test('mirror: changing the window picker sends a window message', () => {
  const { status, ws } = setup();
  try {
    status.value = 'win-2';
    status.dispatchEvent(new Event('change', status));
    assert.deepEqual(sent(ws).at(-1), { type: 'window', id: 'win-2' });
  } finally { uninstallGlobals(); }
});

test('mirror: selecting the Workspaces entry navigates and remembers the window', () => {
  const { status, ws, win } = setup();
  const restoreLS = withLocalStorage();
  try {
    state(ws, { windows: [{ id: 'win-1', title: 'A' }], windowId: 'win-1' });
    status.value = '__workspaces__';
    status.dispatchEvent(new Event('change', status));
    assert.equal(win.location.href, '/workspaces');
    assert.equal(globalThis.localStorage.getItem('mirrorWin'), 'win-1');
    assert.equal(sent(ws).length, 0, 'no window message for the special entry');
  } finally { restoreLS(); uninstallGlobals(); }
});

test('mirror: returning restores the remembered window exactly once', () => {
  const { ws } = setup();
  const restoreLS = withLocalStorage({ mirrorWin: 'win-2' });
  try {
    state(ws, { windows: [{ id: 'win-1', title: 'A' }, { id: 'win-2', title: 'B' }], windowId: 'win-1' });
    assert.deepEqual(sent(ws).filter(m => m.type === 'window'), [{ type: 'window', id: 'win-2' }]);
    assert.equal(globalThis.localStorage.getItem('mirrorWin'), null, 'remembered window consumed');
    // A later state does not re-send it.
    state(ws, { windows: [{ id: 'win-1', title: 'A' }, { id: 'win-2', title: 'B' }], windowId: 'win-1' });
    assert.equal(sent(ws).filter(m => m.type === 'window').length, 1);
  } finally { restoreLS(); uninstallGlobals(); }
});

test('mirror: a stale remembered window is dropped silently', () => {
  const { ws } = setup();
  const restoreLS = withLocalStorage({ mirrorWin: 'gone' });
  try {
    state(ws, { windows: [{ id: 'win-1', title: 'A' }], windowId: 'win-1' });
    assert.equal(sent(ws).filter(m => m.type === 'window').length, 0);
    assert.equal(globalThis.localStorage.getItem('mirrorWin'), null);
  } finally { restoreLS(); uninstallGlobals(); }
});

test('mirror: ?ws= is forwarded to the WS handshake URL', () => {
  const uri = 'file:///c%3A/Users/deort/vscode-load-llama';
  const { ws } = setup({ search: '?ws=' + encodeURIComponent(uri) });
  try {
    assert.equal(ws.url, 'ws://127.0.0.1:8123/ws?ws=' + encodeURIComponent(uri));
  } finally { uninstallGlobals(); }
});

test('mirror: ?ws= suppresses the remembered-window restore', () => {
  const uri = 'file:///c%3A/Users/deort/vscode-load-llama';
  const { ws } = setup({ search: '?ws=' + encodeURIComponent(uri) });
  const restoreLS = withLocalStorage({ mirrorWin: 'win-1' });
  try {
    state(ws, { windows: [{ id: 'win-1', title: 'A' }, { id: 'win-2', title: 'B' }], windowId: 'win-1' });
    assert.equal(sent(ws).filter(m => m.type === 'window').length, 0, 'server already applied the ?ws= selection');
    assert.equal(globalThis.localStorage.getItem('mirrorWin'), 'win-1', 'remembered window not consumed');
  } finally { restoreLS(); uninstallGlobals(); }
});

test('mirror: keydown forwards non-printable keys, suppresses plain typing and composition', () => {
  const { pane, ws } = setup();
  try {
    pane.dispatchEvent({ type: 'keydown', target: pane, key: 'a', code: 'KeyA', keyCode: 65, isComposing: false });
    assert.equal(sent(ws).length, 0, 'printable char without modifiers is not forwarded');
    pane.dispatchEvent({ type: 'keydown', target: pane, key: 'Enter', code: 'Enter', keyCode: 13, isComposing: false });
    assert.deepEqual(sent(ws).at(-1), { type: 'key', kind: 'down', key: 'Enter', code: 'Enter', keyCode: 13, modifiers: 0, text: '' });
    pane.dispatchEvent({ type: 'keydown', target: pane, key: 'a', code: 'KeyA', keyCode: 65, ctrlKey: true, isComposing: false });
    assert.deepEqual(sent(ws).at(-1), { type: 'key', kind: 'down', key: 'a', code: 'KeyA', keyCode: 65, modifiers: 2, text: '' });
    pane.dispatchEvent({ type: 'keydown', target: pane, key: 'Enter', code: 'Enter', keyCode: 13, isComposing: true });
    assert.equal(sent(ws).length, 2, 'keys during composition are suppressed');
  } finally { uninstallGlobals(); }
});

test('mirror: input events forward text and clear the local textarea', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="chat-input-container"><textarea class="ime-text-area"></textarea></div></div>' });
    const ta = pane.querySelector('textarea');
    ta.value = 'abc';
    ta.dispatchEvent({ type: 'input', target: ta, data: 'hello', inputType: 'insertText' });
    assert.deepEqual(sent(ws).at(-1), { type: 'key', kind: 'insert', text: 'hello' });
    assert.equal(ta.value, '');
  } finally { uninstallGlobals(); }
});

test('mirror: compositionend forwards committed text (falling back to the textarea value)', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"><div class="chat-input-container"><textarea class="ime-text-area"></textarea></div></div>' });
    const ta = pane.querySelector('textarea');
    ta.dispatchEvent({ type: 'compositionend', target: ta, data: '你好' });
    assert.deepEqual(sent(ws).at(-1), { type: 'key', kind: 'insert', text: '你好' });
    assert.equal(ta.value, '');
    ta.value = 'fallback';
    ta.dispatchEvent({ type: 'compositionend', target: ta, data: '' });
    assert.deepEqual(sent(ws).at(-1), { type: 'key', kind: 'insert', text: 'fallback' });
    assert.equal(ta.value, '');
  } finally { uninstallGlobals(); }
});

test('mirror: drawn scrollbar mirrors the live slider, and local scroll pins track + sticky', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element">' +
        '<div class="monaco-list-rows"></div>' +
        '<div class="scrollbar vertical"><div class="slider"></div></div>' +
        '<div class="monaco-tree-sticky-container"></div>' +
        '</div></div></div>',
      // Bottom-anchored chat list: scrollTop 0, position in the negative offset.
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
    });
    const scroller = pane.children[0].children[0].children[0];
    scroller.clientHeight = 300;
    scroller.scrollHeight = 600;
    // In-flow content fills the scroller, so the track-top clamp is a no-op.
    scroller.children[0].offsetHeight = 600;
    const track = scroller.children[1];
    const slider = track.children[0];
    const sticky = scroller.children[2];
    scroller.dispatchEvent(new Event('scroll', scroller));
    // liveRatio = (0 - (-100)) / (1000 - 300) = 1/7; sliderH = 300 * 300/1000 = 90.
    assert.equal(track.style.height, '300px');
    assert.equal(track.style.top, '0px');
    assert.equal(slider.style.height, '90px');
    assert.equal(slider.style.top, '30px'); // 1/7 * (300 - 90)
    assert.equal(sticky.style.top, '0px');
    // Local scroll: the track stays pinned to the visible top, the sticky offsets.
    scroller.scrollTop = 50;
    scroller.dispatchEvent(new Event('scroll', scroller));
    assert.equal(track.style.top, '50px');
    assert.equal(sticky.style.top, '-50px');
  } finally { uninstallGlobals(); }
});

test('mirror: a shrunken content does not inflate the scroll range via the drawn track', async () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element">' +
        '<div class="scrollbar invisible vertical"><div class="slider"></div></div>' +
        '<div class="monaco-list-rows"><div class="monaco-list-row" data-index="0"></div></div>' +
        '</div></div></div>',
      scrollPath: [0, 0],
    });
    const sc = pane.querySelector('.monaco-scrollable-element');
    const row = sc.querySelector('.monaco-list-row');
    // The thinking box collapsed: the mirror content shrank to 525px in a
    // 411px viewport, but the scroller still carries the stale inflated
    // scrollHeight (762) from when the content was taller.
    sc.scrollHeight = 762;
    sc.clientHeight = 411;
    sc.querySelector('.monaco-list-rows').offsetHeight = 525;
    row.offsetHeight = 525;
    // Live still fits one screen (scrollH == h) and sits at its bottom.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 762, offset: 0, w: 800, h: 762, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 525],
    });
    assert.equal(sc.scrollTop, 114, 'bottom anchor: inside 525 - clientH 411');
    // The user wheels to the bottom: the (stale, inflated) max is 351, so
    // the mirror pins at 351 — the exact bug state before the fix.
    row.rect = { left: 10, top: 100, width: 780, height: 525 };
    row.dispatchEvent({ type: 'wheel', target: row, clientX: 100, clientY: 140, deltaX: 0, deltaY: 300, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(sc.scrollTop, 351, 'pinned at the stale max');
    // The next state message re-applies the phantom max (pinned branch), but
    // the track must NOT extend the scrollable overflow area with it.
    state(ws, {
      scroll: { left: 0, top: 0, scrollH: 762, offset: 0, w: 800, h: 762, anchorKind: 'bottom' },
      scrollPath: [0, 0],
      scrollRows: [0, 525],
    });
    const track = sc.querySelector('.scrollbar');
    // The track's bottom is clamped to the content bottom (525 - 411 = 114);
    // offset by the stale scrollTop (351) it would extend the scrollable
    // area to 762 and leave a 237px blank strip below the last row.
    assert.equal(track.style.top, '114px', 'track top clamped to the content bottom');
    assert.equal(track.style.height, '411px');
    await sleep(200); // let the wheel flush timer fire (before uninstalling)
  } finally { uninstallGlobals(); }
});

test('mirror: reasoning-trace list follows the live bottom-pinned scroll', () => {
  const { pane, ws } = setup();
  try {
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element">' +
        '<div class="monaco-list-rows"><div class="monaco-list-row">' +
        '<div class="chat-used-context chat-thinking-box"><div class="monaco-scrollable-element">' +
        '<div class="chat-used-context-list chat-thinking-streaming"></div>' +
        '<div class="scrollbar vertical"><div class="slider"></div></div>' +
        '</div></div></div></div></div></div></div>',
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
      // Live list pinned to the bottom: top = scrollH - clientH.
      nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 360, scrollH: 480, clientH: 120 }],
    });
    // root > monaco-list(0) > scroller(0) > rows(0) > row(0) > box(0) > wrap(0) > list(0)
    const rootEl = pane.children[0];
    const scroller = rootEl.children[0].children[0];
    scroller.clientHeight = 300;
    scroller.scrollHeight = 600;
    const list = scroller.children[0].children[0].children[0].children[0].children[0];
    list.scrollHeight = 480;
    list.clientHeight = 120;
    const wrap = list.parentElement;
    wrap.clientHeight = 240;
    const vtrack = wrap.children[1];
    const vslider = vtrack.children[0];
    // The stub has no layout, so the geometry is set after the first state;
    // a scroll-only update re-runs the restore with the geometry in place.
    state(ws, {
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
      nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 360, scrollH: 480, clientH: 120 }],
    });
    // ratio = 360 / (480 - 120) = 1 -> the mirror list pins to its own bottom.
    assert.equal(list.scrollTop, 360); // 1 * (480 - 120)
    // The slider mirrors the live bottom position, scaled to the mirror wrap.
    assert.equal(vtrack.style.height, '240px');
    assert.equal(vtrack.style.top, '0px'); // wrap.scrollTop stays 0 (the LIST scrolls)
    assert.equal(vslider.style.height, '60px'); // 240 * 120/480
    assert.equal(vslider.style.top, '180px'); // 1 * (240 - 60)
    // A later scroll-only update keeps the list following the live ratio.
    state(ws, {
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
      nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 180, scrollH: 480, clientH: 120 }],
    });
    assert.equal(list.scrollTop, 180); // 0.5 * (480 - 120)
    assert.equal(vslider.style.top, '90px'); // 0.5 * (240 - 60)
  } finally { uninstallGlobals(); }
});

// Shared setup helper for the reasoning-trace tests: the trace box lives in a
// row of the main chat list (root > monaco-list > scroller > rows > row >
// box > wrap > list). Returns { pane, ws, scroller, list }.
function traceSetup() {
  const { pane, ws } = setup();
  state(ws, {
    html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element">' +
      '<div class="monaco-list-rows"><div class="monaco-list-row">' +
      '<div class="chat-used-context chat-thinking-box"><div class="monaco-scrollable-element">' +
      '<div class="chat-used-context-list chat-thinking-streaming"></div>' +
      '<div class="scrollbar vertical"><div class="slider"></div></div>' +
      '</div></div></div></div></div></div></div>',
    scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
    scrollPath: [0, 0],
    // Live list pinned to the bottom: top = scrollH - clientH.
    nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 360, scrollH: 480, clientH: 120 }],
  });
  const rootEl = pane.children[0];
  const scroller = rootEl.children[0].children[0];
  scroller.clientHeight = 300;
  scroller.scrollHeight = 600; // main max = 300
  const list = scroller.children[0].children[0].children[0].children[0].children[0];
  list.scrollHeight = 480;
  list.clientHeight = 120; // list max = 360
  list.parentElement.clientHeight = 240;
  // The stub has no layout, so the geometry is set after the first state;
  // a scroll-only update re-runs the restores with the geometry in place.
  state(ws, {
    scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
    scrollPath: [0, 0],
    nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 360, scrollH: 480, clientH: 120 }],
  });
  return { pane, ws, scroller, list };
}

test('mirror: wheel on a reasoning-trace list scrolls the list, not the main viewport', () => {
  const { pane, ws, scroller, list } = traceSetup();
  try {
    assert.equal(list.scrollTop, 360, 'pinned to the live bottom ratio');
    const mainTop = scroller.scrollTop;
    // The user wheels up over the trace.
    list.rect = { left: 10, top: 100, width: 300, height: 120 };
    list.dispatchEvent({ type: 'wheel', target: list, clientX: 100, clientY: 140, deltaX: 0, deltaY: -250, deltaMode: 0, buttons: 0, preventDefault() {} });
    assert.equal(list.scrollTop, 110, 'the trace list scrolls locally (360 - 250)');
    assert.equal(scroller.scrollTop, mainTop, 'the main viewport does not move');
    assert.equal(sent(ws).filter(m => m.kind === 'wheel').length, 2, 'still forwarded in 100px chunks');
  } finally { uninstallGlobals(); }
});

test('mirror: html updates that do not change the live trace state keep the local trace position', () => {
  const { pane, ws, scroller, list } = traceSetup();
  try {
    assert.equal(list.scrollTop, 360, 'pinned to the live bottom ratio');
    // The user reads above the pinned bottom (a local wheel-up).
    list.scrollTop = 100;
    // An html update elsewhere in the pane (a new row) with the live trace
    // state unchanged must not yank the local trace position back to 360.
    state(ws, {
      html: '<div class="root"><div class="monaco-list"><div class="monaco-scrollable-element">' +
        '<div class="monaco-list-rows"><div class="monaco-list-row">' +
        '<div class="chat-used-context chat-thinking-box"><div class="monaco-scrollable-element">' +
        '<div class="chat-used-context-list chat-thinking-streaming"></div>' +
        '<div class="scrollbar vertical"><div class="slider"></div></div>' +
        '</div></div></div><div class="monaco-list-row"></div></div></div></div></div>',
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
      nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 360, scrollH: 480, clientH: 120 }],
    });
    assert.equal(list.scrollTop, 100, 'content-only update keeps the local trace position');
    // The live trace moves (streaming grew it and re-pinned: new scrollH) ->
    // the live ratio re-applies.
    state(ws, {
      scroll: { left: 0, top: 0, offset: -100, h: 300, scrollH: 1000 },
      scrollPath: [0, 0],
      nestedScrolls: [{ path: [0, 0, 0, 0, 0, 0, 0], top: 480, scrollH: 600, clientH: 120 }],
    });
    assert.equal(list.scrollTop, 360, 'a moved live trace re-applies the live ratio (1 * 360)');
  } finally { uninstallGlobals(); }
});

// Pane HTML with the chat-input cursor: root > chat-input-container >
// interactive-input-editor > monaco-editor > cursors-layer > cursor.
const CURSOR_HTML =
  '<div class="root"><div class="chat-input-container">' +
  '<div class="interactive-input-editor"><div class="monaco-editor">' +
  '<div class="cursors-layer"><div class="cursor"></div></div>' +
  '</div></div></div></div>';

function hasBlink(el) {
  return (el.getAttribute('class') || '').split(/\s+/).includes('mirror-cursor-blink');
}

test('mirror: focused chat input shows a blinking cursor', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: CURSOR_HTML, inputFocused: true });
    const cur = pane.querySelector('.cursor');
    assert.equal(cur.style.visibility, 'inherit');
    assert.ok(hasBlink(cur));
  } finally { uninstallGlobals(); }
});

test('mirror: unfocusing the chat input drops the blink class', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: CURSOR_HTML, inputFocused: true });
    const cur = pane.querySelector('.cursor');
    assert.ok(hasBlink(cur));
    state(ws, { html: CURSOR_HTML, inputFocused: false });
    assert.equal(cur, pane.querySelector('.cursor'), 'cursor node keeps identity');
    assert.ok(!hasBlink(cur));
  } finally { uninstallGlobals(); }
});

test('mirror: scroll-only state keeps the cursor blink in sync', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: CURSOR_HTML, inputFocused: true });
    const cur = pane.querySelector('.cursor');
    assert.ok(hasBlink(cur));
    // No html in the message: the scroll-only path must still sync the cursor.
    state(ws, { inputFocused: false });
    assert.ok(!hasBlink(cur));
    state(ws, { inputFocused: true });
    assert.ok(hasBlink(cur));
    assert.equal(cur.style.visibility, 'inherit');
  } finally { uninstallGlobals(); }
});

test('mirror: syncCursor is a no-op without a chat input editor', () => {
  const { pane, ws } = setup();
  try {
    state(ws, { html: '<div class="root"></div>', inputFocused: true });
    assert.equal(pane.querySelector('.cursor'), null);
  } finally { uninstallGlobals(); }
});

// Chat-input editor with text: root > chat-input-container >
// interactive-input-editor > monaco-editor > overflow-guard >
// editor-scrollable > lines-content > view-lines > view-line > span, with
// the cursors-layer (and its cursor) inside lines-content, like live.
const INPUT_EDITOR_HTML =
  '<div class="root"><div class="chat-input-container">' +
  '<div class="interactive-input-editor"><div class="monaco-editor">' +
  '<div class="overflow-guard"><div class="monaco-scrollable-element editor-scrollable">' +
  '<div class="lines-content">' +
  '<div class="view-lines"><div class="view-line"><span>abcdefgh</span></div></div>' +
  '<div class="cursors-layer"><div class="cursor" style="top: 12px; left: 494px;"></div></div>' +
  '</div></div></div>' +
  '</div></div></div></div>';

// Deliver the editor HTML, give the editor/span their mirror rects, then
// deliver the same state again so the re-seat runs against real geometry
// (the first delivery re-seats against zero rects, which is a no-op).
function seatEditor({ pane, ws }, cursorChar) {
  state(ws, { html: INPUT_EDITOR_HTML, inputFocused: true, cursorChar });
  const ed = pane.querySelector('.monaco-editor');
  const span = pane.querySelector('.view-line span');
  const cur = pane.querySelector('.cursor');
  // Editor (10,100) 400x44; the 8-char span (10,112) 80x20 → 10px/char.
  ed.rect = { left: 10, top: 100, width: 400, height: 44 };
  span.rect = { left: 10, top: 112, width: 80, height: 20 };
  state(ws, { html: INPUT_EDITOR_HTML, inputFocused: true, cursorChar });
  return { ed, span, cur };
}

test('mirror: cursor re-seats onto the mirror reflowed text (mid-text)', () => {
  const { pane, ws } = setup();
  try {
    const { cur } = seatEditor({ pane, ws }, 4);
    // Character 4 ('e') starts at x = 10 + 4*10 = 50 → editor-relative 40;
    // top 112 → editor-relative 12. The live pixels (494,12) are replaced.
    assert.equal(cur.style.left, '40px');
    assert.equal(cur.style.top, '12px');
  } finally { uninstallGlobals(); }
});

test('mirror: cursor at the end of the text uses the last character right edge', () => {
  const { pane, ws } = setup();
  try {
    const { cur } = seatEditor({ pane, ws }, 8);
    // After character 7: right edge x = 10 + 8*10 = 90 → editor-relative 80.
    assert.equal(cur.style.left, '80px');
    assert.equal(cur.style.top, '12px');
  } finally { uninstallGlobals(); }
});

test('mirror: cursor at the start of the text sits at the first character left edge', () => {
  const { pane, ws } = setup();
  try {
    const { cur } = seatEditor({ pane, ws }, 0);
    assert.equal(cur.style.left, '0px');
    assert.equal(cur.style.top, '12px');
  } finally { uninstallGlobals(); }
});

test('mirror: empty input (no text nodes) does not throw and skips the re-seat', () => {
  const { pane, ws } = setup();
  try {
    const html =
      '<div class="root"><div class="chat-input-container">' +
      '<div class="interactive-input-editor"><div class="monaco-editor">' +
      '<div class="view-lines"><div class="view-line"></div></div>' +
      '<div class="cursors-layer"><div class="cursor" style="top: 12px; left: 0px;"></div></div>' +
      '</div></div></div></div>';
    state(ws, { html, inputFocused: true, cursorChar: 0 });
    const cur = pane.querySelector('.cursor');
    assert.ok(cur, 'cursor survives');
    assert.equal(cur.style.left, undefined, 'no re-seat without text nodes');
  } finally { uninstallGlobals(); }
});

test('mirror: viewport resize re-seats the cursor on the reflowed text', () => {
  const { pane, ws, win } = setup();
  try {
    const { span, cur } = seatEditor({ pane, ws }, 4);
    assert.equal(cur.style.left, '40px');
    // The mirror widens: the same text now reflows at 20px/char.
    span.rect = { left: 10, top: 112, width: 160, height: 20 };
    win.dispatchEvent({ type: 'resize' });
    assert.equal(cur.style.left, '80px', 'character 4 at 10 + 4*20 = 90 → editor-relative 80');
  } finally { uninstallGlobals(); }
});
