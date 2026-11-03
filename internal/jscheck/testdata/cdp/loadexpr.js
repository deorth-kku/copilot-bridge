// loadexpr.js: loads the committed CDP probe sources (internal/cdp/js)
// and assembles the templated expressions the same way the Go package
// does — the js/lib fragments are spliced into the __PLACEHOLDER__
// markers. The standalone expressions load directly. Keeping the assembly
// here (instead of in a generated fixture) means the suites always run
// against the live committed files.
'use strict';
const fs = require('node:fs');
const path = require('node:path');

const JS_DIR = path.join(__dirname, '..', '..', '..', 'cdp', 'js');
const read = (p) => fs.readFileSync(path.join(JS_DIR, p), 'utf8');

// The shared fragments, keyed by their placeholder. __BINDING__ and
// __DEBOUNCE_MS__ are the inject-IIFE parameters (the binding global and
// the default debounce, cdp.defaultDebounceMs).
const LIBS = {
  __FIND_ROOT__: () => read('lib/find_root.js'),
  __SCROLL_STATE__: () => read('lib/scroll_state.js'),
  __INPUT_EDITOR__: () => read('lib/input_editor.js'),
  __CSS_FP__: () => read('lib/css_fp.js'),
  __SCROLL_OBJ__: () => read('lib/scroll_obj.js'),
  __EXTRACT_FN__: () => read('extract_fn.js'),
  __BINDING__: (b) => b,
  __DEBOUNCE_MS__: () => '50',
};

function resolve(name, binding) {
  let src = read(name);
  for (const [ph, get] of Object.entries(LIBS)) {
    src = src.split(ph).join(ph === '__BINDING__' ? binding : get());
  }
  return src;
}

// loadExpr resolves one expression source (a bare arrow-function
// expression) and evaluates it.
function loadExpr(name) {
  return new Function('return ' + resolve(name))();
}

// loadIIFE resolves one injected-IIFE source (binding = the page global
// the IIFE pushes through).
function loadIIFE(name, binding) {
  return resolve(name, binding);
}

module.exports = { loadExpr, loadIIFE };
