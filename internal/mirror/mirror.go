package mirror

// Package mirror forwards a VS Code Copilot pane to a browser page.
//
// Rendering: the pane's HTML subtree plus the (self-contained) workbench CSS
// are extracted from the live page over CDP and pushed to the browser over a
// WebSocket. Interactivity: the browser captures mouse/keyboard events as
// coordinates and the mirror translates them back onto the live page using
// the CDP Input domain. No page-side JS is injected for interactivity, so
// the page is modified as little as possible.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go4org/hashtriemap"
	"github.com/gorilla/websocket"

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

// Mirror owns the web server, the set of connected browser tabs, and the
// publish loop that keeps each mirrored pane in sync with its live page.
type Mirror struct {
	disc      *cdp.Discovery
	log       *slog.Logger
	addr      string
	selectors []string
	window    string
	// storagePath is the VS Code globalStorage storage.json the workspaces
	// page lists.
	storagePath string
	// opener launches workspaces through the code CLI (workspaces page).
	opener *workspaces.Opener
	// wsStore is the hot-reloaded workspace list (storage.json, watched like
	// settings.json). Readers take an atomic load; the watcher pushes a
	// "workspaces" message to the workspaces-page clients on every
	// successful reload. Nil when the initial load failed (the HTTP API
	// then falls back to a fresh read per request).
	wsStore *workspaces.Store
	// fallback bounds how often the publish loop probes even when the page
	// sent no wake event. It covers changes the injected observer cannot
	// see (CSSOM-only edits, dropped wakes) and re-resolves the session
	// after a window switch or reload.
	fallback time.Duration

	upgrader websocket.Upgrader

	// clients is the set of connected browser tabs. It is a lock-free
	// hash-trie map, so add/remove/send never take a shared lock.
	clients hashtriemap.HashTrieMap[*client, struct{}]

	// imgs caches fetched image resources by the hash of their source URL.
	// A lock-free hash-trie map: reads from the HTTP handlers and writes
	// from the single publish-loop goroutine never block.
	imgs hashtriemap.HashTrieMap[string, imgEntry]

	// snaps holds the latest extracted pane snapshot per mirrored window.
	// The map is mutable shared state, so it is guarded by snapMu; the
	// (potentially large) html/css strings are shared by reference across
	// snapshots, so a swap is cheap.
	snapMu sync.Mutex
	snaps  map[string]*snapState

	// hub fans the MirrorWake channels of all watched sessions into one
	// channel so the publish loop can select on "any watched window
	// changed" (the watched window set is dynamic, so a plain select
	// cannot express it).
	hub *wakeHub
	// refreshNow is signalled by the WS handler (new client, picker change)
	// so the publish loop re-resolves the client groups and refreshes
	// immediately instead of waiting for the next fallback tick. Buffered
	// by 1: one pending signal coalesces.
	refreshNow chan struct{}

	// toggleTimes records the last auto Toggle-Chat click per window, so a
	// missing pane does not re-click the button on every refresh (the pane
	// takes a moment to render after a click, and a window without the
	// button must not be re-probed constantly). Only the publish-loop
	// goroutine touches it.
	toggleTimes map[string]time.Time

	// wsRefresh is signalled by the storage.json watcher (the known
	// workspace list changed) so the publish loop recomputes the
	// CDP-overlaid list. Buffered by 1: one pending signal coalesces.
	wsRefresh chan struct{}
	// lastWsPush is the last workspace list pushed to the workspaces-page
	// clients; it drives change detection for the next push. Only the
	// publish-loop goroutine touches it.
	lastWsPush []workspaces.Workspace
}

// snapState is one extracted pane snapshot.
type snapState struct {
	html       string
	css        string
	cssVer     string
	cssFP      string
	rootStyle  string
	themeVars  string
	themeVer   string
	themeBg    string
	window     string
	windowID   string
	fp         string
	rect       cdp.PaneRect
	scroll     cdp.PaneScroll
	scrollPath []int
	scrollRows []float64
	// nestedScrolls: scroll state of nested scrollers (reasoning-trace
	// lists) the main scroller does not cover.
	nestedScrolls []cdp.NestedScroll
	// inputFocused: whether the live chat input holds the page focus (the
	// mirror shows/blinks the input cursor only then).
	inputFocused bool
	// cursorChar: the live cursor's character index (-1 = no cursor); the
	// mirror re-seats the cursor by this index at its own (reflowed) width.
	cursorChar int
	// inputBreaks: hard-newline vs soft-wrap boundaries between the live
	// input's view lines (see cdp.FingerprintState.InputBreaks); the mirror
	// re-groups the view lines across soft wraps.
	inputBreaks []bool
	// popupHTML/popupFP capture the visible context view (popup menu) that
	// renders outside the pane root; popupFP == "" means no popup is open.
	popupHTML string
	popupFP   string
	popupLeft float64
	popupTop  float64
	popupW    float64
	popupH    float64
}

// client is one connected browser tab.
type client struct {
	logger *slog.Logger
	conn   *websocket.Conn
	// send carries server -> browser frames (stateMsg for mirror tabs,
	// workspacesMsg for workspaces-page tabs); the writer marshals any.
	send chan any
	done chan struct{}
	// wsPage marks a workspaces-page tab (?page=workspaces): it consumes
	// only "workspaces" list pushes and is excluded from the publish
	// loop's pane-state broadcast.
	wsPage bool
	// selID is this tab's status-bar window picker selection (nil = follow
	// the default: -window flag, else first window). Per-client, so several
	// browsers can mirror different VS Code windows at once. Written by the
	// WS handler, read by the publish loop — an atomic pointer keeps it
	// lock-free.
	selID atomic.Pointer[string]
	// lastAnchor is the DOM path (from the pane root) of the control THIS
	// tab last clicked (a pressed mouse event carrying an anchorPath). The
	// publish loop resolves it to a live rect to anchor the open popup's
	// position for this tab. Written by the WS handler, read by the publish
	// loop.
	lastAnchor atomic.Pointer[[]int]
	// lastWin is the window id of the last successful full state this tab
	// rendered (nil = none yet). When the tab's current window differs from
	// it (just connected, or just switched via the picker), the next
	// refresh forces a full HTML/CSS send for this tab even if the window
	// has not changed since the last refresh.
	lastWin atomic.Pointer[string]
}

// wakeHub fans the MirrorWake channels of several sessions into one channel
// so the publish loop can select on "any watched window changed". One
// forwarding goroutine runs per subscribed channel; it is cancelled via a
// quit channel because mirrorWake is never closed.
type wakeHub struct {
	mu   sync.Mutex
	out  chan struct{}
	subs map[<-chan struct{}]chan struct{} // wake channel -> its quit signal
}

func newWakeHub() *wakeHub {
	return &wakeHub{out: make(chan struct{}, 64), subs: make(map[<-chan struct{}]chan struct{})}
}

// add subscribes one session's wake channel. Idempotent: the forwarding
// goroutine is spawned once per channel.
func (h *wakeHub) add(ch <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		return
	}
	quit := make(chan struct{})
	h.subs[ch] = quit
	go func() {
		for {
			select {
			case <-ch:
				// Non-blocking: one pending token already guarantees a
				// refresh, and the refresh reads the latest state.
				select {
				case h.out <- struct{}{}:
				default:
				}
			case <-quit:
				return
			}
		}
	}()
}

// remove unsubscribes and stops the forwarding goroutine.
func (h *wakeHub) remove(ch <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if q, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(q)
	}
}

// lanIP returns the machine's primary LAN IPv4 address (via the route to a
// public endpoint; no packets are sent). Empty string on failure.
func lanIP() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}

// New creates a Mirror. addr is the web bind address; selectors are the
// candidate pane root selectors (tried in order); window is a title
// substring used to pick the VS Code window (empty = first window);
// storagePath/codePath back the workspaces page (list + launch).
func New(disc *cdp.Discovery, log *slog.Logger, addr string, selectors []string, window, storagePath, codePath string) *Mirror {
	m := &Mirror{
		disc:        disc,
		log:         log,
		addr:        addr,
		selectors:   selectors,
		window:      window,
		storagePath: storagePath,
		opener:      workspaces.NewOpener(codePath, log),
		fallback:    1 * time.Second,
		snaps:       make(map[string]*snapState),
		hub:         newWakeHub(),
		refreshNow:  make(chan struct{}, 1),
		wsRefresh:   make(chan struct{}, 1),
		toggleTimes: make(map[string]time.Time),
		upgrader: websocket.Upgrader{
			// Local-only tool; accept any origin (127.0.0.1 / localhost).
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	// The workspace list is loaded once up front, then hot-reloaded via
	// fsnotify (like settings.json). A known-list change signals the
	// publish loop, which recomputes the CDP-overlaid list (the open
	// flags follow the live windows, not storage.json's lagging
	// windowsState) and pushes it to the workspaces-page clients. A
	// missing/unreadable storage.json is not fatal: the HTTP API falls
	// back to a fresh read.
	if st, err := workspaces.NewStore(storagePath, log, m.onWorkspacesChanged); err != nil {
		log.Warn("workspaces: initial list failed", "path", storagePath, "err", err)
	} else {
		m.wsStore = st
	}
	return m
}

// Run starts the web server and the publish loop. It blocks until ctx is
// cancelled, then shuts everything down.
func (m *Mirror) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handlePage)
	mux.HandleFunc("/ws", m.handleWS)
	mux.HandleFunc("/img/", m.handleImage)
	mux.HandleFunc("/workspaces", m.handleWorkspacesPage)
	mux.HandleFunc("/api/workspaces", m.handleWorkspaceList)
	mux.HandleFunc("/api/workspaces/open", m.handleWorkspaceOpen)
	srv := &http.Server{Addr: m.addr, Handler: mux}

	if m.wsStore != nil {
		if err := m.wsStore.Watch(ctx); err != nil {
			// Non-fatal: the list stays at its initial snapshot and the
			// HTTP API still serves it.
			m.log.Error("start workspaces watcher", "err", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		m.log.Info("mirror web server listening", "addr", m.addr)
		if h, _, err := net.SplitHostPort(m.addr); err == nil && (h == "0.0.0.0" || h == "::" || h == "") {
			if ip := lanIP(); ip != "" {
				port := m.addr[strings.LastIndexByte(m.addr, ':')+1:]
				m.log.Info("mirror reachable from LAN at", "url", "http://"+ip+":"+port)
			}
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go m.publishLoop(ctx)
	go m.memReporter(ctx)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	m.closeClients()
	return nil
}

func (m *Mirror) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}

// handleWorkspacesPage serves the workspaces page. The mux pattern
// "/workspaces" matches only that exact path, so no path check is needed.
func (m *Mirror) handleWorkspacesPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(workspacesHTML))
}

// handleWorkspaceList serves the current workspace list: the known
// workspaces with the live open state overlaid from CDP (falls back to a
// fresh read of storage.json when the store is nil).
func (m *Mirror) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	ws, err := m.currentWorkspaces()
	if err != nil {
		m.log.Warn("workspaces: list failed", "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, ws)
}

// openReq is the browser -> server request to launch one workspace.
type openReq struct {
	URI string `json:"uri"`
}

// handleWorkspaceOpen launches a VS Code window for one workspace via the
// code CLI (fire-and-forget).
func (m *Mirror) handleWorkspaceOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req openReq
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.MarshalWrite(w, map[string]string{"err": "bad request: " + err.Error()})
		return
	}
	if err := m.opener.Open(req.URI); err != nil {
		m.log.Warn("workspaces: open failed", "uri", req.URI, "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]bool{"ok": true})
}

// liveWorkspaces probes every live window for the workspace it has open
// (the same workbench probe the jump-to-window logic uses: the main
// process hands every renderer its window configuration, so the answer
// is exact, not a title guess) and returns window id -> workspace URI
// (storage.json key format). Empty windows (no folder open) and failed
// probes are omitted.
func (m *Mirror) liveWorkspaces() map[string]string {
	out := make(map[string]string)
	for _, win := range m.disc.Windows() {
		s := m.disc.SessionForID(win.ID)
		if s == nil {
			continue
		}
		if uri := cdp.WorkspaceURI(s); uri != "" {
			out[win.ID] = uri
		}
	}
	return out
}

// resolveWindowURI finds the live window that has the given workspace
// open. ok is false when no live window matches.
func (m *Mirror) resolveWindowURI(uri string) (string, bool) {
	for id, wuri := range m.liveWorkspaces() {
		if workspaces.SameURI(uri, wuri) {
			return id, true
		}
	}
	return "", false
}

// currentWorkspaces returns the workspace list to show: the known
// workspaces (hot-reloaded storage.json) with the live open state
// overlaid from CDP, so the open flags follow the actual windows instead
// of storage.json's lagging windowsState.
func (m *Mirror) currentWorkspaces() ([]workspaces.Workspace, error) {
	var known []workspaces.Workspace
	var err error
	if m.wsStore != nil {
		known = m.wsStore.Load()
	} else {
		known, err = workspaces.List(m.storagePath)
		if err != nil {
			return nil, err
		}
	}
	return workspaces.Overlay(known, m.liveWorkspaces()), nil
}

// handleImage serves one cached image resource under /img/<hash>.
func (m *Mirror) handleImage(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/img/")
	if key == "" || strings.Contains(key, "/") {
		http.NotFound(w, r)
		return
	}
	e, ok := m.imgs.Load(key)
	if !ok {
		m.log.Debug("image: serve miss", "key", key)
		http.NotFound(w, r)
		return
	}
	m.log.Debug("image: serve hit", "key", key, "bytes", len(e.data), "type", e.contentType)
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(e.data)
}

// rewriteImages replaces blob:/vscode-file: image srcs in extracted pane or
// popup HTML with /img/<hash> URLs. Each source URL is fetched from the live
// page once (via CDP) and cached; on fetch failure the original src is left
// untouched (the mirror shows a broken image for that one, as before).
func (m *Mirror) rewriteImages(s *cdp.Session, html string) string {
	for _, mth := range imgSrcRe.FindAllStringSubmatch(html, -1) {
		u := mth[1]
		key := hashStr(u)
		if _, ok := m.imgs.Load(key); !ok {
			m.log.Debug("image: fetch attempt", "url", u, "key", key)
			fStart := time.Now()
			data, ctype, via, err := cdp.FetchImage(s, u)
			if err != nil {
				m.log.Warn("image fetch failed", "url", u, "err", err)
				continue
			}
			if ctype == "" {
				ctype = guessImageType(data)
			}
			m.log.Debug("image: fetched", "key", key, "via", via, "bytes", len(data), "type", ctype, "ms", time.Since(fStart).Milliseconds())
			m.imgs.Store(key, imgEntry{data: data, contentType: ctype})
		}
		html = strings.ReplaceAll(html, `src="`+u+`"`, `src="/img/`+key+`"`)
	}
	return html
}

// guessImageType falls back to magic-byte sniffing when a blob fetch reports
// no content type.
func guessImageType(data []byte) string {
	switch {
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return "image/gif"
	case len(data) >= 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return "image/png"
	}
}

func (m *Mirror) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan any, 64), done: make(chan struct{}), logger: m.log.With("remote", r.RemoteAddr)}
	// A workspaces-page tab (?page=workspaces) only consumes the workspace
	// list pushes; it never mirrors a pane.
	if r.URL.Query().Get("page") == "workspaces" {
		c.wsPage = true
	} else if wsURI := r.URL.Query().Get("ws"); wsURI != "" {
		// A tab arriving from the mirror carries its target workspace
		// (?ws=). Resolve it to the live window BEFORE the client is added,
		// so the first snapshot is already the requested window (no flash
		// of the default window).
		if id, ok := m.resolveWindowURI(wsURI); ok {
			c.selID.Store(&id)
			m.log.Info("mirror: ws param resolved", "uri", wsURI, "id", id)
		} else {
			m.log.Warn("mirror: ws param unresolved", "uri", wsURI)
		}
	}
	m.addClient(c)
	defer m.removeClient(c)
	go c.writePump()

	if c.wsPage {
		// The list arrives over the socket: once on connect, then after
		// every successful storage.json reload.
		m.sendWorkspaces(c)
	} else {
		// Render immediately with whatever we already have, then ask the
		// publish loop for a fresh refresh: a just-connected tab has no
		// selection yet, and its default window's snapshot may be missing
		// or stale.
		m.sendFullState(c)
		m.signalRefresh()
	}

	conn.SetReadLimit(1 << 20)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		m.handleInput(c, raw)
	}
}

// signalRefresh asks the publish loop to re-resolve the client groups and
// refresh immediately (picker change, new client). Non-blocking: one
// pending signal coalesces.
func (m *Mirror) signalRefresh() {
	select {
	case m.refreshNow <- struct{}{}:
	default:
	}
}

func (c *client) writeMsg(msg any) error {
	wStart := time.Now()
	wt, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		c.logger.Warn("failed to get writer", "err", err)
		return err
	}
	defer wt.Close()

	err = json.MarshalWrite(wt, msg)

	if err != nil {
		c.logger.Warn("failed to write message", "err", err)
		return err
	}

	if ms := time.Since(wStart).Milliseconds(); ms >= 100 {
		// A slow write means the browser tab is not draining the
		// WebSocket (heavy DOM patching on its side, or a throttled
		// background tab); the frame queue backs up behind it.
		c.logger.Debug("writePump: slow write", "ms", ms)
	}
	return nil
}

func (c *client) writePump() {
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.send:
			err := c.writeMsg(msg)
			if err != nil {
				return
			}
		}
	}
}

func (m *Mirror) addClient(c *client) {
	m.clients.Store(c, struct{}{})
}

func (m *Mirror) removeClient(c *client) {
	// LoadAndDelete is atomic: only the first remover wins, so c.done and
	// the connection are each closed exactly once even if two callers race.
	if _, ok := m.clients.LoadAndDelete(c); ok {
		close(c.done)
		c.conn.Close()
	}
}

func (m *Mirror) closeClients() {
	var cs []*client
	m.clients.All()(func(c *client, _ struct{}) bool {
		cs = append(cs, c)
		return true
	})
	for _, c := range cs {
		m.removeClient(c)
	}
}

// sendTo queues one frame for a single client. Non-blocking: a slow client
// drops the frame rather than stalling the publisher.
func (c *client) sendTo(msg any) {
	select {
	case c.send <- msg:
	default:
		c.logger.Warn("send: frame dropped (slow client)")
	}
}

// sendWorkspaces queues the current CDP-overlaid workspace list for one
// workspaces-page client (sent on connect). The list is computed on demand
// so a just-appeared tab sees the live windows, not a stale snapshot.
func (m *Mirror) sendWorkspaces(c *client) {
	list, err := m.currentWorkspaces()
	if err != nil || list == nil {
		return
	}
	c.sendTo(workspacesMsg{Type: "workspaces", Workspaces: list})
}

// pushWorkspaces fans the workspace list out to every connected
// workspaces-page client. Non-blocking per client (sendTo drops for slow
// clients), so it is safe to call from any goroutine.
func (m *Mirror) pushWorkspaces(list []workspaces.Workspace) {
	m.clients.All()(func(c *client, _ struct{}) bool {
		if c.wsPage {
			c.sendTo(workspacesMsg{Type: "workspaces", Workspaces: list})
		}
		return true
	})
}

// onWorkspacesChanged is the Store onChange callback: the known workspace
// list changed, so the CDP-overlaid list must be recomputed. The publish
// loop owns the CDP probes, so the watcher only signals it.
func (m *Mirror) onWorkspacesChanged(_ []workspaces.Workspace) {
	select {
	case m.wsRefresh <- struct{}{}:
	default:
	}
}

// refreshWorkspaces recomputes the CDP-overlaid workspace list and pushes
// it to the workspaces-page clients when it changed. Only the publish-loop
// goroutine calls it; a failed computation keeps the previous list.
func (m *Mirror) refreshWorkspaces() {
	list, err := m.currentWorkspaces()
	if err != nil {
		m.log.Warn("workspaces: live list failed, keeping previous", "err", err)
		return
	}
	if equalWorkspaces(m.lastWsPush, list) {
		return
	}
	m.lastWsPush = list
	m.pushWorkspaces(list)
}

// equalWorkspaces reports whether two workspace lists are identical (same
// entries in the same order; Name/Path/Remote derive from the URI, so URI
// + Open + LastActive fully describe an entry).
func equalWorkspaces(a, b []workspaces.Workspace) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].URI != b[i].URI || a[i].Open != b[i].Open || a[i].LastActive != b[i].LastActive {
			return false
		}
	}
	return true
}

// wsPageClients counts the connected workspaces-page tabs.
func (m *Mirror) wsPageClients() int {
	n := 0
	m.clients.All()(func(c *client, _ struct{}) bool {
		if c.wsPage {
			n++
		}
		return true
	})
	return n
}

// imgCount returns the number of cached image resources.
func (m *Mirror) imgCount() int {
	n := 0
	m.imgs.All()(func(_ string, _ imgEntry) bool { n++; return true })
	return n
}

// memReporter logs process memory and the image-cache size every 30s.
// Unbounded growth (e.g. the blob-URL image cache) shows up as a steady
// climb in heapMB / imgs.
func (m *Mirror) memReporter(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			m.log.Debug("mem", "heapMB", ms.HeapAlloc/1024/1024, "sysMB", ms.Sys/1024/1024, "numGC", ms.NumGC, "imgs", m.imgCount())
		}
	}
}

// sendFullState sends the client a full snapshot of the window it is
// currently mirroring (a new client has no selection yet: the default
// window), plus the current window set for the status-bar picker.
func (m *Mirror) sendFullState(c *client) {
	wins := m.disc.Windows()
	id := m.clientWinID(c, wins)
	m.snapMu.Lock()
	var msg stateMsg
	if s := m.snaps[id]; s != nil {
		msg = stateMsg{
			Type: "state", HTML: s.html, CSS: s.css, CSSVer: s.cssVer,
			RootStyle: s.rootStyle, ThemeVars: s.themeVars, ThemeVer: s.themeVer,
			ThemeBg: s.themeBg, InputFocused: s.inputFocused, CursorChar: s.cursorChar,
			Rect: s.rect, Scroll: s.scroll, ScrollPath: s.scrollPath,
			ScrollRows: s.scrollRows, InputBreaks: s.inputBreaks,
			NestedScrolls: s.nestedScrolls,
			Window:        s.window, WindowID: s.windowID,
		}
		if s.popupFP != "" {
			// A just-connected tab has no anchor of its own yet: the browser
			// falls back to the live pane-relative popup offset.
			msg.Popup = &cdp.PopupState{HTML: s.popupHTML, Left: s.popupLeft, Top: s.popupTop, Width: s.popupW, Height: s.popupH}
		}
	} else {
		msg = stateMsg{Type: "state", Err: "waiting for VS Code window"}
	}
	m.snapMu.Unlock()
	msg.Windows = wins
	c.sendTo(msg)
	// Only a successful full state counts as "rendered" for this client:
	// the browser ignores states carrying an error, so an error (or a
	// missing snapshot) must not suppress the next full send.
	if msg.Err == "" {
		c.lastWin.Store(&id)
	}
}

// publishLoop keeps every watched window in sync with its live page.
// Clients are grouped by the window each one mirrors (its own status-bar
// selection, falling back to the -window flag or the first window); each
// distinct window is refreshed once per cycle and its state is sent only to
// the clients watching it, so several browser tabs can mirror different
// VS Code windows at once.
//
// The loop is event-driven: the page's injected observer (cdp.MirrorInjectJS)
// pushes a wake through the session's CDP binding when the DOM, scroll
// position, or window size changes, and the hub fans every watched
// session's wake into one channel. A slow fallback ticker covers what the
// observer cannot see (CSSOM-only edits, dropped wakes) and re-picks the
// session after a window switch or reload.
func (m *Mirror) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(m.fallback)
	defer ticker.Stop()
	// pageHealth samples each live page's DOM/JS-heap size once a minute so
	// the log can correlate refresh latency with renderer growth over long
	// sessions (the suspected cause of the slow-mirror-over-time symptom).
	healthTicker := time.NewTicker(60 * time.Second)
	defer healthTicker.Stop()
	// subs tracks which sessions the hub is subscribed to. Only the
	// publish-loop goroutine touches it, so no lock is needed.
	subs := make(map[*cdp.Session]struct{})
	for {
		wins := m.disc.Windows()
		groups := m.groupClients(wins)
		// Subscribe the hub to exactly the sessions of the watched windows
		// (the default window is always watched, even with no clients).
		watched := make(map[*cdp.Session]struct{}, len(groups))
		for winID := range groups {
			if s := m.disc.SessionForID(winID); s != nil {
				watched[s] = struct{}{}
			}
		}
		for s := range subs {
			if _, ok := watched[s]; !ok {
				m.hub.remove(s.MirrorWake())
				delete(subs, s)
			}
		}
		for s := range watched {
			if _, ok := subs[s]; !ok {
				m.hub.add(s.MirrorWake())
				subs[s] = struct{}{}
			}
		}
		select {
		case <-ctx.Done():
			// Stop the hub's forwarding goroutines for the remaining
			// subscriptions (mirrorWake is never closed, so they would
			// otherwise leak past shutdown).
			for s := range subs {
				m.hub.remove(s.MirrorWake())
			}
			return
		case <-m.hub.out:
			m.log.Debug("mirror: refresh (wake)")
			m.refreshAll()
		case <-m.refreshNow:
			m.log.Debug("mirror: refresh (client event)")
			m.refreshAll()
		case <-m.wsRefresh:
			// The known workspace list changed (storage.json reload);
			// recompute the CDP-overlaid list for the workspaces page.
			m.log.Debug("mirror: workspaces refresh (known list changed)")
			m.refreshWorkspaces()
		case <-m.disc.WindowsChanged():
			// A window opened or closed: the open-workspace set (the
			// workspaces page's green dots) may have changed.
			if m.wsPageClients() > 0 {
				m.log.Debug("mirror: workspaces refresh (window set changed)")
				m.refreshWorkspaces()
			}
		case <-ticker.C:
			m.log.Debug("mirror: refresh (fallback)")
			m.refreshAll()
			// Re-probe the live windows: a window can change its folder
			// (File > Open Folder) without the window set changing, and a
			// just-opened window's CDP session may not have been dialled
			// when the window-set signal fired.
			if m.wsPageClients() > 0 {
				m.refreshWorkspaces()
			}
		case <-healthTicker.C:
			for winID := range groups {
				s := m.disc.SessionForID(winID)
				if s == nil {
					continue
				}
				hStart := time.Now()
				h, err := cdp.Health(s, m.selectors)
				if err != nil {
					m.log.Debug("page health probe failed", "window", winID, "err", err)
					continue
				}
				heapMB := 0.0
				if h.Heap != nil {
					heapMB = h.Heap.Used / 1024 / 1024
				}
				paneNodes := 0
				if h.PaneNodes != nil {
					paneNodes = *h.PaneNodes
				}
				m.log.Debug("page health", "window", winID, "ms", time.Since(hStart).Milliseconds(),
					"nodes", h.Nodes, "paneNodes", paneNodes, "sheets", h.Sheets,
					"heapMB", float64(int(heapMB*10))/10)
			}
		}
	}
}

// defaultWinID is the window a client follows when it has no picker
// selection of its own: the -window title filter (startup flag), else the
// first window in stable title-sorted order. The stable default (instead of
// SessionFor's arbitrary map order) is what stops the mirror flickering
// between windows when several are open.
func (m *Mirror) defaultWinID(wins []cdp.Window) string {
	if len(wins) == 0 {
		return ""
	}
	if m.window != "" {
		for _, w := range wins {
			if strings.Contains(w.Title, m.window) {
				return w.ID
			}
		}
	}
	return wins[0].ID
}

// clientWinID resolves the window this client currently mirrors: its own
// status-bar picker selection (if that window still exists), else the
// default.
func (m *Mirror) clientWinID(c *client, wins []cdp.Window) string {
	if p := c.selID.Load(); p != nil {
		for _, w := range wins {
			if w.ID == *p {
				return *p
			}
		}
	}
	return m.defaultWinID(wins)
}

// groupClients maps each connected client to the window it currently
// mirrors, keyed by window ID. The default window is always present (even
// with no clients) so a freshly connected tab has a fresh snapshot to
// render.
func (m *Mirror) groupClients(wins []cdp.Window) map[string][]*client {
	groups := make(map[string][]*client)
	if id := m.defaultWinID(wins); id != "" {
		groups[id] = nil
	}
	m.clients.All()(func(c *client, _ struct{}) bool {
		// Workspaces-page tabs never mirror a pane.
		if c.wsPage {
			return true
		}
		id := m.clientWinID(c, wins)
		groups[id] = append(groups[id], c)
		return true
	})
	return groups
}

// pickSessionFor resolves which window this client mirrors (its own
// status-bar picker selection, falling back to the default).
func (m *Mirror) pickSessionFor(c *client, wins []cdp.Window) (*cdp.Session, string) {
	if len(wins) == 0 {
		return nil, ""
	}
	id := m.clientWinID(c, wins)
	return m.disc.SessionForID(id), id
}

// refreshAll refreshes every watched window once and sends each client the
// state of the window it is mirroring. The groups are resolved fresh here:
// a client may have connected or changed its picker selection while the
// publish loop was waiting.
func (m *Mirror) refreshAll() {
	wins := m.disc.Windows()
	groups := m.groupClients(wins)
	// Drop snapshots (and auto-open bookkeeping) of windows that closed
	// since the last cycle so the maps cannot grow unbounded.
	if len(wins) > 0 {
		m.snapMu.Lock()
		for id := range m.snaps {
			if !windowIn(wins, id) {
				delete(m.snaps, id)
			}
		}
		m.snapMu.Unlock()
		for id := range m.toggleTimes {
			if !windowIn(wins, id) {
				delete(m.toggleTimes, id)
			}
		}
	}
	for winID, group := range groups {
		m.refreshWindow(winID, wins, group)
	}
}

// refreshWindow probes one window's pane (cheap fingerprint) and, when
// something changed, does a full extract and sends state messages to the
// clients watching that window in two phases: phase 1 carries the pane HTML
// and the popup (styled by the cached CSS) so a popup becomes visible
// without waiting for a CSS re-extract; phase 2 follows with the re-extracted
// CSS when its fingerprint changed (opening a context view makes VS Code add
// an inline <style>, which flips the fingerprint on the same refresh).
func (m *Mirror) refreshWindow(winID string, wins []cdp.Window, group []*client) {
	totalStart := time.Now()
	s := m.disc.SessionForID(winID)
	if s == nil {
		m.sendErrToGroup(group, "waiting for VS Code window (is it running with the CDP port open?)")
		return
	}
	fpStart := time.Now()
	fpst, err := cdp.Fingerprint(s, m.selectors)
	fpMs := time.Since(fpStart).Milliseconds()
	if err != nil {
		m.sendErrToGroup(group, err.Error())
		return
	}
	if fpst.Err != "" {
		// A missing pane is usually a closed chat view: click the window's
		// title-bar "Toggle Chat" button to re-open it (throttled).
		if fpst.Err == "pane not found" {
			m.autoOpenChat(s, winID)
		}
		m.sendErrToGroup(group, fpst.Err)
		return
	}

	m.snapMu.Lock()
	prev := m.snaps[winID]
	rectChanged := prev == nil || prev.rect != fpst.Rect
	scrollChanged := prev == nil || prev.scroll != fpst.Scroll || !slices.Equal(prev.scrollPath, fpst.ScrollPath) ||
		!slices.Equal(prev.scrollRows, fpst.ScrollRows)
	fpChanged := prev == nil || prev.fp != fpst.FP
	cssFPChanged := prev == nil || prev.cssFP != fpst.CSSFP
	// Nested scrollers (reasoning-trace lists) scroll without any DOM change:
	// while the trace streams, VS Code pins the list's scrollTop to the
	// bottom on every content chunk, and the user can scroll a finished box.
	// Neither changes the content fingerprint, so compare them explicitly.
	nestedChanged := prev == nil || !equalNested(prev.nestedScrolls, fpst.NestedScrolls)
	var prevCSS, prevCSSVer, prevThemeVer string
	if prev != nil {
		prevCSS, prevCSSVer, prevThemeVer = prev.css, prev.cssVer, prev.themeVer
	}
	m.snapMu.Unlock()

	// A client that has not yet rendered this window (just connected, or
	// just switched to it via the picker) needs a full HTML/CSS send even
	// when nothing changed in this window since the last refresh —
	// otherwise its pane would keep showing the previous window until the
	// new one happens to change.
	anyNeedsFull := false
	for _, c := range group {
		if p := c.lastWin.Load(); p == nil || *p != winID {
			anyNeedsFull = true
			break
		}
	}

	if !(rectChanged || scrollChanged || fpChanged || cssFPChanged || nestedChanged || anyNeedsFull) {
		m.log.Debug("mirror: refresh", "window", winID, "totalMs", time.Since(totalStart).Milliseconds(), "fpMs", fpMs, "changed", "-")
		return
	}
	var changedParts []string
	if rectChanged {
		changedParts = append(changedParts, "rect")
	}
	if scrollChanged {
		changedParts = append(changedParts, "scroll")
	}
	if fpChanged {
		changedParts = append(changedParts, "fp")
	}
	if cssFPChanged {
		changedParts = append(changedParts, "css")
	}
	if nestedChanged {
		changedParts = append(changedParts, "nested")
	}
	if anyNeedsFull {
		changedParts = append(changedParts, "full")
	}

	// Full HTML extract only when the content fingerprint changed.
	var html, rootStyle, themeVars, themeBg string
	var newPopup *cdp.PopupState
	var htmlMs int64
	// The nested-scroll state shipped with a message must be measured at the
	// SAME instant as the HTML it accompanies. The fingerprint (taken first,
	// to decide whether to extract) can predate a content chunk that the
	// extract (taken second) already includes; shipping the fingerprint's
	// nestedScrolls with the newer HTML would leave the mirror's nested list
	// (the reasoning-trace / streaming spinner) one chunk behind its own
	// content, clipping the spinner at the box bottom until the next message.
	nestedScrolls := fpst.NestedScrolls
	if fpChanged {
		exStart := time.Now()
		hs, err := cdp.ExtractHTML(s, m.selectors)
		if err != nil {
			m.sendErrToGroup(group, err.Error())
			return
		}
		if hs.Err != "" {
			m.sendErrToGroup(group, hs.Err)
			return
		}
		html, rootStyle, themeVars, themeBg = hs.HTML, hs.RootStyle, hs.ThemeVars, hs.ThemeBg
		if hs.NestedScrolls != nil {
			nestedScrolls = hs.NestedScrolls
		}
		if hs.Popup != nil {
			newPopup = hs.Popup
			// Popups can carry images too (e.g. the attached-image hover
			// preview renders its full-size blob img inside the hover's
			// .context-view); rewrite them just like the pane's.
			newPopup.HTML = m.rewriteImages(s, newPopup.HTML)
		}
		html = m.rewriteImages(s, html)
		htmlMs = time.Since(exStart).Milliseconds()
	} else if prev != nil {
		html, rootStyle, themeVars, themeBg = prev.html, prev.rootStyle, prev.themeVars, prev.themeBg
	}
	themeVer := hashStr(themeVars)
	sendTheme := themeVars != "" && themeVer != prevThemeVer

	// The CSS re-extract (with inlined fonts) is deferred to a follow-up
	// phase (phase 2, below) when the CSS fingerprint changed: opening a
	// context view makes VS Code add an inline <style> element, which flips
	// the CSS fingerprint on exactly the refresh that opens the popup.
	// Extracting (and re-sending ~4MB of) CSS before the popup would make
	// the mirror show the menu only after the live open animation has
	// finished. Until phase 2 lands, the cached copy is authoritative.
	css, cssVer := prevCSS, prevCSSVer
	var cssMs int64

	ns := &snapState{
		html: html, css: css, cssVer: cssVer, cssFP: fpst.CSSFP,
		rootStyle: rootStyle, themeVars: themeVars, themeVer: themeVer, themeBg: themeBg,
		rect: fpst.Rect, scroll: fpst.Scroll, scrollPath: fpst.ScrollPath, scrollRows: fpst.ScrollRows,
		nestedScrolls: nestedScrolls,
		window:        s.Title(), windowID: winID, fp: fpst.FP, inputFocused: fpst.InputFocused,
		cursorChar: fpst.CursorChar, inputBreaks: fpst.InputBreaks,
	}
	if newPopup != nil {
		ns.popupHTML = newPopup.HTML
		ns.popupFP = hashStr(newPopup.HTML)
		ns.popupLeft = newPopup.Left
		ns.popupTop = newPopup.Top
		ns.popupW = newPopup.Width
		ns.popupH = newPopup.Height
	}
	m.snapMu.Lock()
	m.snaps[winID] = ns
	m.snapMu.Unlock()

	base := stateMsg{
		Type: "state", CSSVer: cssVer, RootStyle: rootStyle, ThemeVer: themeVer,
		NestedScrolls: nestedScrolls, InputFocused: fpst.InputFocused,
		CursorChar: fpst.CursorChar, InputBreaks: fpst.InputBreaks,
		Rect: fpst.Rect, Scroll: fpst.Scroll, ScrollPath: fpst.ScrollPath, ScrollRows: fpst.ScrollRows,
		Window: s.Title(), WindowID: winID, Windows: wins,
	}
	if fpChanged {
		base.HTML = html
	}
	// The popup is part of the content fingerprint, so it only changes when
	// fpChanged. Always advertise its position while it is open (the browser
	// keeps its DOM and just re-positions); re-send the HTML only when it
	// actually changed.
	if ns.popupFP != "" {
		base.Popup = &cdp.PopupState{Left: ns.popupLeft, Top: ns.popupTop, Width: ns.popupW, Height: ns.popupH}
		if prev == nil || prev.popupFP != ns.popupFP {
			base.Popup.HTML = ns.popupHTML
		}
	}
	if sendTheme {
		base.ThemeVars = themeVars
		base.ThemeBg = themeBg
	}
	// The popup's anchor depends on each client's OWN last-clicked control
	// (several tabs can mirror this window at once), so resolve it per
	// client on every refresh while a popup is open: one cheap eval per
	// client, only while visible. Marshal a copy of the message per client
	// — never mutate the shared Popup pointer.
	marshalStart := time.Now()
	// Phase 1: the pane HTML and the popup, styled by the CACHED css. This is
	// what makes the popup visible in the mirror, so it must not wait for a
	// CSS re-extract. A client that has not rendered this window yet (just
	// connected, or just switched via the picker) gets the cached CSS here
	// and the fresh extract in phase 2.
	phase1Ms := int64(0)
	if fpChanged || rectChanged || scrollChanged || nestedChanged || anyNeedsFull {
		for _, c := range group {
			msg := base
			if p := c.lastWin.Load(); p == nil || *p != winID {
				// First state of this window for this client: force a full
				// send (HTML + cached CSS + theme) so the pane re-renders
				// even though the window has not changed since the last
				// refresh.
				msg.HTML = html
				msg.CSS = prevCSS
				msg.ThemeVars = themeVars
				msg.ThemeBg = themeBg
				c.lastWin.Store(&winID)
			}
			if ns.popupFP != "" && msg.Popup != nil {
				m.withPopupAnchor(&msg, c, s, fpst.Rect)
			}
			c.sendTo(msg)
		}
		phase1Ms = time.Since(totalStart).Milliseconds()
	}

	// Phase 2: the CSS re-extract (only when the fingerprint changed), sent
	// as a follow-up so phase 1 was not delayed. The follow-up carries the
	// popup position WITHOUT html: a null popup would make the browser drop
	// the menu that phase 1 just rendered. The group is re-resolved because
	// a client may have connected during the extract.
	if cssFPChanged {
		csStart := time.Now()
		if cs, err := cdp.ExtractCSS(s); err == nil {
			css, cssVer = cs.CSS, hashStr(cs.CSS)
			m.snapMu.Lock()
			if cur := m.snaps[winID]; cur == ns {
				ns.css = css
				ns.cssVer = cssVer
			}
			m.snapMu.Unlock()
			// Carry the input state too: CursorChar has no omitempty (0 is a
			// valid caret position), so a follow without it would reset the
			// mirror's caret to the start of the input, and a missing
			// InputFocused would drop the blink class.
			follow := stateMsg{
				Type: "state", CSS: css, CSSVer: cssVer,
				InputFocused: fpst.InputFocused, CursorChar: fpst.CursorChar, InputBreaks: fpst.InputBreaks,
				Rect: fpst.Rect, Scroll: fpst.Scroll, ScrollPath: fpst.ScrollPath, ScrollRows: fpst.ScrollRows,
				Window: s.Title(), WindowID: winID, Windows: wins,
			}
			if ns.popupFP != "" {
				follow.Popup = &cdp.PopupState{Left: ns.popupLeft, Top: ns.popupTop, Width: ns.popupW, Height: ns.popupH}
			}
			for _, c := range m.groupClients(m.disc.Windows())[winID] {
				fm := follow
				if fm.Popup != nil {
					m.withPopupAnchor(&fm, c, s, fpst.Rect)
				}
				c.sendTo(fm)
			}
		} else {
			m.log.Debug("css extract failed", "err", err)
		}
		cssMs = time.Since(csStart).Milliseconds()
	}
	m.log.Debug("mirror: refresh",
		"window", winID,
		"totalMs", time.Since(totalStart).Milliseconds(),
		"fpMs", fpMs, "htmlMs", htmlMs, "cssMs", cssMs,
		"phase1Ms", phase1Ms,
		"marshalMs", time.Since(marshalStart).Milliseconds(),
		"changed", strings.Join(changedParts, ","), "clients", len(group),
		"htmlBytes", len(html), "cssBytes", len(css))
}

// withPopupAnchor resolves the client's last-clicked control to a live rect
// and attaches it as the popup's anchor, mutating the (per-client COPY of
// the) message in place. The mirror applies the live (popup - anchor)
// offset to its own copy of the control, which is size-independent. No-op
// when the message carries no popup or the client has no anchor yet.
func (m *Mirror) withPopupAnchor(msg *stateMsg, c *client, s *cdp.Session, paneRect cdp.PaneRect) {
	if msg.Popup == nil {
		return
	}
	ap := c.lastAnchor.Load()
	if ap == nil {
		return
	}
	ar, ok := cdp.EvalRect(s, m.selectors, *ap)
	if !ok {
		return
	}
	p := *msg.Popup
	p.Anchor = &cdp.PaneRect{
		Left: ar.Left - paneRect.Left, Top: ar.Top - paneRect.Top,
		Width: ar.Width, Height: ar.Height,
	}
	msg.Popup = &p
}

// windowIn reports whether id is in the (title-sorted) window set.
func windowIn(wins []cdp.Window, id string) bool {
	for _, w := range wins {
		if w.ID == id {
			return true
		}
	}
	return false
}

// sendErrToGroup sends an error state message to one group of clients.
// The live window set travels with the error so the browser's picker
// stays populated: a missing pane in the selected window must not hide
// the other windows from the picker.
func (m *Mirror) sendErrToGroup(group []*client, err string) {
	wins := m.disc.Windows()
	for _, c := range group {
		c.sendTo(stateMsg{Type: "state", Err: err, Windows: wins})
	}
}

// chatToggleCooldown bounds how often the mirror clicks the title-bar
// "Toggle Chat" button to re-open a missing chat pane. After a click the
// pane takes a moment to render, and windows without the button (or with a
// hidden title bar) must not be re-probed on every refresh.
const chatToggleCooldown = 5 * time.Second

// autoOpenChat re-opens a missing chat pane by clicking the window's
// title-bar "Toggle Chat" button (a real CDP input event at the button's
// center, like forwarded mirror clicks). Throttled per window.
func (m *Mirror) autoOpenChat(s *cdp.Session, winID string) {
	now := time.Now()
	if since := now.Sub(m.toggleTimes[winID]); since < chatToggleCooldown {
		return
	}
	x, y, ok := cdp.ToggleChatPoint(s)
	if !ok {
		m.toggleTimes[winID] = now
		m.log.Debug("auto-open: Toggle Chat button not found", "window", winID)
		return
	}
	m.toggleTimes[winID] = now
	m.log.Info("auto-open: clicking Toggle Chat", "window", winID, "x", x, "y", y)
	s.Send("Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1,
	})
	s.Send("Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": x, "y": y, "button": "left", "buttons": 0, "clickCount": 1,
	})
}

// handleInput dispatches one browser input frame to the live page the
// sending client is currently mirroring.
func (m *Mirror) handleInput(c *client, raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return
	}
	m.log.Debug("mirror input frame", "type", head.Type)
	if head.Type == "window" {
		var e windowMsg
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		c.selID.Store(&e.ID)
		m.log.Info("mirror: window selected", "id", e.ID)
		m.signalRefresh()
		return
	}
	wins := m.disc.Windows()
	s, winID := m.pickSessionFor(c, wins)
	if s == nil {
		m.log.Debug("mirror input: no session")
		return
	}
	switch head.Type {
	case "mouse":
		var e mouseMsg
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		// Remember the control that was clicked so the next popup can be
		// anchored to it. Only pressed events carry a meaningful anchor;
		// nil (e.g. clicks inside the popup itself) leaves the previous one.
		if e.Kind == "pressed" && e.AnchorPath != nil {
			c.lastAnchor.Store(&e.AnchorPath)
		}
		m.forwardMouse(s, winID, e)
	case "key":
		var e keyMsg
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		m.forwardKey(s, e)
	}
}

// forwardMouse maps a mirror mouse event onto the live page and dispatches the
// matching CDP input event. Events carrying an element identity (DOM-order
// index + relative position) are resolved by that identity so they land on the
// correct live element even when the mirror's rendering is not pixel-identical
// (the responsive layout is a different size than the live pane); events
// without one fall back to the pane-relative offset.
func (m *Mirror) forwardMouse(s *cdp.Session, winID string, e mouseMsg) {
	// m.selectors is immutable (set in the constructor); only the snaps
	// read below needs the lock.
	selectors := m.selectors
	m.snapMu.Lock()
	var rect cdp.PaneRect
	if sn := m.snaps[winID]; sn != nil {
		rect = sn.rect
	}
	m.snapMu.Unlock()

	x, y := rect.Left+e.X, rect.Top+e.Y
	placed := false
	// The browser only sends char for a caret position (>= 0), so a
	// non-nil Char already implies a valid character index.
	if e.Char != nil {
		// Chat-input click: re-seat the caret character on the live layout.
		// The element-identity mapping cannot reach the mirror's wrapped
		// lines (the live line is shorter), so the character index is the
		// size-independent identity.
		ecStart := time.Now()
		if px, py, ok := cdp.EvalCharPoint(s, selectors, *e.Char); ok {
			x, y = px, py
			placed = true
		}
		m.log.Debug("forwardMouse: evalCharPoint", "char", *e.Char, "ok", placed, "ms", time.Since(ecStart).Milliseconds())
	}
	if !placed && e.Path != nil {
		ecStart := time.Now()
		if px, py, ok := cdp.EvalClickPoint(s, selectors, e.Path, e.RelX, e.RelY); ok {
			x, y = px, py
		}
		m.log.Debug("forwardMouse: evalClickPoint", "ms", time.Since(ecStart).Milliseconds())
	}
	// Wheels only need to land somewhere inside the pane (the exact element
	// under the cursor is irrelevant); clicks need the precise spot. A wheel
	// whose path resolved off-screen (a virtualized row scrolled out of the
	// live viewport, or a stale pane-relative fallback) would be dropped by
	// the browser, so clamp it to the pane center instead.
	if e.Kind == "wheel" && rect.Width > 0 && rect.Height > 0 {
		if x < rect.Left || x > rect.Left+rect.Width || y < rect.Top || y > rect.Top+rect.Height {
			x = rect.Left + rect.Width/2
			y = rect.Top + rect.Height/2
			m.log.Debug("mirror forwardMouse: wheel point outside pane, clamped to center", "x", x, "y", y)
		}
	}
	m.log.Debug("mirror forwardMouse", "kind", e.Kind, "path", e.Path, "relX", e.RelX, "relY", e.RelY, "x", x, "y", y)

	params := map[string]any{"x": x, "y": y}
	var method string
	switch e.Kind {
	case "pressed":
		method = "Input.dispatchMouseEvent"
		params["type"] = "mousePressed"
		params["button"] = orDefault(e.Button, "left")
		params["buttons"] = e.Buttons
		params["clickCount"] = e.Clicks
	case "released":
		method = "Input.dispatchMouseEvent"
		params["type"] = "mouseReleased"
		params["button"] = orDefault(e.Button, "left")
		params["buttons"] = e.Buttons
		params["clickCount"] = e.Clicks
	case "moved":
		method = "Input.dispatchMouseEvent"
		params["type"] = "mouseMoved"
		params["buttons"] = e.Buttons
	case "wheel":
		method = "Input.dispatchMouseEvent"
		params["type"] = "mouseWheel"
		params["deltaX"] = e.DeltaX
		params["deltaY"] = e.DeltaY
	default:
		return
	}
	s.Send(method, params)
}

// forwardKey dispatches a keyboard event to the live page. For a keydown
// that carries a printable character, a "char" event is sent as well so the
// focused editor (e.g. Monaco) receives the text.
func (m *Mirror) forwardKey(s *cdp.Session, e keyMsg) {
	m.log.Debug("mirror forwardKey", "kind", e.Kind, "key", e.Key, "code", e.Code, "text", e.Text)
	switch e.Kind {
	case "down":
		s.Send("Input.dispatchKeyEvent", map[string]any{
			"type": "rawKeyDown", "key": e.Key, "code": e.Code,
			"windowsVirtualKeyCode": e.KeyCode, "nativeVirtualKeyCode": e.KeyCode,
			"modifiers": e.Modifiers,
		})
		if e.Text != "" {
			s.Send("Input.dispatchKeyEvent", map[string]any{
				"type": "char", "text": e.Text, "modifiers": e.Modifiers,
			})
		}
	case "up":
		s.Send("Input.dispatchKeyEvent", map[string]any{
			"type": "keyUp", "key": e.Key, "code": e.Code,
			"windowsVirtualKeyCode": e.KeyCode, "nativeVirtualKeyCode": e.KeyCode,
			"modifiers": e.Modifiers,
		})
	case "insert":
		if e.Text != "" {
			s.Send("Input.insertText", map[string]any{"text": e.Text})
		}
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func equalNested(a, b []cdp.NestedScroll) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Top != b[i].Top || a[i].ScrollH != b[i].ScrollH || a[i].ClientH != b[i].ClientH ||
			!slices.Equal(a[i].Path, b[i].Path) {
			return false
		}
	}
	return true
}

// hashStr returns a short hex digest used to detect content/CSS changes.
func hashStr(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8])
}
