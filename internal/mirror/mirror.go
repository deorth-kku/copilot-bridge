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
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go4org/hashtriemap"
	"github.com/gorilla/websocket"

	"vscode-load-llama/internal/cdp"
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
	ThemeBg string         `json:"themeBg,omitempty"`
	Rect    cdp.PaneRect   `json:"rect"`
	Scroll  cdp.PaneScroll `json:"scroll"`
	// ScrollPath identifies the measured scroll container as a DOM path from
	// the pane root (no omitempty: an empty path means "the root itself").
	ScrollPath []int `json:"scrollPath"`
	// ScrollRows is each rendered row's [offsetTop, offsetHeight] pair in
	// full-content coordinates; the browser uses it to align its viewport
	// with the live viewport inside the rendered row window.
	ScrollRows []float64 `json:"scrollRows,omitempty"`
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
// publish loop that keeps the mirrored pane in sync with the live page.
type Mirror struct {
	disc      *cdp.Discovery
	log       *slog.Logger
	addr      string
	selectors []string
	window    string
	// fallback bounds how often the publish loop probes even when the page
	// sent no wake event. It covers changes the injected observer cannot
	// see (CSSOM-only edits, dropped wakes) and re-resolves the session
	// after a window switch or reload.
	fallback time.Duration

	upgrader websocket.Upgrader

	// clients is the set of connected browser tabs. It is a lock-free
	// hash-trie map, so add/remove/broadcast never take a shared lock.
	clients hashtriemap.HashTrieMap[*client, struct{}]

	// imgs caches fetched image resources by the hash of their source URL.
	// A lock-free hash-trie map: reads from the HTTP handlers and writes
	// from the single publish-loop goroutine never block.
	imgs hashtriemap.HashTrieMap[string, imgEntry]

	// snap is the latest extracted pane snapshot. The pointer is swapped
	// under snapMu; the (potentially large) html/css strings are shared by
	// reference across snapshots, so a swap is cheap.
	snapMu sync.Mutex
	snap   *snapState

	// selectedID is the CDP target id the user picked in the status-bar
	// window picker (nil = follow the default). Written by the WS handler,
	// read by the publish loop — an atomic pointer keeps it lock-free.
	selectedID atomic.Pointer[string]

	// lastAnchor is the DOM path (from the pane root) of the control the
	// user last clicked (a pressed mouse event carrying an anchorPath). The
	// publish loop resolves it to a live rect to anchor the open popup's
	// position. Written by the WS handler, read by the publish loop.
	lastAnchor atomic.Pointer[[]int]
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
	// popupHTML/popupFP capture the visible context view (popup menu) that
	// renders outside the pane root; popupFP == "" means no popup is open.
	popupHTML string
	popupFP   string
	popupLeft float64
	popupTop  float64
	popupW    float64
	popupH    float64
	// popupAnchor is the live rect (pane-relative) of the control that
	// opened the popup; the mirror offsets the popup from its own copy of
	// that control instead of copying the live pane-relative position.
	popupAnchor *cdp.PaneRect
	err         string
}

// client is one connected browser tab.
type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
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
// substring used to pick the VS Code window (empty = first window).
func New(disc *cdp.Discovery, log *slog.Logger, addr string, selectors []string, window string) *Mirror {
	return &Mirror{
		disc:      disc,
		log:       log,
		addr:      addr,
		selectors: selectors,
		window:    window,
		fallback:  1 * time.Second,
		upgrader: websocket.Upgrader{
			// Local-only tool; accept any origin (127.0.0.1 / localhost).
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Run starts the web server and the publish loop. It blocks until ctx is
// cancelled, then shuts everything down.
func (m *Mirror) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handlePage)
	mux.HandleFunc("/ws", m.handleWS)
	mux.HandleFunc("/img/", m.handleImage)
	srv := &http.Server{Addr: m.addr, Handler: mux}

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

// rewriteImages replaces blob:/vscode-file: image srcs in the extracted pane
// HTML with /img/<hash> URLs. Each source URL is fetched from the live page
// once (via CDP) and cached; on fetch failure the original src is left
// untouched (the mirror shows a broken image for that one, as before).
func (m *Mirror) rewriteImages(s *cdp.Session, html string) string {
	for _, mth := range imgSrcRe.FindAllStringSubmatch(html, -1) {
		u := mth[1]
		key := hashStr(u)
		if _, ok := m.imgs.Load(key); !ok {
			m.log.Debug("image: fetch attempt", "url", u, "key", key)
			data, ctype, via, err := cdp.FetchImage(s, u)
			if err != nil {
				m.log.Warn("image fetch failed", "url", u, "err", err)
				continue
			}
			if ctype == "" {
				ctype = guessImageType(data)
			}
			m.log.Debug("image: fetched", "key", key, "via", via, "bytes", len(data), "type", ctype)
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
	m.clients.Store(c, struct{}{})
}

func (m *Mirror) removeClient(c *client) {
	// LoadAndDelete is atomic: only the first remover wins, so c.done is
	// closed exactly once even if two callers race.
	if _, ok := m.clients.LoadAndDelete(c); ok {
		close(c.done)
	}
	c.conn.Close()
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

func (m *Mirror) broadcast(b []byte) {
	m.clients.All()(func(c *client, _ struct{}) bool {
		select {
		case c.send <- b:
		default:
			// Slow client: drop the frame rather than stall the publisher.
		}
		return true
	})
}

func (m *Mirror) sendFullState(c *client) {
	m.snapMu.Lock()
	var msg stateMsg
	if s := m.snap; s != nil {
		msg = stateMsg{
			Type: "state", HTML: s.html, CSS: s.css, CSSVer: s.cssVer,
			RootStyle: s.rootStyle, ThemeVars: s.themeVars, ThemeVer: s.themeVer,
			ThemeBg: s.themeBg,
			Rect:    s.rect, Scroll: s.scroll, ScrollPath: s.scrollPath,
			Window: s.window, WindowID: s.windowID, Err: s.err,
		}
		if s.popupFP != "" {
			msg.Popup = &cdp.PopupState{HTML: s.popupHTML, Left: s.popupLeft, Top: s.popupTop, Width: s.popupW, Height: s.popupH, Anchor: s.popupAnchor}
		}
	} else {
		msg = stateMsg{Type: "state", Err: "waiting for VS Code window"}
	}
	m.snapMu.Unlock()
	msg.Windows = m.disc.Windows()
	if b, err := json.Marshal(msg); err == nil {
		select {
		case c.send <- b:
		default:
		}
	}
}

// publishLoop is event-driven: the page's injected observer (cdp.
// MirrorInjectJS) pushes a wake through the session's CDP binding when the
// DOM, scroll position, or window size changes, and the loop refreshes on
// each wake. A slow fallback ticker covers what the observer cannot see
// (CSSOM-only edits, dropped wakes) and re-picks the session after a
// window switch or reload.
func (m *Mirror) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(m.fallback)
	defer ticker.Stop()
	for {
		s, _ := m.pickSession(m.disc.Windows())
		var wake <-chan struct{}
		if s != nil {
			wake = s.MirrorWake()
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
			m.log.Debug("mirror: refresh (wake)")
			m.refresh()
		case <-ticker.C:
			m.log.Debug("mirror: refresh (fallback)")
			m.refresh()
		}
	}
}

// pickSession resolves which window to mirror. Priority:
//  1. the user's explicit status-bar picker selection (selectedID);
//  2. the -window title filter (startup flag);
//  3. the first window in stable title-sorted order.
//
// The stable default (instead of SessionFor's arbitrary map order) is what
// stops the mirror flickering between windows when several are open.
func (m *Mirror) pickSession(wins []cdp.Window) (*cdp.Session, string) {
	if len(wins) == 0 {
		return nil, ""
	}
	if p := m.selectedID.Load(); p != nil {
		sel := *p
		for _, w := range wins {
			if w.ID == sel {
				return m.disc.SessionForID(sel), w.ID
			}
		}
	}
	if m.window != "" {
		for _, w := range wins {
			if strings.Contains(w.Title, m.window) {
				return m.disc.SessionForID(w.ID), w.ID
			}
		}
	}
	return m.disc.SessionForID(wins[0].ID), wins[0].ID
}

// refresh probes the pane (cheap fingerprint) and, when something changed,
// does a full extract and broadcasts a state message.
func (m *Mirror) refresh() {
	wins := m.disc.Windows()
	s, winID := m.pickSession(wins)
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
	scrollChanged := prev == nil || prev.scroll != fpst.Scroll || !equalInts(prev.scrollPath, fpst.ScrollPath) ||
		!equalFloats(prev.scrollRows, fpst.ScrollRows)
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
	var html, rootStyle, themeVars, themeBg string
	var newPopup *cdp.PopupState
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
		html, rootStyle, themeVars, themeBg = hs.HTML, hs.RootStyle, hs.ThemeVars, hs.ThemeBg
		if hs.Popup != nil {
			newPopup = hs.Popup
		}
		html = m.rewriteImages(s, html)
	} else if prev != nil {
		html, rootStyle, themeVars, themeBg = prev.html, prev.rootStyle, prev.themeVars, prev.themeBg
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
		rootStyle: rootStyle, themeVars: themeVars, themeVer: themeVer, themeBg: themeBg,
		rect: fpst.Rect, scroll: fpst.Scroll, scrollPath: fpst.ScrollPath, scrollRows: fpst.ScrollRows,
		window: s.Title(), windowID: winID, fp: fpst.FP,
	}
	if newPopup != nil {
		ns.popupHTML = newPopup.HTML
		ns.popupFP = hashStr(newPopup.HTML)
		ns.popupLeft = newPopup.Left
		ns.popupTop = newPopup.Top
		ns.popupW = newPopup.Width
		ns.popupH = newPopup.Height
	}
	// Resolve the popup's anchor (the last clicked control) to a live rect
	// on every refresh while a popup is open: one cheap eval, only while
	// visible. The mirror applies the live (popup - anchor) offset to its
	// own copy of the control, which is size-independent.
	if ns.popupFP != "" {
		if p := m.lastAnchor.Load(); p != nil {
			ap := *p
			if ar, ok := cdp.EvalRect(s, m.selectors, ap); ok {
				ns.popupAnchor = &cdp.PaneRect{
					Left: ar.Left - fpst.Rect.Left, Top: ar.Top - fpst.Rect.Top,
					Width: ar.Width, Height: ar.Height,
				}
			}
		}
	}
	m.snapMu.Lock()
	m.snap = ns
	m.snapMu.Unlock()

	msg := stateMsg{
		Type: "state", CSSVer: cssVer, RootStyle: rootStyle, ThemeVer: themeVer,
		Rect: fpst.Rect, Scroll: fpst.Scroll, ScrollPath: fpst.ScrollPath, ScrollRows: fpst.ScrollRows,
		Window: s.Title(), WindowID: winID, Windows: wins,
	}
	if fpChanged {
		msg.HTML = html
	}
	// The popup is part of the content fingerprint, so it only changes when
	// fpChanged. Always advertise its position while it is open (the browser
	// keeps its DOM and just re-positions); re-send the HTML only when it
	// actually changed.
	if ns.popupFP != "" {
		msg.Popup = &cdp.PopupState{Left: ns.popupLeft, Top: ns.popupTop, Width: ns.popupW, Height: ns.popupH, Anchor: ns.popupAnchor}
		if prev == nil || prev.popupFP != ns.popupFP {
			msg.Popup.HTML = ns.popupHTML
		}
	}
	if sendCSS {
		msg.CSS = css
	}
	if sendTheme {
		msg.ThemeVars = themeVars
		msg.ThemeBg = themeBg
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
	if head.Type == "window" {
		var e windowMsg
		if err := json.Unmarshal(raw, &e); err != nil {
			return
		}
		m.selectedID.Store(&e.ID)
		m.log.Info("mirror: window selected", "id", e.ID)
		return
	}
	s, _ := m.pickSession(m.disc.Windows())
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
			m.lastAnchor.Store(&e.AnchorPath)
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
// matching CDP input event. Events carrying an element identity (DOM-order
// index + relative position) are resolved by that identity so they land on the
// correct live element even when the mirror's rendering is not pixel-identical
// (the responsive layout is a different size than the live pane); events
// without one fall back to the pane-relative offset.
func (m *Mirror) forwardMouse(s *cdp.Session, e mouseMsg) {
	m.snapMu.Lock()
	var rect cdp.PaneRect
	if m.snap != nil {
		rect = m.snap.rect
	}
	selectors := m.selectors
	m.snapMu.Unlock()

	x, y := rect.Left+e.X, rect.Top+e.Y
	if e.Path != nil {
		if px, py, ok := cdp.EvalClickPoint(s, selectors, e.Path, e.RelX, e.RelY); ok {
			x, y = px, py
		}
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

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
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
