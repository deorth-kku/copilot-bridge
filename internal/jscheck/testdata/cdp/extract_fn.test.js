// Unit tests for extractFn (internal/cdp/inject.go): reads the chat input
// state (input text, model, effort, mode, visibility) from the live DOM.
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { El, makeDocument, makeWindow, installGlobals, uninstallGlobals } = require('../domstub.js');
const extract = require('../modules/extract_fn.js');

function buildState({ input, model, modelAttr, effort, effortAttr, mode, inputEditor = true } = {}) {
  const root = new El('div', { attrs: { class: 'monaco-workbench' }, offsetWidth: 100, offsetHeight: 100 });
  if (inputEditor) {
    const editor = new El('div', {
      attrs: { class: 'monaco-editor' },
      innerText: input || '',
      offsetWidth: 10,
      offsetHeight: 10,
    });
    const inputInner = new El('div', { attrs: { class: 'interactive-input-editor' } });
    inputInner.appendChild(editor);
    const inputContainer = new El('div', { attrs: { class: 'chat-input-container' } });
    inputContainer.appendChild(inputInner);
    root.appendChild(inputContainer);
  }
  if (model !== undefined || modelAttr !== undefined) {
    const m = new El('a', { attrs: { class: 'model-picker-name' }, text: model || '', offsetWidth: 5, offsetHeight: 5 });
    if (modelAttr !== undefined) m.setAttribute('aria-label', modelAttr);
    root.appendChild(m);
  }
  if (effort !== undefined || effortAttr !== undefined) {
    const e = new El('div', { attrs: { class: 'model-picker-config' }, text: effort || '' });
    if (effortAttr !== undefined) e.setAttribute('aria-label', effortAttr);
    root.appendChild(e);
  }
  if (mode) {
    const item = new El('div', { attrs: { class: 'chat-input-picker-item' } });
    item.appendChild(new El('span', { attrs: { class: 'chat-input-picker-label' }, text: mode }));
    root.appendChild(item);
  }
  const doc = makeDocument(root);
  installGlobals(doc, makeWindow(doc));
}

test('extract: full state via aria-labels', () => {
  buildState({ input: 'hello\n', modelAttr: 'Models, Qwen3.8 27B', effortAttr: 'Reasoning Effort: high', mode: ' Agent ' });
  try {
    const s = extract();
    assert.equal(s.input, 'hello');
    assert.equal(s.model, 'Qwen3.8 27B');
    assert.equal(s.effort, 'high');
    assert.equal(s.mode, 'Agent');
    assert.equal(s.inputVisible, true);
    assert.equal(s.modelVisible, true);
  } finally { uninstallGlobals(); }
});

test('extract: model falls back to textContent when aria-label is absent (regression)', () => {
  buildState({ model: 'Claude Opus 4.8' });
  try {
    assert.equal(extract().model, 'Claude Opus 4.8');
  } finally { uninstallGlobals(); }
});

test('extract: empty-string aria-label also falls back to textContent', () => {
  buildState({ model: 'GPT-5.6', modelAttr: '' });
  try {
    assert.equal(extract().model, 'GPT-5.6');
  } finally { uninstallGlobals(); }
});

test('extract: effort prefix stripped case-insensitively from textContent', () => {
  buildState({ effort: 'reasoning effort: medium' });
  try {
    assert.equal(extract().effort, 'medium');
  } finally { uninstallGlobals(); }
});

test('extract: missing elements yield nulls and invisibility', () => {
  buildState({ inputEditor: false });
  try {
    const s = extract();
    assert.equal(s.input, null);
    assert.equal(s.model, null);
    assert.equal(s.effort, null);
    assert.equal(s.mode, null);
    assert.equal(s.inputVisible, false);
    assert.equal(s.modelVisible, false);
  } finally { uninstallGlobals(); }
});

test('extract: trailing newlines stripped from input', () => {
  buildState({ input: 'line1\nline2\n\n' });
  try {
    assert.equal(extract().input, 'line1\nline2');
  } finally { uninstallGlobals(); }
});
