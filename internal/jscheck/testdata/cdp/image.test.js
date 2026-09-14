// Unit tests for imageFetchExpr / imageCanvasExpr (internal/cdp/image.go):
// the two-stage in-page image fetch (blob fetch, then canvas fallback).
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const imageFetch = require('../modules/image_fetch.js');
const imageCanvas = require('../modules/image_canvas.js');

function withGlobals(doc, fetchImpl) {
  installGlobals(doc, makeWindow(doc), fetchImpl ? { fetch: fetchImpl } : {});
}

test('imageFetch: returns base64 bytes and content type', async () => {
  const bytes = new TextEncoder().encode('PNGDATA');
  const fetchImpl = async u => ({
    ok: true,
    status: 200,
    arrayBuffer: async () => bytes.buffer,
    headers: { get: k => (k === 'content-type' ? 'image/png' : null) },
  });
  const doc = makeDocument(new El('div'));
  withGlobals(doc, fetchImpl);
  try {
    const r = await imageFetch('blob:vscode-file://x');
    assert.equal(r.b64, Buffer.from('PNGDATA').toString('base64'));
    assert.equal(r.type, 'image/png');
  } finally { uninstallGlobals(); }
});

test('imageFetch: http error surfaces the status', async () => {
  const doc = makeDocument(new El('div'));
  withGlobals(doc, async () => ({ ok: false, status: 404 }));
  try {
    assert.equal((await imageFetch('u')).err, 'http 404');
  } finally { uninstallGlobals(); }
});

test('imageFetch: empty body (revoked blob) surfaces its own error', async () => {
  const doc = makeDocument(new El('div'));
  withGlobals(doc, async () => ({
    ok: true,
    status: 200,
    arrayBuffer: async () => new ArrayBuffer(0),
    headers: { get: () => null },
  }));
  try {
    assert.equal((await imageFetch('blob:vscode-file://revoked')).err, 'empty body');
  } finally { uninstallGlobals(); }
});

test('imageCanvas: re-encodes the displayed bitmap of the matching img', () => {
  const img = new El('img', { src: 'blob:vscode-file://x', naturalWidth: 8, naturalHeight: 6 });
  const root = new El('div');
  root.appendChild(img);
  const doc = makeDocument(root);
  withGlobals(doc);
  try {
    const r = imageCanvas('blob:vscode-file://x');
    assert.equal(r.b64, 'FAKEPNG');
    assert.equal(r.type, 'image/png');
  } finally { uninstallGlobals(); }
});

test('imageCanvas: ignores imgs without a decoded bitmap', () => {
  const img = new El('img', { src: 'blob:vscode-file://x', naturalWidth: 0 });
  const root = new El('div');
  root.appendChild(img);
  const doc = makeDocument(root);
  withGlobals(doc);
  try {
    assert.equal(imageCanvas('blob:vscode-file://x').err, 'no decoded img for url');
  } finally { uninstallGlobals(); }
});

test('imageCanvas: no img for the url -> err', () => {
  const doc = makeDocument(new El('div'));
  withGlobals(doc);
  try {
    assert.equal(imageCanvas('blob:vscode-file://y').err, 'no decoded img for url');
  } finally { uninstallGlobals(); }
});
