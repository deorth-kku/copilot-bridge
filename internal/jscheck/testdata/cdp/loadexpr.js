// loadexpr.js: loads the committed CDP probe sources (internal/cdp/js).
// The probe expressions reference the page-global probe library
// (window.__copilotBridgeProbes, probe_lib.js) the same way the live page
// does: installLib() evaluates the library against the current stub
// window, mirroring cdp.Session, which evaluates it before the probes.
// Keeping the load here (instead of in a generated fixture) means the
// suites always run against the live committed files.
'use strict';
const fs = require('node:fs');
const path = require('node:path');

const JS_DIR = path.join(__dirname, '..', '..', '..', 'cdp', 'js');
const read = (p) => fs.readFileSync(path.join(JS_DIR, p), 'utf8');

// installLib evaluates the probe library in the current global context
// (window = the current domstub window). A fresh installGlobals yields a
// window without the library, so the next probe call reinstalls it.
function installLib() {
  new Function(read('probe_lib.js'))();
}

function libReady() {
  return typeof window !== 'undefined' && !!window && !!window.__copilotBridgeProbes;
}

// loadExpr loads one expression source (a bare arrow-function expression)
// and evaluates it. The returned wrapper installs the probe library on
// first call (the expressions reference window.__copilotBridgeProbes at
// call time).
function loadExpr(name) {
  const f = new Function('return ' + read(name))();
  return (...args) => {
    if (!libReady()) installLib();
    return f(...args);
  };
}

// loadIIFE loads one injected-IIFE source. __BINDING__ is spliced with the
// page global the IIFE pushes through, __DEBOUNCE_MS__ with the default
// debounce (cdp.defaultDebounceMs). The caller must installLib() before
// evaluating the IIFE (it references the library at eval time).
function loadIIFE(name, binding) {
  return read(name).split('__BINDING__').join(binding).split('__DEBOUNCE_MS__').join('50');
}

module.exports = { installLib, loadExpr, loadIIFE };
