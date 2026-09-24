package mirror

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
)

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
		// The armed state of the power-off toggle likewise arrives on
		// connect (and on every change).
		if m.planner != nil {
			c.sendTo(shutdownMsg{Type: "shutdown", Armed: m.planner.Armed()})
		}
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
