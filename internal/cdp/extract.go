package cdp

import (
	_ "embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"
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
	Left    float64 `json:"left"`
	Top     float64 `json:"top"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	ScrollH float64 `json:"scrollH"`
	// Offset is the .monaco-list-rows container's offsetTop within the
	// scroll element. Bottom-anchored monaco-lists keep scrollTop at 0 and
	// encode the scroll position in this negative offset instead, so the
	// mirror needs it to map the live scroll state onto its own (static,
	// top-anchored) layout.
	Offset float64 `json:"offset"`
	// AnchorKind reports how the live list encodes its scroll position:
	// "bottom" for bottom-anchored monaco-lists (the chat message list,
	// .interactive-list — scrollTop stays 0, position in the rows' negative
	// offsetTop) and "top" for ordinary top-anchored lists (the
	// agent-sessions picker — position in scrollTop, offsetTop stays 0).
	// The mirror uses it to choose which end of the live viewport to keep
	// when the live viewport's mirror height exceeds the mirror scroller's
	// own height. The end is a STATIC property of the list: switching ends
	// between state messages would teleport the mirror's viewport.
	AnchorKind string `json:"anchorKind,omitempty"`
}

// NestedScroll is the scroll state of a nested scroll container inside the
// pane that the main pane scroller does not cover. Currently: the
// reasoning-trace list of a .chat-thinking-box (.chat-used-context-list,
// ~200px max-height while streaming). While the trace streams, VS Code pins
// its scrollTop to the bottom (DomScrollableElement applies
// setScrollPosition to the wrapped list element), but the extracted HTML
// carries no scrollTop, so the mirror maps the live ratio
// (Top / (ScrollH - ClientH)) onto its own copy of the list.
type NestedScroll struct {
	// Path is the DOM path from the pane root to the list element.
	Path    []int   `json:"path"`
	Top     float64 `json:"top"`
	ScrollH float64 `json:"scrollH"`
	ClientH float64 `json:"clientH"`
}

// layoutState is the pane layout metadata shared by the cheap fingerprint
// probe (FingerprintState) and the full HTML extract (HTMLState).
type layoutState struct {
	Rect PaneRect `json:"rect"`
	// Scroll is the scroll state of the pane's nearest scrollable ancestor
	// (see PaneScroll).
	Scroll PaneScroll `json:"scroll"`
	// ScrollPath identifies the measured scroll container as a DOM path from
	// the pane root (no omitempty: an empty path means "the root itself",
	// null means "no scroller").
	ScrollPath []int `json:"scrollPath"`
	// ScrollRows is each currently rendered row's [offsetTop, offsetHeight]
	// pair in full-content coordinates (see scrollStateJS). The mirror uses
	// it to align its viewport with the live viewport inside the rendered
	// row window, because the mirror's content is only that window.
	ScrollRows []float64 `json:"scrollRows,omitempty"`
	// NestedScrolls carries the scroll state of nested scroll containers the
	// main scroller does not cover (see NestedScroll).
	NestedScrolls []NestedScroll `json:"nestedScrolls,omitempty"`
}

// HTMLState is the result of ExtractHTML.
type HTMLState struct {
	Err       string `json:"err"`
	HTML      string `json:"html"`
	RootStyle string `json:"rootStyle"`
	ThemeVars string `json:"themeVars"`
	// InputFocused reports whether the chat input's Monaco editor holds the
	// page focus. The mirror uses it to decide whether to show (and blink)
	// the input cursor: live keeps the cursor hidden when the input is not
	// focused.
	InputFocused bool `json:"inputFocused"`
	// ThemeBg is the pane root's effective background color (the nearest
	// non-transparent ancestor's computed value); the mirror pins it on its
	// own root and page body.
	ThemeBg string `json:"themeBg"`
	layoutState
	CSSFP string `json:"cssFP"`
	// Popups are the currently visible context views (empty/nil when none
	// is open). VS Code can show several at once (e.g. the model picker
	// dropdown plus an auto-shown hover tooltip), so the mirror renders
	// each as a separate overlay.
	Popups []PopupState `json:"popups"`
	// ModalEditor is the currently visible modal editor (the image carousel
	// editor, a Markdown preview, ...), rendered outside the pane root in a
	// .part.editor.modal-editor-part overlay. Nil when no modal editor is
	// open.
	ModalEditor *ModalEditorState `json:"modalEditor"`
}

// CSSState is the result of ExtractCSS.
type CSSState struct {
	CSS string `json:"css"`
}

// PopupState is one visible context view (popup menu, dropdown, tooltip)
// captured alongside the pane. VS Code renders context views OUTSIDE the
// pane root — in .context-view containers that are children of the
// workbench root — so the pane subtree alone never contains them. Left/Top
// are the container's offset relative to the pane root's bounding box, so
// the mirror can place it in the same pane-relative spot.
type PopupState struct {
	// ID is the container's index in the FULL (including hidden)
	// .context-view list. The containers are persistent DOM nodes, so the
	// id is stable across open/close cycles and does not shift when an
	// earlier popup closes — the mirror matches its copies by this id.
	ID     int     `json:"id"`
	HTML   string  `json:"html,omitempty"`
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	// Anchor is the live bounding box of the control that opened the popup
	// (the last clicked button), in the same pane-relative coordinates as
	// Left/Top. The mirror's responsive layout does not preserve live
	// pane-relative offsets, so it positions the popup by applying the
	// (popup - anchor) offset to its own copy of that control. Nil when the
	// anchor is unknown (e.g. the popup was opened via the keyboard).
	Anchor *PaneRect `json:"anchor,omitempty"`
}

// ModalEditorState is the visible modal editor (the image carousel editor, a
// Markdown preview, ...) captured alongside the pane. VS Code renders modal
// editors OUTSIDE the pane root — in a .part.editor.modal-editor-part
// floating overlay that blocks interaction with the rest of the workbench —
// so the pane subtree alone never contains it. The mirror renders it as a
// full-viewport overlay and forwards clicks (notably the close button) to
// live. Left/Top/Width/Height are the overlay's bounding box in the live
// viewport.
type ModalEditorState struct {
	// HTML is the modal editor's subtree. omitempty: a position-only follow
	// (the box moved but its content did not) omits it, so the browser keeps
	// its current copy and only re-applies the size (an empty string would
	// parse to no element and crash the in-place patch).
	HTML   string  `json:"html,omitempty"`
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// FingerprintState is the result of Fingerprint: a cheap probe run every
// poll that avoids the costly outerHTML dump until something actually
// changed.
type FingerprintState struct {
	Err   string `json:"err"`
	FP    string `json:"fp"`
	CSSFP string `json:"cssFP"`
	// InputFocused is part of the fp (focus changes must trigger a
	// re-extract) and is reported here so the cheap probe can carry it to
	// the state message even when only focus changed.
	InputFocused bool `json:"inputFocused"`
	// CursorChar is the chat-input cursor's character index (count of
	// characters before the caret) in the live layout; -1 when the input
	// has no cursor. The mirror re-wraps the text at its own width, so it
	// re-seats the cursor by this index instead of the live pixels.
	CursorChar int `json:"cursorChar"`
	// InputBreaks is one bool per boundary between the chat input's
	// consecutive live view lines: true = hard newline (a text-line end),
	// false = soft wrap. The mirror re-groups the view lines across the
	// soft boundaries so the text re-flows continuously at the mirror
	// width. Nil when the input has fewer than two view lines.
	InputBreaks []bool `json:"inputBreaks,omitempty"`
	layoutState
}

// PageHealth is one live-page size/memory sample.
type PageHealth struct {
	Nodes     int  `json:"nodes"`
	PaneNodes *int `json:"paneNodes"`
	Sheets    int  `json:"sheets"`
	Heap      *struct {
		Used  float64 `json:"used"`
		Total float64 `json:"total"`
	} `json:"heap"`
}

// Shared probe fragments (js/lib/). The probe expressions are evaluated as
// SEPARATE programs by CDP Runtime.evaluate, so the helpers cannot be
// shared page-side and must be inlined into every expression that needs
// them. The committed expression files carry __PLACEHOLDER__ markers that
// assembleProbes splices with these fragments at package init — the
// fragments live in one place instead of being copy-pasted into every
// expression.
//
// TODO: consider installing the probe library once per session as page
// globals (window.__probes = {...}) so the shared fragments are parsed
// once instead of spliced into every expression.
//
// findRootJS tries the candidate selectors in order and returns the first
// match (or null).
//
//go:embed js/lib/find_root.js
var findRootJS string

// scrollStateJS locates the element that actually scrolls the active view:
// monaco-lists scroll their .monaco-scrollable-element DIRECT CHILD
// (overflow:hidden, scrolled programmatically via scrollTop), so a "walk up
// for overflow:auto/scroll" heuristic never finds it. The pane root
// contains TWO such lists — the chat message list and the agent-sessions
// list (session-selection view) — and exactly one of them is expanded at a
// time, so the one with the most scrollable room (scrollHeight -
// clientHeight) is the active one. The ancestor walk remains as a fallback
// for other pane shapes. It also records the measured container as a DOM
// path from the pane root (scrollPath), the rows' offset/geometry
// (scrollOffset/scrollRows), and the nested scroll containers
// (nestedScrolls).
//
//go:embed js/lib/scroll_state.js
var scrollStateJS string

// inputEditorJS locates the chat input's Monaco editor and reports whether
// it holds the page focus (document.activeElement inside it — with the
// native edit context that is a .native-edit-context div, not the old
// textarea). The mirror needs the focus state because live keeps the input
// cursor hidden whenever the input is not focused, and a static HTML
// snapshot cannot tell the two apart (the cursor element exists in both
// states). It also reports the cursor's CHARACTER INDEX (count of
// characters before the caret), computed from the live layout: the cursor's
// inline top picks the wrapped line, its left the offset within it. The
// mirror re-wraps the input text at its own width (responsive layout), so
// it needs the index — a size-independent identity — not the live cursor
// pixels. It also fingerprints the editor width plus each line's character
// count (inputFP) and the hard/soft wrap boundaries (inputBreaks).
//
//go:embed js/lib/input_editor.js
var inputEditorJS string

// cssFPJS computes the cheap CSS fingerprint: the stylesheet count plus
// the inline <style> lengths on documentElement and body. VS Code adds
// inline <style> elements (e.g. when opening a context view), so any of
// those flips the fingerprint and triggers a CSS re-extract.
//
//go:embed js/lib/css_fp.js
var cssFPJS string

// scrollObjJS builds the scroll state object from the measured scroller
// (sc) and the rows' offset (scrollOffset). When no real scroller exists
// (sc is null), it reports the pane root's own geometry as a neutral state:
// the mirror early-returns on the null scrollPath, so those values are
// informational only.
//
//go:embed js/lib/scroll_obj.js
var scrollObjJS string

// Templated probe expressions (js/*.js): the committed files carry the
// __PLACEHOLDER__ markers; the final strings are assembled below.
//
// htmlExprTmpl extracts the pane subtree's outerHTML plus the layout
// metadata the mirror needs (bounding box, scroll, theme inline styles, and
// a cheap CSS fingerprint). It is an arrow function (NOT invoked);
// ExtractHTML passes the candidate selectors as its single argument.
//
//go:embed js/html_expr.js
var htmlExprTmpl string

// fpExprTmpl is the cheap per-poll probe: it returns a content fingerprint,
// a CSS fingerprint, and the layout metadata (bounding box + scroll)
// without dumping the pane's outerHTML.
//
//go:embed js/fp_expr.js
var fpExprTmpl string

// healthExprTmpl samples the live page's size and memory: total/pane DOM
// node counts, stylesheet count, and the JS heap (when the Chromium build
// exposes performance.memory). The mirror evaluates it once a minute so the
// log can correlate refresh latency with renderer pressure over long
// sessions.
//
//go:embed js/health_expr.js
var healthExprTmpl string

// clickPointExprTmpl resolves a DOM path (child indices from the pane root)
// plus a relative (0..1) position within the target element to an absolute
// live-page coordinate. Mapping by element identity (rather than by absolute
// pane offset) keeps forwarded clicks accurate even when the mirror's
// rendering is not pixel-identical to the live layout. A DOM path is also
// robust to sibling additions elsewhere in the tree (e.g. the conversation
// growing), which would shift a flat descendant index.
// NOTE: takes a single args array (not 4 params) so it composes with
// callExpr, which wraps the expression in an IIFE and passes one argument.
//
//go:embed js/click_point_expr.js
var clickPointExprTmpl string

// charPointExprTmpl resolves a chat-input caret character index (count of
// characters before the caret, same identity as FingerprintState.CursorChar)
// to an absolute live-page coordinate: the click point on the caret's
// boundary. The mirror re-wraps the input text at its own width, so a
// pixel mapping (path + relative position) cannot reach the wrapped lines;
// the character index is size-independent, and this eval re-seats it on the
// LIVE layout, where the measurement is exact.
// NOTE: takes a single args array (not 2 params) so it composes with
// callExpr, which wraps the expression in an IIFE and passes one argument.
//
//go:embed js/char_point_expr.js
var charPointExprTmpl string

// rectExprTmpl resolves a DOM path (child indices from the pane root) to the
// target element's bounding box in viewport coordinates. ok:false (via a
// null return) when the path does not resolve.
//
//go:embed js/rect_expr.js
var rectExprTmpl string

// The final probe expressions, assembled once at package init.
var (
	htmlExpr       = assembleProbes(htmlExprTmpl)
	fpExpr         = assembleProbes(fpExprTmpl)
	healthExpr     = assembleProbes(healthExprTmpl)
	clickPointExpr = assembleProbes(clickPointExprTmpl)
	charPointExpr  = assembleProbes(charPointExprTmpl)
	rectExpr       = assembleProbes(rectExprTmpl)
)

// assembleProbes splices the shared fragments (js/lib) into a templated
// probe expression.
func assembleProbes(tmpl string) string {
	return strings.NewReplacer(
		"__FIND_ROOT__", findRootJS,
		"__SCROLL_STATE__", scrollStateJS,
		"__INPUT_EDITOR__", inputEditorJS,
		"__CSS_FP__", cssFPJS,
		"__SCROLL_OBJ__", scrollObjJS,
	).Replace(tmpl)
}

// cssExpr dumps every accessible stylesheet and rewrites each @font-face
// font URL into an inlined base64 data URI, so the returned CSS is fully
// self-contained (no external font requests). It is async because it
// fetches the font bytes in the page context. Each font URL is resolved
// against its own stylesheet's base (sheet.href) before fetching, which is
// required for VS Code's vscode-file:// resources.
//
//go:embed js/css_expr.js
var cssExpr string

// callExpr wraps an arrow-function expression in an IIFE and evaluates it,
// passing argsJSON (a JS array literal, or "" for none) as the argument.
// Runtime.evaluate does NOT invoke a bare function expression with "args",
// so the IIFE form is required.
//
// The wrapper is an async IIFE that stamps the result object with
// __cdp_ms, the page-side execution time (performance.now delta). It splits
// the total round-trip logged by Session.Call into two parts: __cdp_ms =
// probe cost on the page (DOM size / JS heap pressure), the remainder =
// CDP transport + browser main-thread queue latency.
func callExpr(s *Session, expr, argsJSON string) (jsontext.Value, error) {
	full := "(async () => { const __t0 = performance.now(); const __r = await (" + expr + ")(" + argsJSON + ");" +
		" if (__r && typeof __r === 'object') __r.__cdp_ms = Math.round((performance.now() - __t0) * 100) / 100;" +
		" return __r; })()"
	return s.Call("Runtime.evaluate", map[string]any{
		"expression":    full,
		"awaitPromise":  true,
		"returnByValue": true,
	})
}

// evalMs extracts the page-side execution time (__cdp_ms, in ms) stamped by
// callExpr into the evaluate result. Returns -1 when the field is absent
// (null result or a failed eval).
func evalMs(raw jsontext.Value) float64 {
	var v struct {
		CdpMs float64 `json:"__cdp_ms"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return -1
	}
	return v.CdpMs
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
	s.log.Debug("extractHTML", "pageMs", evalMs(raw))
	return decodeEval[HTMLState](raw)
}

// Fingerprint returns a cheap probe of the pane (content + CSS fingerprint
// plus layout metadata) without dumping the outerHTML.
func Fingerprint(s *Session, selectors []string) (*FingerprintState, error) {
	raw, err := callExpr(s, fpExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	s.log.Debug("fingerprint", "pageMs", evalMs(raw))
	return decodeEval[FingerprintState](raw)
}

// ExtractCSS returns the full workbench CSS with @font-face fonts inlined
// as base64 data URIs (self-contained).
func ExtractCSS(s *Session) (*CSSState, error) {
	raw, err := callExpr(s, cssExpr, "")
	if err != nil {
		return nil, err
	}
	s.log.Debug("extractCSS", "pageMs", evalMs(raw))
	return decodeEval[CSSState](raw)
}

// Health returns a DOM/JS-heap sample of the live page.
func Health(s *Session, selectors []string) (*PageHealth, error) {
	raw, err := callExpr(s, healthExpr, selectorsJSON(selectors))
	if err != nil {
		return nil, err
	}
	return decodeEval[PageHealth](raw)
}

// EvalClickPoint resolves a DOM path (child indices from the pane root) plus
// a relative (0..1) position within the target element to an absolute
// live-page coordinate. An empty path means the root itself; navigation
// failure falls back to the root.
func EvalClickPoint(s *Session, selectors []string, path []int, relX, relY float64) (float64, float64, bool) {
	args, _ := json.Marshal([]any{selectors, path, relX, relY})
	raw, err := callExpr(s, clickPointExpr, string(args))
	if err != nil {
		return 0, 0, false
	}
	// A null result means the path did not resolve (the pane root is gone,
	// or a -1 popup path with no visible context view). Decoding null would
	// silently yield (0,0) — the live window's top-left corner.
	if isNullResult(raw) {
		return 0, 0, false
	}
	v, err := decodeEval[struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}](raw)
	if err != nil {
		return 0, 0, false
	}
	return v.X, v.Y, true
}

// EvalCharPoint resolves a chat-input caret character index to an absolute
// live-page coordinate (see charPointExprTmpl). ok is false when the input
// editor or its text cannot be resolved.
func EvalCharPoint(s *Session, selectors []string, char int) (float64, float64, bool) {
	args, _ := json.Marshal([]any{selectors, char})
	raw, err := callExpr(s, charPointExpr, string(args))
	if err != nil {
		return 0, 0, false
	}
	// A null result means the input editor or its text did not resolve.
	// Decoding null would silently yield (0,0) — the live window's
	// top-left corner.
	if isNullResult(raw) {
		return 0, 0, false
	}
	v, err := decodeEval[struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}](raw)
	if err != nil {
		return 0, 0, false
	}
	return v.X, v.Y, true
}

// EvalRect resolves a DOM path to the element's bounding box in viewport
// coordinates. ok is false when the path does not resolve.
func EvalRect(s *Session, selectors []string, path []int) (PaneRect, bool) {
	args, _ := json.Marshal([]any{selectors, path})
	raw, err := callExpr(s, rectExpr, string(args))
	if err != nil {
		return PaneRect{}, false
	}
	v, err := decodeEval[struct {
		OK     bool    `json:"ok"`
		Left   float64 `json:"left"`
		Top    float64 `json:"top"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}](raw)
	if err != nil || !v.OK {
		return PaneRect{}, false
	}
	return PaneRect{Left: v.Left, Top: v.Top, Width: v.Width, Height: v.Height}, true
}

// evalResp is a Runtime.evaluate response: the result value plus the
// page-side exception (if any).
type evalResp struct {
	Result struct {
		Value jsontext.Value `json:"value"`
	} `json:"result"`
	Exception struct {
		Text string `json:"text"`
		Obj  struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// exception returns the page-side exception description of a response (""
// when none). Note: CDP puts the description at
// exceptionDetails.exception.description, with a human-readable fallback at
// exceptionDetails.text.
func (r evalResp) exception() string {
	if r.Exception.Obj.Description != "" {
		return r.Exception.Obj.Description
	}
	return r.Exception.Text
}

// isNullResult reports whether a Runtime.evaluate response carries a null
// value (the expression returned null). Decoding null into a struct is a
// silent no-op, so a probe that returns null to signal failure would
// otherwise look like a successful zero-value result — for the point
// probes that means a click dispatched at (0,0), the live window's
// top-left corner (the menu bar).
func isNullResult(raw jsontext.Value) bool {
	var resp evalResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false
	}
	return resp.Result.Value == nil || string(resp.Result.Value) == "null"
}

// decodeEval unwraps a Runtime.evaluate response and decodes result.value
// into T. Page exceptions are surfaced as errors. A rejected promise
// (awaitPromise) also carries result.value = {}, so the exception check
// must happen before decoding the value.
func decodeEval[T any](raw jsontext.Value) (*T, error) {
	var resp evalResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if desc := resp.exception(); desc != "" {
		return nil, errors.New("page exception: " + desc)
	}
	var out T
	if err := json.Unmarshal(resp.Result.Value, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// workspaceExpr asks the workbench which workspace this window has open.
// The main process hands every renderer its own window configuration
// (window.vscode.context.resolveConfiguration); its workspace field is
// the single-folder URI (w.uri) or the .code-workspace file (w.configPath)
// in the same percent-encoded format storage.json uses as its keys.
//
//go:embed js/workspace_expr.js
var workspaceExpr string

// WorkspaceURI returns the workspace URI the window currently has open —
// in storage.json key format (file:///c%3A/... or
// vscode-remote://ssh-remote%2Bhost/...) — or "" for empty windows and
// failed probes.
func WorkspaceURI(s *Session) string {
	raw, err := s.Call("Runtime.evaluate", map[string]any{
		"expression":    workspaceExpr,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return ""
	}
	uri, err := decodeEval[string](raw)
	if err != nil || *uri == "" {
		return ""
	}
	return *uri
}
