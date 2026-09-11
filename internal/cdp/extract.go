package cdp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
)

// PaneRect is the pane root's bounding box in the VS Code viewport
// (CSS pixels). The mirror uses it to map its own coordinates back onto
// the live page when forwarding input.
type PaneRect struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// PaneScroll is the scroll state of the pane's nearest scrollable
// ancestor, used to keep the mirror's scroll position in sync.
type PaneScroll struct {
	Left float64 `json:"left"`
	Top  float64 `json:"top"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

// HTMLState is the result of ExtractHTML.
type HTMLState struct {
	Err       string     `json:"err"`
	HTML      string     `json:"html"`
	RootStyle string     `json:"rootStyle"`
	ThemeVars string     `json:"themeVars"`
	Rect      PaneRect   `json:"rect"`
	Scroll    PaneScroll `json:"scroll"`
	CSSFP     string     `json:"cssFP"`
}

// CSSState is the result of ExtractCSS.
type CSSState struct {
	CSS string `json:"css"`
}

// FingerprintState is the result of Fingerprint: a cheap probe run every
// poll that avoids the costly outerHTML dump until something actually
// changed.
type FingerprintState struct {
	Err    string     `json:"err"`
	FP     string     `json:"fp"`
	CSSFP  string     `json:"cssFP"`
	Rect   PaneRect   `json:"rect"`
	Scroll PaneScroll `json:"scroll"`
}

// htmlExpr extracts the pane subtree's outerHTML plus the layout metadata
// the mirror needs (bounding box, scroll, theme inline styles, and a cheap
// CSS fingerprint). It is an arrow function (NOT invoked); ExtractHTML
// passes the candidate selectors as its single argument.
const htmlExpr = `(selectors) => {
  let el = null;
  for (const sel of selectors) {
    try { el = document.querySelector(sel); } catch (e) {}
    if (el) break;
  }
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
` + scrollContainerJS + `
  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
  const rootStyle = ((document.documentElement.getAttribute('style') || '') + ';' +
    (document.body ? (document.body.getAttribute('style') || '') : '')).trim();
  // Capture every CSS custom property computed at the pane root. VS Code
  // defines its palette as hundreds of --vscode-* variables on ancestor
  // selectors (e.g. .monaco-workbench) that are absent from the extracted
  // subtree; carrying the computed values over is what makes themed colors
  // render correctly in the mirror.
  let themeVars = '';
  try {
    const cs = getComputedStyle(el);
    for (let i = 0; i < cs.length; i++) {
      const p = cs[i];
      if (p.indexOf('--') !== 0) continue;
      const v = cs.getPropertyValue(p).trim();
      if (v) themeVars += p + ': ' + v + ';';
    }
    // Also carry over the inherited text-metric properties computed at the
    // pane root. VS Code sets font-family / font-size / line-height on
    // ancestor selectors (e.g. .monaco-workbench) that are absent from the
    // extracted subtree; without them the mirror falls back to the browser
    // default font and line-height, which vertically compresses the content
    // and offsets every forwarded click. These are inherited properties, so
    // applying them to the mirror root reproduces the live text environment.
    const textProps = ['font-family', 'font-size', 'line-height', 'font-weight', 'font-style', 'letter-spacing'];
    for (const p of textProps) {
      const v = cs.getPropertyValue(p).trim();
      if (v) themeVars += p + ': ' + v + ';';
    }
  } catch (e) {}
  return {
    html: el.outerHTML,
    rootStyle: rootStyle,
    themeVars: themeVars,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, w: sc.clientWidth, h: sc.clientHeight },
    cssFP: cssFP,
  };
}`

// scrollContainerJS is spliced into both htmlExpr and fpExpr (they are
// evaluated as separate programs, so the helper must be inlined). It locates
// the element that actually scrolls the active view: monaco-lists scroll
// their .monaco-scrollable-element DESCENDANT (overflow:hidden, scrolled
// programmatically via scrollTop), so a "walk up for overflow:auto/scroll"
// heuristic never finds it. The pane root contains TWO such lists — the chat
// message list and the agent-sessions list (session-selection view) — and
// exactly one of them is expanded at a time, so the one with the most
// scrollable room (scrollHeight - clientHeight) is the active one. The
// ancestor walk remains as a fallback for other pane shapes.
const scrollContainerJS = `
  let sc = null;
  try {
    const cands = el.querySelectorAll('.monaco-scrollable-element');
    let best = 0;
    for (const c of cands) {
      const room = (c.scrollHeight || 0) - (c.clientHeight || 0);
      if (room > best) { best = room; sc = c; }
    }
  } catch (e) {}
  if (!sc) {
    sc = el;
    while (sc && sc !== document.documentElement) {
      const ov = getComputedStyle(sc).overflowY;
      if ((ov === 'auto' || ov === 'scroll') && sc.scrollHeight > sc.clientHeight) break;
      sc = sc.parentElement;
    }
  }
`

// fpExpr is the cheap per-poll probe: it returns a content fingerprint, a
// CSS fingerprint, and the layout metadata (bounding box + scroll) without
// dumping the pane's outerHTML.
const fpExpr = `(selectors) => {
  let el = null;
  for (const sel of selectors) {
    try { el = document.querySelector(sel); } catch (e) {}
    if (el) break;
  }
  if (!el) return { err: 'pane not found' };
  const r = el.getBoundingClientRect();
` + scrollContainerJS + `
  let cssFP = '';
  try {
    cssFP = document.styleSheets.length + ':' +
      (document.documentElement.getAttribute('style') || '').length + ':' +
      (document.body ? (document.body.getAttribute('style') || '').length : 0);
  } catch (e) {}
  const model = (el.querySelector('.model-picker-name') || { textContent: '' }).textContent;
  // Include a cheap theme indicator so a theme change (which does not change
  // text content) still triggers a full re-extract of themeVars.
  let themeInd = '';
  try { themeInd = getComputedStyle(el).getPropertyValue('--vscode-foreground').trim(); } catch (e) {}
  const fp = el.innerText.length + ':' + el.childElementCount + ':' + model.length + ':' + themeInd;
  return {
    fp: fp,
    cssFP: cssFP,
    rect: { left: r.left, top: r.top, width: r.width, height: r.height },
    scroll: { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, w: sc.clientWidth, h: sc.clientHeight },
  };
}`

// cssExpr dumps every accessible stylesheet and rewrites each @font-face
// font URL into an inlined base64 data URI, so the returned CSS is fully
// self-contained (no external font requests). It is async because it fetches
// the font bytes in the page context. Each font URL is resolved against its
// own stylesheet's base (sheet.href) before fetching, which is required for
// VS Code's vscode-file:// resources.
const cssExpr = `async () => {
  const toB64 = (buf) => {
    let s = '';
    const bytes = new Uint8Array(buf);
    for (let i = 0; i < bytes.length; i += 0x8000) {
      s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    }
    return btoa(s);
  };
  const mime = (u) => {
    const m = u.toLowerCase();
    if (m.includes('.woff2')) return 'font/woff2';
    if (m.includes('.woff')) return 'font/woff';
    if (m.includes('.ttf')) return 'font/ttf';
    if (m.includes('.otf')) return 'font/otf';
    if (m.includes('.eot')) return 'application/vnd.ms-fontobject';
    return 'application/octet-stream';
  };
  let css = '';
  for (const sheet of document.styleSheets) {
    let rules;
    try { rules = Array.from(sheet.cssRules); } catch (e) { continue; }
    let text = rules.map(rule => rule.cssText).join('\n');
    const base = sheet.href || location.href;
    for (const m of text.matchAll(/url\(([^)]+)\)/g)) {
      const raw = m[1].replace(/^["']|["']$/g, '').split(/[?#]/)[0];
      if (!/\.(woff2?|ttf|otf|eot)([?#]|$)/i.test(raw)) continue;
      let abs;
      try { abs = new URL(raw, base).href; } catch (e) { continue; }
      let b64;
      try {
        const resp = await fetch(abs);
        if (!resp.ok) continue;
        b64 = toB64(await resp.arrayBuffer());
      } catch (e) { continue; }
      if (!b64) continue;
      const dataURI = 'data:' + mime(abs) + ';base64,' + b64;
      text = text.split(m[1]).join(m[1].replace(raw, dataURI));
    }
    css += text + '\n';
  }
  return { css: css };
}`

// callExpr wraps an arrow-function expression in an IIFE and evaluates it,
// passing argsJSON (a JS array literal, or "" for none) as the argument.
// Runtime.evaluate does NOT invoke a bare function expression with "args",
// so the IIFE form is required.
func callExpr(s *Session, expr, argsJSON string) (jsontext.Value, error) {
	full := "(" + expr + ")(" + argsJSON + ")"
	return s.Call("Runtime.evaluate", map[string]any{
		"expression":    full,
		"awaitPromise":  true,
		"returnByValue": true,
	})
}

// selectorsJSON marshals the candidate selectors as a JS array literal.
func selectorsJSON(selectors []string) string {
	b, _ := json.Marshal(selectors)
	return string(b)
}

// ExtractHTML returns the pane subtree's outerHTML plus layout metadata.
// selectors are tried in order; the first that matches wins.
func ExtractHTML(s *Session, selectors []string) (*HTMLState, error) {
	raw, err := callExpr(s, htmlExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	return decodeEval[HTMLState](raw)
}

// Fingerprint returns a cheap probe of the pane (content + CSS fingerprint
// plus layout metadata) without dumping the outerHTML.
func Fingerprint(s *Session, selectors []string) (*FingerprintState, error) {
	raw, err := callExpr(s, fpExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	return decodeEval[FingerprintState](raw)
}

// ExtractCSS returns the full workbench CSS with @font-face fonts inlined
// as base64 data URIs (self-contained).
func ExtractCSS(s *Session) (*CSSState, error) {
	raw, err := callExpr(s, cssExpr, "")
	if err != nil {
		return nil, err
	}
	return decodeEval[CSSState](raw)
}

// clickPointExpr resolves a DOM path (child indices from the pane root) plus a
// relative (0..1) position within the target element to an absolute live-page
// coordinate. Mapping by element identity (rather than by absolute pane offset)
// keeps forwarded clicks accurate even when the mirror's rendering is not
// pixel-identical to the live layout. A DOM path is also robust to sibling
// additions elsewhere in the tree (e.g. the conversation growing), which would
// shift a flat descendant index.
// NOTE: takes a single args array (not 4 params) so it composes with callExpr,
// which wraps the expression in an IIFE and passes one argument.
const clickPointExpr = `(args) => {
  const sels = args[0], path = args[1], rx = args[2], ry = args[3];
  let root = null;
  for (const sel of sels) { try { root = document.querySelector(sel); } catch (e) {} if (root) break; }
  if (!root) return null;
  let el = root;
  if (Array.isArray(path)) {
    for (const i of path) {
      if (!el.children[i]) { el = null; break; }
      el = el.children[i];
    }
  }
  if (!el) el = root;
  const r = el.getBoundingClientRect();
  return { x: r.left + rx * r.width, y: r.top + ry * r.height };
}`

// EvalClickPoint resolves a DOM path (child indices from the pane root) plus a
// relative (0..1) position within the target element to an absolute live-page
// coordinate. An empty path means the root itself; navigation failure falls
// back to the root.
func EvalClickPoint(s *Session, selectors []string, path []int, relX, relY float64) (float64, float64, bool) {
	args, _ := json.Marshal([]any{selectors, path, relX, relY})
	raw, err := callExpr(s, clickPointExpr, string(args))
	if err != nil {
		return 0, 0, false
	}
	var resp struct {
		Result struct {
			Value struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
		Exception struct {
			Description string `json:"description"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, 0, false
	}
	if resp.Exception.Description != "" {
		return 0, 0, false
	}
	return resp.Result.Value.X, resp.Result.Value.Y, true
}

// decodeEval unwraps a Runtime.evaluate response and decodes result.value
// into T. Page exceptions are surfaced as errors.
func decodeEval[T any](raw jsontext.Value) (*T, error) {
	var resp struct {
		Result struct {
			Value jsontext.Value `json:"value"`
		} `json:"result"`
		Exception struct {
			Description string `json:"description"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Exception.Description != "" {
		return nil, errors.New("page exception: " + resp.Exception.Description)
	}
	var out T
	if err := json.Unmarshal(resp.Result.Value, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
