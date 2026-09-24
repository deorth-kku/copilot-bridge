package mirror

import (
	"encoding/json/v2"
	"time"

	"copilot-bridge/internal/cdp"
)

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
