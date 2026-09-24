package mirror

import (
	"regexp"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/workspaces"
)

// stateMsg is the server -> browser message carrying a pane snapshot.
// HTML and CSS are only present when they changed (the browser keeps its
// last copy otherwise); rect/scroll/rootStyle/cssVersion are always sent.
type stateMsg struct {
	Type      string `json:"type"` // "state"
	HTML      string `json:"html,omitempty"`
	CSS       string `json:"css,omitempty"`
	CSSVer    string `json:"cssVersion,omitempty"`
	RootStyle string `json:"rootStyle,omitempty"`
	ThemeVars string `json:"themeVars,omitempty"`
	ThemeVer  string `json:"themeVer,omitempty"`
	// ThemeBg is the pane root's effective background color; the browser
	// pins it on html/body (--mirror-bg) so the page frame matches the live
	// theme.
	ThemeBg string `json:"themeBg,omitempty"`
	// InputFocused reports whether the live chat input holds the page focus.
	// The mirror shows (and blinks) the input cursor only then: live keeps
	// the cursor hidden whenever the input is not focused.
	InputFocused bool `json:"inputFocused,omitempty"`
	// CursorChar is the live cursor's character index (count of characters
	// before the caret); -1 when the input has no cursor. The mirror
	// re-wraps the input text at its own width, so it re-seats the cursor by
	// this index instead of the live cursor pixels. No omitempty: 0 (caret
	// at the start) is a valid value.
	CursorChar int `json:"cursorChar"`
	// InputBreaks marks which live view-line boundaries are hard newlines
	// (true) versus soft wraps (false); the mirror merges the segments
	// joined by soft wraps so the text flows continuously at the mirror
	// width. See cdp.FingerprintState.InputBreaks.
	InputBreaks []bool         `json:"inputBreaks,omitempty"`
	Rect        cdp.PaneRect   `json:"rect"`
	Scroll      cdp.PaneScroll `json:"scroll"`
	// ScrollPath identifies the measured scroll container as a DOM path from
	// the pane root (no omitempty: an empty path means "the root itself").
	ScrollPath []int `json:"scrollPath"`
	// ScrollRows is each rendered row's [offsetTop, offsetHeight] pair in
	// full-content coordinates; the browser uses it to align its viewport
	// with the live viewport inside the rendered row window.
	ScrollRows []float64 `json:"scrollRows,omitempty"`
	// NestedScrolls carries the scroll state of nested scroll containers the
	// main scroller does not cover (currently: the reasoning-trace list of
	// each .chat-thinking-box); the browser maps each live ratio onto its own
	// copy of the element.
	NestedScrolls []cdp.NestedScroll `json:"nestedScrolls,omitempty"`
	// Popup is the visible context view (popup menu / dropdown) that renders
	// outside the pane root. HTML is only present when it changed since the
	// last message; Left/Top are pane-relative and always present. Null when
	// no popup is open (the browser removes its copy).
	Popup  *cdp.PopupState `json:"popup,omitempty"`
	Window string          `json:"window,omitempty"`
	// WindowID is the CDP target id of the window currently mirrored; the
	// browser uses it to mark the selected item in the status-bar picker.
	WindowID string `json:"windowId,omitempty"`
	// Windows is the current set of live windows (title-sorted) for the
	// status-bar picker. Sent with every state message so the picker always
	// reflects reality (window open/close).
	Windows []cdp.Window `json:"windows"`
	Err     string       `json:"err,omitempty"`
}

// mouseMsg is a browser -> server mouse event. X/Y are relative to the
// mirrored pane's top-left corner. Path/RelX/RelY identify the element under
// the cursor (by DOM path from the pane root) and the position within it, so
// clicks can be mapped by element identity rather than absolute offset.
type mouseMsg struct {
	Type    string  `json:"type"` // "mouse"
	Kind    string  `json:"kind"` // pressed|released|moved|wheel
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Path    []int   `json:"path"`
	RelX    float64 `json:"relX"`
	RelY    float64 `json:"relY"`
	Button  string  `json:"button,omitempty"`
	Buttons int     `json:"buttons,omitempty"`
	DeltaX  float64 `json:"deltaX,omitempty"`
	DeltaY  float64 `json:"deltaY,omitempty"`
	Clicks  int     `json:"clickCount,omitempty"`
	// AnchorPath identifies the control the user clicked (the nearest
	// button-like ancestor of the click target, as a DOM path from the pane
	// root). The mirror anchors the next popup's position to it, because the
	// live pane-relative popup offset does not transfer to the mirror's
	// responsive layout.
	AnchorPath []int `json:"anchorPath,omitempty"`
	// Char is the chat-input caret character index under a click inside the
	// input editor (count of characters before the caret; nil when the click
	// is outside the input). The mirror re-wraps the input at its own width,
	// so a click on a wrapped line has no live pixel equivalent by element
	// identity; the server re-seats the character on the live layout
	// (cdp.EvalCharPoint) instead of using Path/RelX/RelY.
	Char *int `json:"char,omitempty"`
}

// keyMsg is a browser -> server keyboard event.
type keyMsg struct {
	Type      string `json:"type"` // "key"
	Kind      string `json:"kind"` // down|up|insert
	Key       string `json:"key,omitempty"`
	Code      string `json:"code,omitempty"`
	KeyCode   int    `json:"keyCode,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`
	Text      string `json:"text,omitempty"`
}

// windowMsg is a browser -> server window-picker selection: the CDP target
// id of the window the user wants mirrored.
type windowMsg struct {
	Type string `json:"type"` // "window"
	ID   string `json:"id"`
}

// workspacesMsg is a server -> browser message for the workspaces page:
// the current workspace list. Sent on connect and whenever the list
// changes (a storage.json reload, a window opening/closing, or the
// fallback tick re-probing the live windows), so the page's green dots
// update live without a refresh. The open flags follow the LIVE windows
// (CDP probe), not storage.json's lagging windowsState.
type workspacesMsg struct {
	Type       string                 `json:"type"` // "workspaces"
	Workspaces []workspaces.Workspace `json:"workspaces"`
}

// shutdownMsg is a server -> browser message for the workspaces page:
// the current armed state of the "power off at the next task end" toggle.
// Sent on connect and on every arm/disarm, so several open tabs stay in
// sync.
type shutdownMsg struct {
	Type  string `json:"type"` // "shutdown"
	Armed bool   `json:"armed"`
}

// imgEntry is one fetched image resource, cached by the hash of its source
// URL (which doubles as the /img/<hash> path segment).
type imgEntry struct {
	data        []byte
	contentType string
}

// imgSrcRe matches blob:/vscode-file: image srcs in extracted pane HTML.
// These URLs are only valid inside the live page's document, so the mirror
// rewrites them to /img/<hash> URLs served by this process.
var imgSrcRe = regexp.MustCompile(`src="((?:blob:|vscode-file:)[^"]*)"`)
