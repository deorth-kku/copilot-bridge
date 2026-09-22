// Minimal DOM + browser-global stubs for unit-testing the project's
// injected JS under node. Implements only the surface the injected
// scripts use:
//   - class / tag / attribute selectors with descendant, '>' and ',' combinators
//   - getBoundingClientRect, offset/scroll/client geometry
//   - getComputedStyle backed by a per-element property map
//   - document capture-phase listeners (scroll) and window listeners
//   - a MutationObserver double with an instance registry
//   - a template/innerHTML mini-parser (for the mirror's incremental DOM patch)
//   - a WebSocket double with an instance registry
//
// NOT a general DOM: no layout, no CSS cascade, no events besides what
// the tests dispatch. Extend it deliberately when new JS needs more.
'use strict';

// --- text nodes (the mirror patcher diffs text nodes in place) ---

class Text {
  constructor(data) {
    this.nodeType = 3;
    this.nodeName = '#text';
    this.data = String(data);
    this.parentElement = null;
  }
  get parentNode() { return this.parentElement; }
  get length() { return this.data.length; }
  get textContent() { return this.data; }
}

// --- element attributes: array of {name, value} with named shortcuts,
//     so both attributes[i] (NamedNodeMap iteration) and attributes.class
//     (direct access) work ---

function makeAttrs(obj) {
  const arr = [];
  for (const [k, v] of Object.entries(obj || {})) {
    arr.push({ name: k, value: v });
    arr[k] = v;
  }
  return arr;
}

function attrOf(el, n) {
  const a = el.attributes[n];
  return a === undefined ? null : a;
}

// --- elements ---

class El {
  constructor(tag, opts = {}) {
    this.tagName = String(tag || 'div').toUpperCase();
    this.nodeType = 1;
    this.nodeName = this.tagName;
    this.children = [];
    this.attributes = makeAttrs(opts.attrs);
    this.parentElement = null;
    this.textContent = opts.text || '';
    this.innerText = opts.innerText !== undefined ? opts.innerText : this.textContent;
    this.outerHTML =
      opts.outerHTML ||
      `<${this.tagName.toLowerCase()}${attrOf(this, 'class') ? ` class="${attrOf(this, 'class')}"` : ''}>`;
    this.offsetWidth = opts.offsetWidth || 0;
    this.offsetHeight = opts.offsetHeight || 0;
    this.scrollLeft = opts.scrollLeft || 0;
    this.scrollTop = opts.scrollTop || 0;
    this.scrollHeight = opts.scrollHeight || 0;
    this.clientWidth = opts.clientWidth || 0;
    this.clientHeight = opts.clientHeight || 0;
    this.offsetTop = opts.offsetTop || 0;
    this.naturalWidth = opts.naturalWidth || 0;
    this.naturalHeight = opts.naturalHeight || 0;
    this.src = opts.src || '';
    // Form elements always have a value property in the real DOM.
    if (this.tagName === 'INPUT' || this.tagName === 'TEXTAREA' || this.tagName === 'SELECT') this.value = '';
    this.rect = Object.assign({ left: 0, top: 0, width: 0, height: 0 }, opts.rect || {});
    this.style = { setProperty(p, v) { this[p] = v; } };
    this._listeners = {};
  }
  get className() { return attrOf(this, 'class') || ''; }
  get childElementCount() { return this.children.filter(c => c.nodeType === 1).length; }
  get childNodes() { return this.children; }
  get parentNode() { return this.parentElement; }
  get firstElementChild() { return this.children.find(c => c.nodeType === 1) || null; }
  // HTMLSelectElement.length: assigning 0 clears the options.
  get length() { return this.children.length; }
  set length(n) { this.children.length = Math.max(0, n | 0); }
  get isConnected() {
    let e = this;
    while (e && e.parentElement) e = e.parentElement;
    const doc = globalThis.document;
    return !!doc && e === doc.documentElement;
  }
  getAttribute(n) {
    for (const a of this.attributes) if (a.name === n) return a.value;
    return null;
  }
  setAttribute(n, v) {
    for (const a of this.attributes) if (a.name === n) { a.value = v; this.attributes[n] = v; return; }
    this.attributes.push({ name: n, value: v });
    this.attributes[n] = v;
  }
  removeAttribute(n) {
    const i = this.attributes.findIndex(a => a.name === n);
    if (i >= 0) this.attributes.splice(i, 1);
    delete this.attributes[n];
  }
  appendChild(c) { c.parentElement = this; this.children.push(c); return c; }
  insertBefore(c, ref) {
    const i = ref === null ? this.children.length : this.children.indexOf(ref);
    if (i < 0) { c.parentElement = this; this.children.push(c); return c; }
    c.parentElement = this;
    this.children.splice(i, 0, c);
    return c;
  }
  removeChild(c) {
    const i = this.children.indexOf(c);
    if (i < 0) return null;
    this.children.splice(i, 1);
    c.parentElement = null;
    return c;
  }
  replaceWith(c) {
    const p = this.parentElement;
    if (!p) return;
    const i = p.children.indexOf(this);
    if (i < 0) return;
    c.parentElement = p;
    p.children[i] = c;
  }
  remove() { if (this.parentElement) this.parentElement.removeChild(this); }
  focus() { if (globalThis.document) globalThis.document.activeElement = this; }
  getBoundingClientRect() { return this.rect; }
  querySelector(sel) { return qsa(this, sel)[0] || null; }
  querySelectorAll(sel) { return qsa(this, sel); }
  // closest(sel): self first, then ancestors; comma = any branch.
  // Multi-part selectors match the candidate against the LAST part and walk
  // the ancestor chain for the earlier parts (descendant ' ' or child '>'),
  // as in the browser. ':scope' is the element closest() was called on.
  closest(sel) {
    const branches = sel.split(',').map(s => parseSel(s.trim()));
    for (let e = this; e; e = e.parentElement) {
      if (e.nodeType !== 1) continue;
      for (const parts of branches) {
        if (parts.length === 0) continue;
        const last = parts[parts.length - 1];
        if (last.simple !== null && !matchesSimple(e, last.simple)) continue;
        let ok = true;
        let a = e.parentElement;
        for (let i = parts.length - 2; i >= 0; i--) {
          const { comb, simple } = parts[i];
          if (comb === '@') {
            if (e !== this) { ok = false; break; }
            continue;
          }
          if (comb === '>') {
            if (!a || !matchesSimple(a, simple)) { ok = false; break; }
            a = a.parentElement;
          } else {
            let found = null;
            for (let x = a; x; x = x.parentElement) {
              if (matchesSimple(x, simple)) { found = x; break; }
            }
            if (!found) { ok = false; break; }
            a = found.parentElement;
          }
        }
        if (ok) return e;
      }
    }
    return null;
  }
  addEventListener(type, fn, capture) {
    (this._listeners[type] = this._listeners[type] || []).push({ fn, capture: !!capture });
  }
  removeEventListener(type, fn) {
    const arr = this._listeners[type];
    if (!arr) return;
    const i = arr.findIndex(l => l.fn === fn);
    if (i >= 0) arr.splice(i, 1);
  }
  getContext() { return { drawImage: () => {} }; }
  toDataURL() { return 'data:image/png;base64,FAKEPNG'; }
  dispatchEvent(ev) { return dispatch(globalThis.document, ev); }
}

// --- template: innerHTML mini-parser feeding content.firstElementChild ---

const VOID_TAGS = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr']);

// Deliberately small HTML parser: elements with (quoted or bare) attributes,
// text nodes, comments, void elements. Good enough for the fixture HTML the
// mirror tests feed it — not a general parser.
function parseHTML(html) {
  const root = [];
  const stack = [root];
  let i = 0;
  while (i < html.length) {
    const lt = html.indexOf('<', i);
    if (lt < 0) {
      const t = html.slice(i);
      if (t) root.push(new Text(t));
      break;
    }
    if (lt > i) {
      const t = html.slice(i, lt);
      if (t) {
        const tn = new Text(t);
        tn.parentElement = stack[stack.length - 1]._el || null;
        stack[stack.length - 1].push(tn);
      }
    }
    if (html.startsWith('<!--', lt)) {
      const end = html.indexOf('-->', lt);
      i = end < 0 ? html.length : end + 3;
      continue;
    }
    const gt = html.indexOf('>', lt);
    if (gt < 0) { i = html.length; break; }
    const inner = html.slice(lt + 1, gt);
    if (inner.startsWith('/')) {
      const tag = inner.slice(1).trim().toLowerCase();
      for (let s = stack.length - 1; s >= 1; s--) {
        if (stack[s]._tag === tag) { stack.length = s; break; }
      }
    } else {
      const m = inner.match(/^([a-zA-Z][a-zA-Z0-9]*)([\s\S]*)$/);
      if (m) {
        const tag = m[1].toLowerCase();
        const attrs = {};
        for (const am of m[2].matchAll(/([\w-]+)(?:=["']([^"']*)["'])?/g)) {
          if (am[1].toLowerCase() === tag) continue;
          attrs[am[1]] = am[2] !== undefined ? am[2] : '';
        }
        const el = new El(tag, { attrs });
        const parentArr = stack[stack.length - 1];
        el.parentElement = parentArr._el || null;
        parentArr.push(el);
        if (!VOID_TAGS.has(tag)) {
          // The element's own children array is the open level: parsed
          // children land directly in el.children.
          el.children._tag = tag;
          el.children._el = el;
          stack.push(el.children);
        }
      }
    }
    i = gt + 1;
  }
  return root;
}

class Template extends El {
  constructor() {
    super('template');
    this._content = [];
  }
  get content() {
    return {
      firstElementChild: this._content.find(c => c.nodeType === 1) || null,
      childNodes: this._content,
    };
  }
  set innerHTML(html) { this._content = parseHTML(String(html)); }
}

// --- selector engine (class / tag / [attr] with ' ', '>', ',' combinators) ---

function parseSimple(part) {
  const tag = (part.match(/^[a-zA-Z][a-zA-Z0-9]*/) || [null])[0];
  const classes = [...part.matchAll(/\.([\w-]+)/g)].map(m => m[1]);
  const attrs = [...part.matchAll(/\[([\w-]+)(?:=["']([^"']*)["'])?\]/g)].map(m => [m[1], m[2]]);
  return { tag, classes, attrs };
}

function matchesSimple(el, s) {
  if (el.nodeType !== 1) return false;
  if (s.tag && el.tagName !== s.tag.toUpperCase()) return false;
  const cls = (el.getAttribute('class') || '').split(/\s+/);
  for (const c of s.classes) if (!cls.includes(c)) return false;
  for (const [n, v] of s.attrs) {
    const av = el.getAttribute(n);
    if (av === null) return false;
    if (v !== undefined && av !== v) return false;
  }
  return true;
}

function parseSel(sel) {
  // Tokenize on whitespace / '>' OUTSIDE [attr="..."] brackets: attribute
  // values may contain spaces (e.g. [aria-label="Toggle Chat"]).
  const tokens = [];
  let cur = '';
  let depth = 0;
  for (const ch of sel.trim()) {
    if (ch === '[') depth++;
    else if (ch === ']') depth--;
    if (depth > 0) { cur += ch; continue; }
    if (ch === '>') {
      if (cur.trim()) tokens.push(cur.trim());
      cur = '';
      tokens.push('>');
      continue;
    }
    if (/\s/.test(ch)) {
      if (cur.trim()) tokens.push(cur.trim());
      cur = '';
      continue;
    }
    cur += ch;
  }
  if (cur.trim()) tokens.push(cur.trim());
  const parts = [];
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i] === ':scope') parts.push({ comb: '@', simple: null });
    else if (tokens[i] === '>') parts.push({ comb: '>', simple: parseSimple(tokens[++i]) });
    else parts.push({ comb: ' ', simple: parseSimple(tokens[i]) });
  }
  return parts;
}

function descendants(el) {
  const out = [];
  (function w(e) { for (const c of e.children) if (c.nodeType === 1) { out.push(c); w(c); } })(el);
  return out;
}

function qsaBranch(root, parts) {
  const res = [];
  (function rec(ancestor, idx) {
    const { comb, simple } = parts[idx];
    if (comb === '@') { // :scope — the root itself
      if (idx === parts.length - 1) res.push(ancestor);
      else rec(ancestor, idx + 1);
      return;
    }
    const cands = comb === '>' ? ancestor.children : descendants(ancestor);
    for (const c of cands) {
      if (matchesSimple(c, simple)) {
        if (idx === parts.length - 1) res.push(c);
        else rec(c, idx + 1);
      }
    }
  })(root, 0);
  return res;
}

function qsa(root, sel) {
  // Comma-separated branches: match any.
  const branches = sel.split(',').map(s => parseSel(s));
  const seen = new Set();
  const res = [];
  for (const parts of branches) {
    for (const c of qsaBranch(root, parts)) {
      if (!seen.has(c)) { seen.add(c); res.push(c); }
    }
  }
  return res;
}

// --- events: capture (doc -> target), then target, then bubble ---

class Event {
  constructor(type, target) { this.type = type; this.target = target || null; }
}

function dispatch(doc, ev) {
  const t = ev.target;
  const call = (holder, mode) => {
    const arr = holder._listeners ? holder._listeners[ev.type] : undefined;
    if (!arr) return;
    for (const l of arr.slice()) {
      if (mode === 'capture' && !l.capture) continue;
      if (mode === 'bubble' && l.capture) continue;
      l.fn(ev);
    }
  };
  if (t) {
    const chain = [];
    for (let e = t; e; e = e.parentElement) chain.unshift(e);
    // capture phase: document, then ancestors above the target
    call(doc, 'capture');
    for (let i = 0; i < chain.length - 1; i++) call(chain[i], 'capture');
    // target phase (all listeners), then bubble phase
    call(chain[chain.length - 1], 'target');
    for (let i = chain.length - 2; i >= 0; i--) call(chain[i], 'bubble');
  } else {
    call(doc, 'target');
  }
}

// --- Range (the mirror's cursor repositioning measures the caret's
//     character in the mirror layout). The stub approximates each character
//     as an equal slice of its container element's rect, UNLESS the
//     container (text node or parent element) carries a `charWidths` array
//     of per-character widths (e.g. CJK glyphs ~2x the Latin advance). ---

class Range {
  constructor() {
    this.startContainer = null;
    this.startOffset = 0;
    this.endContainer = null;
    this.endOffset = 0;
  }
  setStart(c, o) { this.startContainer = c; this.startOffset = o; }
  setEnd(c, o) { this.endContainer = c; this.endOffset = o; }
  selectNodeContents(n) {
    this.startContainer = n;
    this.startOffset = 0;
    this.endContainer = n;
    this.endOffset = n.nodeType === 3 ? n.data.length : (n.textContent || '').length;
  }
  getBoundingClientRect() {
    const c = this.startContainer;
    if (!c) return { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 };
    // A text-node container is measured through its parent element.
    const el = c.nodeType === 3 ? c.parentElement : c;
    if (!el) return { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 };
    const r = el.getBoundingClientRect();
    const len = (c.nodeType === 3 ? c.data : c.textContent || '').length;
    const cw = c.charWidths || el.charWidths || null;
    let left, width;
    if (cw && cw.length === len) {
      let a = 0, b = 0;
      for (let i = 0; i < this.startOffset; i++) a += cw[i];
      for (let i = 0; i < this.endOffset; i++) b += cw[i];
      left = r.left + a;
      width = b - a;
    } else {
      const w = len ? r.width / len : 0;
      left = r.left + this.startOffset * w;
      width = (this.endOffset - this.startOffset) * w;
    }
    const top = r.top;
    const height = r.height;
    return { left, top, width, height, right: left + width, bottom: top + height };
  }
}

// --- document / window / getComputedStyle / MutationObserver / WebSocket ---

function makeDocument(root, opts = {}) {
  const doc = {
    documentElement: root,
    body: opts.body || null,
    styleSheets: opts.styleSheets || [],
    activeElement: null,
    _listeners: {},
    get images() { return descendants(root).filter(e => e.tagName === 'IMG'); },
    querySelector: sel => qsa(root, sel)[0] || null,
    querySelectorAll: sel => qsa(root, sel),
    getElementById: id => qsa(root, '[id="' + id + '"]')[0] || null,
    addEventListener(type, fn, capture) {
      (doc._listeners[type] = doc._listeners[type] || []).push({ fn, capture: !!capture });
    },
    removeEventListener(type, fn) {
      const arr = doc._listeners[type];
      if (!arr) return;
      const i = arr.findIndex(l => l.fn === fn);
      if (i >= 0) arr.splice(i, 1);
    },
    createElement(tag) { return tag === 'template' ? new Template() : new El(tag); },
    createRange: () => new Range(),
    dispatchEvent(ev) { return dispatch(doc, ev); },
  };
  return doc;
}

function makeWindow(doc, opts = {}) {
  const w = {
    document: doc,
    location: {
      href: opts.href || 'vscode-file://vscode-app/app/index.html',
      search: opts.search || '',
      protocol: opts.protocol || 'http:',
      host: opts.host || '127.0.0.1:8123',
    },
    innerWidth: opts.innerWidth || 1280,
    innerHeight: opts.innerHeight || 800,
    _listeners: {},
  };
  w.addEventListener = (type, fn) => { (w._listeners[type] = w._listeners[type] || []).push(fn); };
  w.removeEventListener = (type, fn) => {
    const arr = w._listeners[type];
    if (!arr) return;
    const i = arr.indexOf(fn);
    if (i >= 0) arr.splice(i, 1);
  };
  w.dispatchEvent = ev => { for (const fn of (w._listeners[ev.type] || []).slice()) fn(ev); };
  return w;
}

// getComputedStyle(el) -> indexed property bag + getPropertyValue, with
// direct property access (cs.overflowY, cs.backgroundColor) as well — the
// injected JS uses both forms.
// mapOf(el) returns the computed property map for el (or null/{}).
function makeGetComputedStyle(mapOf) {
  return el => {
    const props = (mapOf && mapOf(el)) || {};
    const keys = Object.keys(props);
    const cs = {
      length: keys.length,
      getPropertyValue: k => (Object.prototype.hasOwnProperty.call(props, k) ? props[k] : ''),
    };
    // cs[i] must be the property NAME (real CSSStyleDeclaration behavior);
    // cs[name] gives direct value access; getPropertyValue(name) likewise.
    keys.forEach((k, i) => { cs[i] = k; cs[k] = props[k]; });
    return cs;
  };
}

class MutationObserver {
  constructor(cb) {
    this.cb = cb;
    this.disconnected = false;
    this.target = null;
    this.options = null;
    MutationObserver.instances.push(this);
  }
  observe(t, o) { this.target = t; this.options = o; }
  disconnect() { this.disconnected = true; }
  // Test hook: simulate a batch of mutations reaching the callback.
  trigger() { if (!this.disconnected) this.cb([], this); }
}
MutationObserver.instances = [];

// WebSocket double: the mirror client opens ws://<host>/ws on load.
// Tests capture instances and drive the lifecycle via fire*.
class WebSocketStub {
  constructor(url) {
    this.url = url;
    this.readyState = 1; // OPEN
    this.sent = [];
    WebSocketStub.instances.push(this);
  }
  send(data) { this.sent.push(data); }
  close() { this.readyState = 3; }
  fireOpen() { if (this.onopen) this.onopen(); }
  fireClose() { if (this.onclose) this.onclose(); }
  fireMessage(data) { if (this.onmessage) this.onmessage({ data }); }
}
WebSocketStub.instances = [];

let savedFetch;
let savedWebSocket;
function installGlobals(doc, win, opts = {}) {
  globalThis.document = doc;
  globalThis.window = win;
  globalThis.location = win.location;
  globalThis.getComputedStyle = opts.getComputedStyle || makeGetComputedStyle(null);
  globalThis.MutationObserver = MutationObserver;
  if (savedWebSocket === undefined) savedWebSocket = globalThis.WebSocket;
  globalThis.WebSocket = opts.WebSocket || WebSocketStub;
  if (opts.fetch) {
    if (savedFetch === undefined) savedFetch = globalThis.fetch;
    globalThis.fetch = opts.fetch;
  }
}
function uninstallGlobals() {
  delete globalThis.document;
  delete globalThis.window;
  delete globalThis.location;
  delete globalThis.getComputedStyle;
  delete globalThis.MutationObserver;
  if (savedWebSocket !== undefined) { globalThis.WebSocket = savedWebSocket; savedWebSocket = undefined; }
  if (savedFetch !== undefined) { globalThis.fetch = savedFetch; savedFetch = undefined; }
}

module.exports = {
  El,
  Text,
  Template,
  parseHTML,
  Event,
  Range,
  qsa,
  descendants,
  makeDocument,
  makeWindow,
  makeGetComputedStyle,
  MutationObserver,
  WebSocketStub,
  installGlobals,
  uninstallGlobals,
};
