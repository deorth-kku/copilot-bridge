package cdp

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startMockPage simulates a VS Code workbench page over CDP. It records
// every command method the client sends (in order) and drives the same
// flow the real page produces:
//
//  1. client: Runtime.enable, Page.enable, then addBinding + evaluate for
//     BOTH bindings (chat state, mirror wake)
//  2. page:   bindingCalled with EMPTY input (startup snapshot — the
//     real page always emits this on (re)inject),
//     then a real input event,
//     then Page.frameNavigated (workbench reload),
//  3. client: addBinding + evaluate for both bindings (re-injection)
//  4. page:   bindingCalled with final input
func startMockPage(t *testing.T) (wsURL string, methods chan string) {
	t.Helper()
	methods = make(chan string, 32)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		send := func(payload string) {
			conn.WriteJSON(map[string]any{
				"method": "Runtime.bindingCalled",
				"params": map[string]any{"name": BindingName, "payload": payload},
			})
		}
		evaluates := 0
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			method, _ := m["method"].(string)
			methods <- method
			// Answer id-carrying commands: the session's injection uses
			// Call for addBinding/evaluate and waits for these responses.
			if id, ok := m["id"]; ok {
				conn.WriteJSON(map[string]any{"id": id, "result": map[string]any{}})
			}
			if method == "Runtime.evaluate" {
				evaluates++
				if evaluates == 1 {
					// startup snapshot: empty input (POC-observed behavior)
					send(`{"input":"","model":"Qwen3.8 27B","effort":null,"mode":null}`)
					send(`{"input":"hello","model":"Qwen3.8 27B","effort":"high","mode":"agent"}`)
					// simulate workbench reload
					conn.WriteJSON(map[string]any{"method": "Page.frameNavigated"})
				} else {
					// re-injection after frameNavigated
					send(`{"input":"final","model":"Qwen3.8 27B","effort":null,"mode":null}`)
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), methods
}

func readEvent(t *testing.T, events chan Event) Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for event")
		return Event{}
	}
}

func TestSessionHandshakeAndEvents(t *testing.T) {
	wsURL, methods := startMockPage(t)
	events := make(chan Event, 10)
	s := NewSession("test-id", "Test Window", wsURL, events, discardLog, defaultDebounceMs)
	go s.Run(context.Background())

	// 1. handshake commands in the expected order (two bindings:
	//    chat state + mirror wake)
	for _, want := range []string{
		"Runtime.enable", "Page.enable",
		"Runtime.addBinding", "Runtime.evaluate",
		"Runtime.addBinding", "Runtime.evaluate",
	} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("command order: got %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for handshake command")
		}
	}

	// 2. startup empty-input snapshot is forwarded (the POC-verified case)
	ev := readEvent(t, events)
	if ev.Input != "" {
		t.Errorf("first event should be the startup empty snapshot, got %q", ev.Input)
	}
	if ev.Model != "Qwen3.8 27B" || ev.Window != "Test Window" {
		t.Errorf("unexpected first event: %+v", ev)
	}

	// 3. real input event forwarded with all fields
	ev = readEvent(t, events)
	if ev.Input != "hello" || ev.Effort != "high" || ev.Mode != "agent" {
		t.Errorf("unexpected second event: %+v", ev)
	}

	// 4. frameNavigated triggers re-injection of BOTH bindings
	for _, want := range []string{
		"Runtime.addBinding", "Runtime.evaluate",
		"Runtime.addBinding", "Runtime.evaluate",
	} {
		select {
		case got := <-methods:
			if got != want {
				t.Fatalf("re-injection: got %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for re-injection command")
		}
	}

	// 5. event after re-injection is delivered
	ev = readEvent(t, events)
	if ev.Input != "final" {
		t.Errorf("unexpected third event: %+v", ev)
	}

	s.Stop()
}

func TestSessionDialFailureDoesNotPanic(t *testing.T) {
	events := make(chan Event, 1)
	s := NewSession("x", "X", "ws://127.0.0.1:1/devtools/page/x", events, discardLog, defaultDebounceMs)
	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
		if !s.IsDone() {
			t.Error("IsDone should be true after run exits")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after dial failure")
	}
}

// startControlledPage simulates a workbench page whose binding payloads
// the test can push on demand (unlike startMockPage, which is scripted
// by Runtime.evaluate).
func startControlledPage(t *testing.T) (wsURL string, push chan string) {
	t.Helper()
	push = make(chan string, 16)
	t.Cleanup(func() { close(push) })
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// NOTE: no connect handshake here — the client only dials after
		// this function returns the URL, so waiting for a connection
		// would deadlock. The buffered push channel decouples timing.
		// Single writer goroutine: both the pushed binding events and the
		// id-matched command responses (the session's injection Calls wait
		// for them) go through it, so the connection never has two
		// concurrent writers.
		responses := make(chan map[string]any, 16)
		go func() {
			for {
				select {
				case p, ok := <-push:
					if !ok {
						return
					}
					c.WriteJSON(map[string]any{
						"method": "Runtime.bindingCalled",
						"params": map[string]any{"name": BindingName, "payload": p},
					})
				case r := <-responses:
					c.WriteJSON(r)
				}
			}
		}()
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err == nil {
				if id, ok := m["id"]; ok {
					responses <- map[string]any{"id": id, "result": map[string]any{}}
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), push
}

// TestSessionTitleUpdateVisibleInEvents verifies that a title change
// (SetTitle, driven by the discovery scan) is reflected in the Window
// field of subsequent events, race-free (run with -race).
func TestSessionTitleUpdateVisibleInEvents(t *testing.T) {
	wsURL, push := startControlledPage(t)
	events := make(chan Event, 10)
	s := NewSession("id", "Original", wsURL, events, discardLog, defaultDebounceMs)
	go s.Run(context.Background())
	defer s.Stop()

	push <- `{"input":"a","model":"M"}`
	ev := readEvent(t, events)
	if ev.Window != "Original" {
		t.Fatalf("expected Window %q, got %q", "Original", ev.Window)
	}

	s.SetTitle("Renamed")
	if got := s.Title(); got != "Renamed" {
		t.Fatalf("Title() = %q, want %q", got, "Renamed")
	}

	push <- `{"input":"b","model":"M"}`
	ev = readEvent(t, events)
	if ev.Window != "Renamed" {
		t.Fatalf("expected Window %q after SetTitle, got %q", "Renamed", ev.Window)
	}
}
