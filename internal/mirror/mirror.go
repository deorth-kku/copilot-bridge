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
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"vscode-load-llama/internal/cdp"
)

// stateMsg is the server -> browser message carrying a pane snapshot.
// HTML and CSS are only present when they changed (the browser keeps its
// last copy otherwise); rect/scroll/rootStyle/cssVersion are always sent.
type stateMsg struct {
	Type      string         `json:"type"` // "state"
	HTML      string         `json:"html,omitempty"`
	CSS       string         `json:"css,omitempty"`
	CSSVer    string         `json:"cssVersion,omitempty"`
	RootStyle string         `json:"rootStyle,omitempty"`
	ThemeVars string         `json:"themeVars,omitempty"`
	ThemeVer  string         `json:"themeVer,omitempty"`
	Rect      cdp.PaneRect   `json:"rect"`
	Scroll    cdp.PaneScroll `json:"scroll"`
	Window    string         `json:"window,omitempty"`
	Err       string         `json:"err,omitempty"`
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

// Mirror owns the web server, the set of connected browser tabs, and the
// publish loop that keeps the mirrored pane in sync with the live page.
type Mirror struct {
	disc      *cdp.Discovery
	log       *slog.Logger
	addr      string
	selectors []string
	window    string
	poll      time.Duration

	upgrader websocket.Upgrader

	clientsMu sync.Mutex
	clients   map[*client]struct{}

	// snap is the latest extracted pane snapshot. The pointer is swapped
	// under snapMu; the (potentially large) html/css strings are shared by
	// reference across snapshots, so a swap is cheap.
	snapMu sync.Mutex
	snap   *snapState
}

// snapState is one extracted pane snapshot.
type snapState struct {
	html      string
	css       string
	cssVer    string
	cssFP     string
	rootStyle string
	themeVars string
	themeVer  string
	window    string
	fp        string
	rect      cdp.PaneRect
	scroll    cdp.PaneScroll
	err       string
}

// client is one connected browser tab.
type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
}

// New creates a Mirror. addr is the web bind address; selectors are the
// candidate pane root selectors (tried in order); window is a title
// substring used to pick the VS Code window (empty = first window).
func New(disc *cdp.Discovery, log *slog.Logger, addr string, selectors []string, window string) *Mirror {
	return &Mirror{
		disc:      disc,
		log:       log,
		addr:      addr,
		selectors: selectors,
		window:    window,
		poll:      150 * time.Millisecond,
		upgrader: websocket.Upgrader{
			// Local-only tool; accept any origin (127.0.0.1 / localhost).
			CheckOrigin: func(*http.Request) bool { return true },
		},
		clients: make(map[*client]struct{}),
	}
}

// Run starts the web server and the publish loop. It blocks until ctx is
// cancelled, then shuts everything down.
func (m *Mirror) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handlePage)
	mux.HandleFunc("/ws", m.handleWS)
	srv := &http.Server{Addr: m.addr, Handler: mux}

	errCh := make(chan error, 1)
	go func() {
		m.log.Info("mirror web server listening", "addr", m.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go m.publishLoop(ctx)

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

func (m *Mirror) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 64), done: make(chan struct{})}
	m.addClient(c)
	defer m.removeClient(c)

	// Render immediately with whatever we already have.
	m.sendFullState(c)
	go m.writePump(c)

	conn.SetReadLimit(1 << 20)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		m.handleInput(raw)
	}
}

func (m *Mirror) writePump(c *client) {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.send:
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		}
	}
}

func (m *Mirror) addClient(c *client) {
	m.clientsMu.Lock()
	m.clients[c] = struct{}{}
	m.clientsMu.Unlock()
}

func (m *Mirror) removeClient(c *client) {
	m.clientsMu.Lock()
	if _, ok := m.clients[c]; ok {
		delete(m.clients, c)
		close(c.done)
	}
	m.clientsMu.Unlock()
	c.conn.Close()
}

func (m *Mirror) closeClients() {
	m.clientsMu.Lock()
	cs := make([]*client, 0, len(m.clients))
	for c := range m.clients {
		cs = append(cs, c)
	}
	m.clientsMu.Unlock()
	for _, c := range cs {
		m.removeClient(c)
	}
}

func (m *Mirror) broadcast(b []byte) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	for c := range m.clients {
		select {
		case c.send <- b:
		default:
			// Slow client: drop the frame rather than stall the publisher.
		}
	}
}

func (m *Mirror) sendFullState(c *client) {
	m.snapMu.Lock()
	var msg stateMsg
	if s := m.snap; s != nil {
		msg = stateMsg{
			Type: "state", HTML: s.html, CSS: s.css, CSSVer: s.cssVer,
			RootStyle: s.rootStyle, ThemeVars: s.themeVars, ThemeVer: s.themeVer,
			Rect: s.rect, Scroll: s.scroll,
			Window: s.window, Err: s.err,
		}
	} else {
		msg = stateMsg{Type: "state", Err: "waiting for VS Code window"}
	}
	m.snapMu.Unlock()
	if b, err := json.Marshal(msg); err == nil {
		select {
		case c.send <- b:
		default:
		}
	}
}

// publishLoop probes the pane on a timer and broadcasts changes.
func (m *Mirror) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refresh()
		}
	}
}

// refresh probes the pane (cheap fingerprint) and, when something changed,
// does a full extract and broadcasts a state message.
func (m *Mirror) refresh() {
	s := m.disc.SessionFor(m.window)
	if s == nil {
		m.publishError("waiting for VS Code window (is it running with the CDP port open?)")
		return
	}
	fpst, err := cdp.Fingerprint(s, m.selectors)
	if err != nil {
		m.publishError(err.Error())
		return
	}
	if fpst.Err != "" {
		m.publishError(fpst.Err)
		return
	}

	m.snapMu.Lock()
	prev := m.snap
	rectChanged := prev == nil || prev.rect != fpst.Rect
	scrollChanged := prev == nil || prev.scroll != fpst.Scroll
	fpChanged := prev == nil || prev.fp != fpst.FP
	cssFPChanged := prev == nil || prev.cssFP != fpst.CSSFP
	var prevCSS, prevCSSVer, prevThemeVer string
	if prev != nil {
		prevCSS, prevCSSVer, prevThemeVer = prev.css, prev.cssVer, prev.themeVer
	}
	m.snapMu.Unlock()

	if !(rectChanged || scrollChanged || fpChanged || cssFPChanged) {
		return
	}

	// Full HTML extract only when the content fingerprint changed.
	var html, rootStyle, themeVars string
	if fpChanged {
		hs, err := cdp.ExtractHTML(s, m.selectors)
		if err != nil {
			m.publishError(err.Error())
			return
		}
		if hs.Err != "" {
			m.publishError(hs.Err)
			return
		}
		html, rootStyle, themeVars = hs.HTML, hs.RootStyle, hs.ThemeVars
	} else if prev != nil {
		html, rootStyle, themeVars = prev.html, prev.rootStyle, prev.themeVars
	}
	themeVer := hashStr(themeVars)
	sendTheme := themeVars != "" && themeVer != prevThemeVer

	// Full CSS extract (with inlined fonts) only when the CSS fingerprint
	// changed; otherwise reuse the cached copy.
	css, cssVer, sendCSS := prevCSS, prevCSSVer, false
	if cssFPChanged {
		if cs, err := cdp.ExtractCSS(s); err == nil {
			css, cssVer, sendCSS = cs.CSS, hashStr(cs.CSS), true
		} else {
			m.log.Debug("css extract failed", "err", err)
		}
	}

	ns := &snapState{
		html: html, css: css, cssVer: cssVer, cssFP: fpst.CSSFP,
		rootStyle: rootStyle, themeVars: themeVars, themeVer: themeVer,
		rect: fpst.Rect, scroll: fpst.Scroll,
		window: s.Title(), fp: fpst.FP,
	}
	m.snapMu.Lock()
	m.snap = ns
	m.snapMu.Unlock()

	msg := stateMsg{
		Type: "state", CSSVer: cssVer, RootStyle: rootStyle, ThemeVer: themeVer,
		Rect: fpst.Rect, Scroll: fpst.Scroll, Window: s.Title(),
	}
	if fpChanged {
		msg.HTML = html
	}
	if sendCSS {
		msg.CSS = css
	}
	if sendTheme {
		msg.ThemeVars = themeVars
	}
	if b, e := json.Marshal(msg); e == nil {
		m.broadcast(b)
	}
}

func (m *Mirror) publishError(err string) {
	m.snapMu.Lock()
	if m.snap == nil {
		m.snap = &snapState{err: err}
	} else {
		m.snap.err = err
	}
	m.snapMu.Unlock()
	if b, e := json.Marshal(stateMsg{Type: "state", Err: err}); e == nil {
		m.broadcast(b)
	}
}

// handleInput dispatches one browser input frame to the live page.
func (m *Mirror) handleInput(raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return
	}
	m.log.Debug("mirror input frame", "type", head.Type)
	s := m.disc.SessionFor(m.window)
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
		m.forwardMouse(s, e)
	case "key":
		var e keyMsg
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		m.forwardKey(s, e)
	}
}

// forwardMouse maps a mirror mouse event onto the live page and dispatches the
// matching CDP input event. Clicks (pressed/released/wheel) are resolved by
// element identity (DOM-order index + relative position) so they land on the
// correct live element even when the mirror's rendering is not pixel-identical;
// hover (moved) uses the cheap pane-relative offset.
func (m *Mirror) forwardMouse(s *cdp.Session, e mouseMsg) {
	m.snapMu.Lock()
	var rect cdp.PaneRect
	if m.snap != nil {
		rect = m.snap.rect
	}
	selectors := m.selectors
	m.snapMu.Unlock()

	x, y := rect.Left+e.X, rect.Top+e.Y
	if e.Kind != "moved" {
		if px, py, ok := cdp.EvalClickPoint(s, selectors, e.Path, e.RelX, e.RelY); ok {
			x, y = px, py
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

// hashStr returns a short hex digest used to detect content/CSS changes.
func hashStr(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8])
}
