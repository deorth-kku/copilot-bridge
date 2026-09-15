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

function setup() {
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
  const win = makeWindow(doc, { host: '127.0.0.1:8123' });
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
    assert.equal(status.children.length, 3);
    assert.equal(status.children[0].value, 'win-123456');
    assert.equal(status.children[0].textContent, 'W (123456)');
    assert.equal(status.children[1].textContent, 'W (654321)');
    assert.equal(status.children[2].textContent, 'X');
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
    assert.equal(status.children.length, 2);
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

test('mirror: changing the window picker sends a window message', () => {
  const { status, ws } = setup();
  try {
    status.value = 'win-2';
    status.dispatchEvent(new Event('change', status));
    assert.deepEqual(sent(ws).at(-1), { type: 'window', id: 'win-2' });
  } finally { uninstallGlobals(); }
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
