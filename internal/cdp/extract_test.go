package cdp

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// evalMockCDP stands in for one live window's CDP page socket:
// Runtime.evaluate is answered with a fixed "result" member (the
// Runtime.evaluate response wraps a RemoteObject:
// {"result":{"type":...,"value":...}}), every other command with an
// empty object.
type evalMockCDP struct {
	mu         sync.Mutex
	url        string
	evalResult string
}

func newEvalMockCDP(t *testing.T, evalResult string) *evalMockCDP {
	t.Helper()
	m := &evalMockCDP{evalResult: evalResult}
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
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &req); err != nil || req.ID == nil {
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
func (m *evalMockCDP) session(t *testing.T) *Session {
	t.Helper()
	s := NewSession("test", "Test Window", m.url, make(chan Event, 1), discardLog, defaultDebounceMs)
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

// A page expression that returns null (the path did not resolve, the pane
// root is gone, no visible context view) must surface as ok=false. Decoding
// null into the point struct is a silent no-op and would otherwise yield
// (0,0) — a click dispatched at the live window's top-left corner (the
// menu bar).
func TestEvalClickPointNullResult(t *testing.T) {
	m := newEvalMockCDP(t, `{"result":{"type":"null"}}`)
	s := m.session(t)
	if x, y, ok := EvalClickPoint(s, []string{".pane"}, []int{1, 2}, 0.5, 0.5); ok {
		t.Fatalf("expected ok=false for a null result, got (%v, %v)", x, y)
	}
}

func TestEvalCharPointNullResult(t *testing.T) {
	m := newEvalMockCDP(t, `{"result":{"type":"null"}}`)
	s := m.session(t)
	if x, y, ok := EvalCharPoint(s, []string{".pane"}, 3); ok {
		t.Fatalf("expected ok=false for a null result, got (%v, %v)", x, y)
	}
}

// A resolved point is passed through untouched.
func TestEvalClickPointValue(t *testing.T) {
	m := newEvalMockCDP(t, `{"result":{"type":"object","value":{"x":11.5,"y":22.5}}}`)
	s := m.session(t)
	x, y, ok := EvalClickPoint(s, []string{".pane"}, []int{1, 2}, 0.5, 0.5)
	if !ok || x != 11.5 || y != 22.5 {
		t.Fatalf("expected (11.5, 22.5) ok=true, got (%v, %v) ok=%v", x, y, ok)
	}
}
