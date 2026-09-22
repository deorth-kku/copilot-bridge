package mirror

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/shutdown"
)

// newHookMirror builds a Mirror with the given planner (nil allowed) and
// no live VS Code (the hook endpoints do not need CDP).
func newHookMirror(t *testing.T, planner *shutdown.Planner) (*Mirror, string) {
	t.Helper()
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	addr := freeAddr(t)
	m := New(disc, discardLog(), addr, nil, "", filepath.Join(t.TempDir(), "nope.json"), "", "")
	m.SetPlanner(planner)
	return m, addr
}

// waitServer returns the server base URL once the listener is up.
func waitServer(t *testing.T, addr string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			return "http://" + addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not come up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// postHook POSTs one hook payload to /api/hook and returns the status.
func postHook(t *testing.T, base, body string) int {
	t.Helper()
	resp, err := http.Post(base+"/api/hook", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestHookEventDrivesPlanner(t *testing.T) {
	var fired atomic.Bool
	planner := shutdown.NewPlanner(50*time.Millisecond, func() { fired.Store(true) })
	m, addr := newHookMirror(t, planner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	base := waitServer(t, addr)

	planner.Arm()
	if code := postHook(t, base, `{"hook_event_name":"Stop"}`); code != http.StatusOK {
		t.Fatalf("POST /api/hook status = %d, want 200", code)
	}
	waitFor(t, "trigger fired", func() bool { return fired.Load() })
}

func TestHookStartInsideGraceCancels(t *testing.T) {
	var fired atomic.Bool
	planner := shutdown.NewPlanner(50*time.Millisecond, func() { fired.Store(true) })
	m, addr := newHookMirror(t, planner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	base := waitServer(t, addr)

	planner.Arm()
	if code := postHook(t, base, `{"hook_event_name":"Stop"}`); code != http.StatusOK {
		t.Fatalf("POST /api/hook status = %d, want 200", code)
	}
	// A new task starts inside the grace window: cancels the pending
	// power-off, keeps the armed flag.
	if code := postHook(t, base, `{"hook_event_name":"SessionStart"}`); code != http.StatusOK {
		t.Fatalf("POST /api/hook status = %d, want 200", code)
	}
	time.Sleep(150 * time.Millisecond)
	if fired.Load() {
		t.Fatal("start inside the grace window must cancel the pending power-off")
	}
	if !planner.Armed() {
		t.Fatal("start must not disarm the planner")
	}
}

func TestHookEndpointNoPlanner(t *testing.T) {
	m, addr := newHookMirror(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	base := waitServer(t, addr)

	if code := postHook(t, base, `{"hook_event_name":"Stop"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/hook status = %d, want 503 without a planner", code)
	}
}

func TestShutdownArmEndpoint(t *testing.T) {
	planner := shutdown.NewPlanner(10*time.Second, nil)
	m, addr := newHookMirror(t, planner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	base := waitServer(t, addr)

	// A workspaces-page client connects: with no storage.json it receives
	// no workspaces message, so the first frame is the armed state.
	conn, err := dialWS(t, addr, "/ws?page=workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var first shutdownMsg
	readMsg(t, conn, &first)
	if first.Type != "shutdown" || first.Armed {
		t.Fatalf("unexpected first frame: %+v", first)
	}

	// Arm via the endpoint: 200, armed echoed, and a push to the client.
	resp, err := http.Post(base+"/api/shutdown", "application/json", strings.NewReader(`{"armed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/shutdown status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"armed":true`) {
		t.Fatalf("unexpected response: %s", body)
	}
	var pushed shutdownMsg
	readMsg(t, conn, &pushed)
	if pushed.Type != "shutdown" || !pushed.Armed {
		t.Fatalf("unexpected push: %+v", pushed)
	}

	// Disarm again.
	resp, err = http.Post(base+"/api/shutdown", "application/json", strings.NewReader(`{"armed":false}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/shutdown status = %d, want 200", resp.StatusCode)
	}
	if planner.Armed() {
		t.Fatal("planner must be disarmed")
	}
	var pushed2 shutdownMsg
	readMsg(t, conn, &pushed2)
	if pushed2.Type != "shutdown" || pushed2.Armed {
		t.Fatalf("unexpected push: %+v", pushed2)
	}
}

// dialWS dials the mirror WebSocket, retrying until the listener is up.
func dialWS(t *testing.T, addr, path string) (*websocket.Conn, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var conn *websocket.Conn
	var err error
	for {
		conn, _, err = websocket.DefaultDialer.Dial("ws://"+addr+path, nil)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readMsg reads one WebSocket JSON message with a deadline.
func readMsg(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(v); err != nil {
		t.Fatal(err)
	}
}
