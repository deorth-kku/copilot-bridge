package cdp

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startHoldWS starts a WebSocket server that accepts connections and
// holds them open (a stand-in for a live workbench page). Returns the
// host:port of the server.
func startHoldWS(t *testing.T) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// mockCDPServer stands in for the VS Code CDP HTTP endpoint. The
// "up" flag simulates VS Code running vs. fully exited; the target list
// simulates windows opening/closing.
type mockCDPServer struct {
	mu     sync.Mutex
	up     bool
	wsHost string
	tgs    []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
		WSURL string `json:"webSocketDebuggerUrl"`
	}
}

func (m *mockCDPServer) set(up bool, tgs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.up = up
	m.tgs = nil
	for _, id := range tgs {
		// host must be "localhost" to exercise the replacement
		host := strings.Replace(m.wsHost, "127.0.0.1", "localhost", 1)
		m.tgs = append(m.tgs, struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
			WSURL string `json:"webSocketDebuggerUrl"`
		}{
			ID:    id,
			Type:  "page",
			URL:   "vscode-workbench://" + id,
			Title: "win-" + id,
			WSURL: "ws://" + host + "/devtools/page/" + id,
		})
	}
}

func (m *mockCDPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.up {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/json/version":
		w.Write([]byte(`{"Browser":"mock"}`))
	case "/json/list":
		json.MarshalWrite(w, m.tgs)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestDiscoveryLifecycle(t *testing.T) {
	wsHost := startHoldWS(t)
	mock := &mockCDPServer{wsHost: wsHost}
	mock.set(false) // VS Code not running yet
	srv := httptest.NewServer(mock)
	defer srv.Close()

	events := make(chan Event, 16)
	d := NewDiscovery(strings.TrimPrefix(srv.URL, "http://"), events, discardLog, defaultDebounceMs)
	d.Poll = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// waiting: no sessions while VS Code is down
	time.Sleep(100 * time.Millisecond)
	d.mu.Lock()
	n0 := len(d.sessions)
	up0 := d.up
	d.mu.Unlock()
	if n0 != 0 || up0 {
		t.Fatalf("expected waiting state, got sessions=%d up=%v", n0, up0)
	}

	// VS Code starts with one window
	mock.set(true, "t1")
	waitFor(t, "session for t1", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		s, ok := d.sessions["t1"]
		return ok && !s.IsDone()
	})

	// second window opens
	mock.set(true, "t1", "t2")
	waitFor(t, "session for t2", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		s, ok := d.sessions["t2"]
		return ok && !s.IsDone()
	})

	// window t2 closes
	mock.set(true, "t1")
	waitFor(t, "t2 gone", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.sessions["t2"]
		return !ok
	})

	// VS Code fully exits -> all sessions stopped, state resets
	mock.set(false)
	waitFor(t, "disconnected", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.up && len(d.sessions) == 0
	})

	// VS Code restarts (fresh target ids, as in reality)
	mock.set(true, "t9")
	waitFor(t, "session for t9", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		s, ok := d.sessions["t9"]
		return ok && !s.IsDone()
	})

	cancel()
}

func TestDiscoveryWindowsChanged(t *testing.T) {
	wsHost := startHoldWS(t)
	mock := &mockCDPServer{wsHost: wsHost}
	mock.set(false)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	events := make(chan Event, 16)
	d := NewDiscovery(strings.TrimPrefix(srv.URL, "http://"), events, discardLog, defaultDebounceMs)
	d.Poll = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	ch := d.WindowsChanged()

	// VS Code starts with one window: the set changes, one signal.
	mock.set(true, "t1")
	waitFor(t, "first window signal", func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	})
	// The signal must not fire again while the window set is stable.
	mock.set(true, "t1") // same set the previous scan saw
	select {
	case <-ch:
		t.Fatal("no window-set change, but a signal was pending")
	case <-time.After(150 * time.Millisecond):
	}

	// A second window opens: one more signal.
	mock.set(true, "t1", "t2")
	waitFor(t, "second window signal", func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	})
	// VS Code exits: the set empties, one more signal.
	mock.set(false)
	waitFor(t, "disconnect signal", func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	})
}

func TestDiscoveryWindowsSorted(t *testing.T) {
	wsHost := startHoldWS(t)
	mock := &mockCDPServer{wsHost: wsHost}
	mock.set(true, "t2", "t1") // titles: win-t2, win-t1
	srv := httptest.NewServer(mock)
	defer srv.Close()

	events := make(chan Event, 16)
	d := NewDiscovery(strings.TrimPrefix(srv.URL, "http://"), events, discardLog, defaultDebounceMs)
	d.Poll = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	waitFor(t, "both sessions", func() bool { return len(d.Windows()) == 2 })

	// Stable title-sorted order across calls (map iteration is not).
	for i := 0; i < 20; i++ {
		wins := d.Windows()
		if len(wins) != 2 || wins[0].ID != "t1" || wins[1].ID != "t2" {
			t.Fatalf("expected stable sorted order [t1 t2], got %+v", wins)
		}
	}

	// SessionForID returns the live session; unknown id -> nil.
	if s := d.SessionForID("t2"); s == nil || s.Title() != "win-t2" {
		t.Fatalf("SessionForID(t2) wrong: %v", s)
	}
	if s := d.SessionForID("nope"); s != nil {
		t.Fatalf("expected nil for unknown id, got %v", s)
	}

	// A closed window drops out of the list and its session.
	mock.set(true, "t1")
	waitFor(t, "t2 gone", func() bool { return len(d.Windows()) == 1 })
	if s := d.SessionForID("t2"); s != nil {
		t.Fatalf("expected nil for closed window")
	}
}
