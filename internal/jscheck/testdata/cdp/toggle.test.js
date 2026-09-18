// Unit tests for toggleChatExpr (internal/cdp/toggle.go): title-bar
// "Toggle Chat" button resolution to a click point.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const toggleChat = require('../modules/toggle_chat.js');

function build() {
  const root = new El('div');
  const titlebar = new El('div', { attrs: { class: 'titlebar-container' } });
  const container = new El('div', { attrs: { class: 'action-container' } });
  const btn = new El('a', {
    attrs: { class: 'action-label codicon codicon-chat-sparkle', 'aria-label': 'Toggle Chat' },
    rect: { left: 100, top: 7, width: 22, height: 22 },
  });
  container.appendChild(btn);
  titlebar.appendChild(container);
  root.appendChild(titlebar);
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  return { root, btn };
}

test('toggleChat: resolves the title-bar button to its center', () => {
  build();
  try {
    assert.deepEqual(toggleChat(), { ok: true, x: 111, y: 18 });
  } finally { uninstallGlobals(); }
});

test('toggleChat: absent button -> null', () => {
  const root = new El('div');
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    assert.equal(toggleChat(), null);
  } finally { uninstallGlobals(); }
});

test('toggleChat: substring aria labels do not match (exact match only)', () => {
  const root = new El('div');
  // A chat-message a11y label that merely CONTAINS "Toggle Chat" (the
  // conversation text can say anything) must not resolve to a click point.
  root.appendChild(new El('div', { attrs: { 'aria-label': 'Use Toggle Chat to open the pane' } }));
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    assert.equal(toggleChat(), null);
  } finally { uninstallGlobals(); }
});

test('toggleChat: hidden (zero-size) button -> null', () => {
  const root = new El('div');
  const titlebar = new El('div', { attrs: { class: 'titlebar-container' } });
  const btn = new El('a', { attrs: { 'aria-label': 'Toggle Chat' }, rect: { left: 0, top: 0, width: 0, height: 0 } });
  titlebar.appendChild(btn);
  root.appendChild(titlebar);
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
  try {
    assert.equal(toggleChat(), null);
  } finally { uninstallGlobals(); }
});
