package mirror

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
)

// captureCDP stands in for one live window's CDP page socket: it records
// the parameters of every Input.dispatchMouseEvent and answers
// Runtime.evaluate with a fixed "result" JSON (the point probes' outcome).
type captureCDP struct {
	mu         sync.Mutex
	url        string
	inputs     []map[string]any
	evalResult string
}

func newCaptureCDP(t *testing.T, evalResult string) *captureCDP {
	t.Helper()
	m := &captureCDP{evalResult: evalResult}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     *int           `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				continue
			}
			if req.Method == "Input.dispatchMouseEvent" {
				m.mu.Lock()
				m.inputs = append(m.inputs, req.Params)
				m.mu.Unlock()
				continue
			}
			if req.ID == nil {
				continue
			}
			result := `{}`
			if req.Method == "Runtime.evaluate" {
				result = m.evalResult
			}
			resp := `{"id":` + strconv.Itoa(*req.ID) + `,"result":` + result + `}`
			if err := conn.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	m.url = "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/devtools/page/test"
	return m
}

// session dials a real Session at the mock and waits until it is connected
// (a Call fails with "not connected" until the socket is up).
func (m *captureCDP) session(t *testing.T) *cdp.Session {
	t.Helper()
	s := cdp.NewSession("test", "Test Window", m.url, make(chan cdp.Event, 1), discardLog(), 50)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(s.Stop)
	go s.Run(ctx)
	waitFor(t, "session connect", func() bool {
		_, err := s.Call("Runtime.evaluate", map[string]any{"expression": "0"})
		return err == nil
	})
	return s
}

func (m *captureCDP) inputCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}

func (m *captureCDP) lastInput() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inputs) == 0 {
		return nil
	}
	return m.inputs[len(m.inputs)-1]
}

func testMirror(rect cdp.PaneRect) *Mirror {
	snaps := map[string]*snapState{}
	if rect.Width > 0 && rect.Height > 0 {
		snaps["win"] = &snapState{rect: rect}
	}
	return &Mirror{log: discardLog(), selectors: []string{".pane"}, snaps: snaps}
}

// Without a live pane snapshot the pane-relative fallback degenerates to
// the mirror's own coordinates interpreted as LIVE WINDOW coordinates: a
// click near the mirror pane's top-right would land on the live window's
// title-bar close button. Such events must be dropped (all kinds).
func TestForwardMouseNoRectDropped(t *testing.T) {
	m := testMirror(cdp.PaneRect{})
	c := newCaptureCDP(t, `{"result":{"type":"object","value":{"x":1,"y":2}}}`)
	s := c.session(t)

	// A click at the mirror pane's top-right corner (the close-button spot).
	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "pressed", X: 1400, Y: 10, Button: "left", Buttons: 1, Clicks: 1})
	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "moved", X: 1400, Y: 10})
	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "wheel", X: 700, Y: 300, DeltaY: -100})

	// forwardMouse is synchronous; give the mock a moment to read the
	// socket, then assert nothing was dispatched.
	time.Sleep(100 * time.Millisecond)
	if n := c.inputCount(); n != 0 {
		t.Fatalf("expected no dispatch without a live pane rect, got %d: %+v", n, c.inputs)
	}
}

// With a live pane rect, a click whose DOM path resolves in live is
// dispatched at the resolved point (element-identity mapping).
func TestForwardMouseResolvedPath(t *testing.T) {
	m := testMirror(cdp.PaneRect{Left: 100, Top: 50, Width: 800, Height: 600})
	c := newCaptureCDP(t, `{"result":{"type":"object","value":{"x":123.5,"y":456.5}}}`)
	s := c.session(t)

	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "pressed", X: 200, Y: 300, Path: []int{1, 2}, RelX: 0.5, RelY: 0.5, Button: "left", Buttons: 1, Clicks: 1})

	waitFor(t, "input dispatched", func() bool { return c.inputCount() == 1 })
	in := c.lastInput()
	if in["x"] != 123.5 || in["y"] != 456.5 {
		t.Fatalf("expected the resolved point (123.5, 456.5), got %+v", in)
	}
}

// A click whose DOM path no longer resolves in live (the page expression
// returns null: stale path, a closed popup, a vanished pane) must NOT fall
// back to the pane-relative guess — that lands on whatever live element now
// sits at that spot.
func TestForwardMouseUnresolvedClickDropped(t *testing.T) {
	m := testMirror(cdp.PaneRect{Left: 100, Top: 50, Width: 800, Height: 600})
	c := newCaptureCDP(t, `{"result":{"type":"null"}}`)
	s := c.session(t)

	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "pressed", X: 200, Y: 300, Path: []int{1, 2}, Button: "left", Buttons: 1, Clicks: 1})
	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "released", X: 200, Y: 300, Path: []int{1, 2}, Button: "left", Buttons: 1, Clicks: 1})

	time.Sleep(100 * time.Millisecond)
	if n := c.inputCount(); n != 0 {
		t.Fatalf("expected no dispatch for an unresolved click path, got %d: %+v", n, c.inputs)
	}
}

// Hover (and wheel) keep the pane-relative fallback when the path does not
// resolve: a hover is harmless, and a wheel only needs to land inside the
// pane.
func TestForwardMouseUnresolvedHoverKept(t *testing.T) {
	m := testMirror(cdp.PaneRect{Left: 100, Top: 50, Width: 800, Height: 600})
	c := newCaptureCDP(t, `{"result":{"type":"null"}}`)
	s := c.session(t)

	m.forwardMouse(s, "win", mouseMsg{Type: "mouse", Kind: "moved", X: 200, Y: 300, Path: []int{1, 2}})

	waitFor(t, "hover dispatched", func() bool { return c.inputCount() == 1 })
	in := c.lastInput()
	// The pane-relative fallback: rect.Left+200, rect.Top+300.
	if in["x"] != 300.0 || in["y"] != 350.0 {
		t.Fatalf("expected the pane-relative fallback (300, 350), got %+v", in)
	}
}
