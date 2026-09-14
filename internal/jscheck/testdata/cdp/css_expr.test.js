// Unit tests for cssExpr (internal/cdp/extract.go): dumps stylesheets and
// rewrites @font-face font URLs into inlined base64 data URIs.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const cssExpr = require('../modules/css_expr.js');

const SHEET_HREF = 'vscode-file://vscode-app/sheet.css';
const FONT_BYTES = 'FONTBYTES';
const FONT_B64 = Buffer.from(FONT_BYTES).toString('base64');

function sheet(href, cssText) {
  return { href, cssRules: [{ cssText }] };
}

async function run(sheets, fetchImpl) {
  const root = new El('div');
  const doc = makeDocument(root, { styleSheets: sheets });
  const win = makeWindow(doc);
  installGlobals(doc, win, { fetch: fetchImpl });
  try {
    return await cssExpr();
  } finally { uninstallGlobals(); }
}

const okFetch = url => ({
  url,
  ok: true,
  arrayBuffer: async () => new TextEncoder().encode(FONT_BYTES).buffer,
});

test('cssExpr: inlines an absolute woff2 font as a data URI', async () => {
  const abs = SHEET_HREF.replace('sheet.css', 'fonts/camelia.woff2');
  const fetchImpl = async u => {
    assert.equal(u, abs);
    return okFetch(u);
  };
  const { css } = await run([sheet(SHEET_HREF, "@font-face { font-family: 'Camelia'; src: url('" + abs + "') format('woff2'); }")], fetchImpl);
  assert.ok(css.includes("src: url('data:font/woff2;base64," + FONT_B64 + "')"), css);
});

test('cssExpr: relative font URL resolved against the sheet href', async () => {
  const abs = 'vscode-file://vscode-app/fonts/c.woff2';
  const fetchImpl = async u => {
    assert.equal(u, abs);
    return okFetch(u);
  };
  const { css } = await run([sheet(SHEET_HREF, "@font-face { src: url('fonts/c.woff2'); }")], fetchImpl);
  assert.ok(css.includes("url('data:font/woff2;base64," + FONT_B64 + "')"), css);
});

test('cssExpr: ?query tail does not corrupt the data URI (regression)', async () => {
  const abs = 'vscode-file://vscode-app/fonts/c.woff2';
  const fetchImpl = async u => {
    assert.equal(u, abs, 'query must be stripped before fetch');
    return okFetch(u);
  };
  const { css } = await run([sheet(SHEET_HREF, "@font-face { src: url('fonts/c.woff2?v=7'); }")], fetchImpl);
  assert.ok(css.includes("url('data:font/woff2;base64," + FONT_B64 + "')"), css);
  assert.ok(!css.includes(FONT_B64 + '?v=7'), 'query tail must not trail the base64 payload');
});

test('cssExpr: unquoted url keeps unquoted form', async () => {
  const abs = 'vscode-file://vscode-app/fonts/a.ttf';
  const fetchImpl = async u => {
    assert.equal(u, abs);
    return okFetch(u);
  };
  const { css } = await run([sheet(SHEET_HREF, '@font-face { src: url(fonts/a.ttf); }')], fetchImpl);
  assert.ok(css.includes('url(data:font/ttf;base64,' + FONT_B64 + ')'), css);
});

test('cssExpr: non-font urls are left untouched and not fetched', async () => {
  let fetched = 0;
  const fetchImpl = async () => { fetched++; return okFetch('x'); };
  const { css } = await run([sheet(SHEET_HREF, '.x { background: url(\'img/pic.png\'); }')], fetchImpl);
  assert.equal(fetched, 0);
  assert.ok(css.includes("url('img/pic.png')"), css);
});

test('cssExpr: fetch failure leaves the url untouched', async () => {
  const fetchImpl = async () => { throw new Error('network down'); };
  const { css } = await run([sheet(SHEET_HREF, "@font-face { src: url('fonts/c.woff2'); }")], fetchImpl);
  assert.ok(css.includes("url('fonts/c.woff2')"), css);
});

test('cssExpr: non-ok response leaves the url untouched', async () => {
  const fetchImpl = async () => ({ ok: false, status: 404 });
  const { css } = await run([sheet(SHEET_HREF, "@font-face { src: url('fonts/c.woff2'); }")], fetchImpl);
  assert.ok(css.includes("url('fonts/c.woff2')"), css);
});

test('cssExpr: empty body yields no data URI', async () => {
  const fetchImpl = async () => ({ ok: true, arrayBuffer: async () => new ArrayBuffer(0) });
  const { css } = await run([sheet(SHEET_HREF, "@font-face { src: url('fonts/c.woff2'); }")], fetchImpl);
  assert.ok(css.includes("url('fonts/c.woff2')"), css);
});

test('cssExpr: unreadable stylesheet is skipped, others still dumped', async () => {
  const bad = { href: 'https://other-origin/sheet.css', get cssRules() { throw new Error('cross-origin'); } };
  const { css } = await run([bad, sheet(SHEET_HREF, '.a { color: red; }')], async () => okFetch('x'));
  assert.ok(css.includes('.a { color: red; }'), css);
  assert.ok(!css.includes('other-origin'), css);
});

test('cssExpr: multiple sheets concatenated', async () => {
  const { css } = await run([sheet(SHEET_HREF, '.a { color: red; }'), sheet(SHEET_HREF + '2', '.b { color: blue; }')], async () => okFetch('x'));
  assert.ok(css.includes('.a { color: red; }'));
  assert.ok(css.includes('.b { color: blue; }'));
});
