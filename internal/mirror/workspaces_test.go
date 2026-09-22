package mirror

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/workspaces"
)

// minStorage is a one-workspace storage.json (open + last-active).
const minStorage = `{
  "profileAssociations": {
    "workspaces": {
      "file:///c%3A/Users/deort/vscode-load-llama": "__default__profile__"
    }
  },
  "windowsState": {
    "lastActiveWindow": { "folder": "file:///c%3A/Users/deort/vscode-load-llama" },
    "openedWindows": [
      { "folder": "file:///c%3A/Users/deort/vscode-load-llama" }
    ]
  }
}`

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeStorage writes one storage.json fixture into a temp dir.
func writeStorage(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitFor polls f until it returns true or the test fails.
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

// mockLiveCDP stands in for a running VS Code: it serves the CDP HTTP
// endpoint (/json/version, /json/list) and answers the page WebSocket
// commands. The workspace probe (Runtime.evaluate with awaitPromise and
// the resolveConfiguration expression) returns the target's configured
// workspace URI; every other command gets an empty result (the pane
// probes fail, which is fine — the workspaces page never mirrors a pane).
type mockLiveCDP struct {
	mu      sync.Mutex
	url     string
	wsHost  string
	up      bool
	uriByID map[string]string // target id -> workspace URI
}

func newMockLiveCDP(t *testing.T) *mockLiveCDP {
	t.Helper()
	m := &mockLiveCDP{uriByID: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		up := m.up
		m.mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"mock"}`))
	})
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		up := m.up
		targets := make([]map[string]any, 0, len(m.uriByID))
		for id := range m.uriByID {
			targets = append(targets, map[string]any{
				"id":                   id,
				"type":                 "page",
				"url":                  "vscode-workbench://" + id,
				"title":                "win-" + id,
				"webSocketDebuggerUrl": "ws://" + m.wsHost + "/devtools/page/" + id,
			})
		}
		m.mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.MarshalWrite(w, targets)
	})
	mux.HandleFunc("/devtools/page/", m.handlePageWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m.url = srv.URL
	m.wsHost = strings.TrimPrefix(srv.URL, "http://")
	return m
}

// set replaces the live window set: target id -> workspace URI.
func (m *mockLiveCDP) set(up bool, winURIs map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.up = up
	m.uriByID = winURIs
}

// handlePageWS answers one window's CDP commands.
func (m *mockLiveCDP) handlePageWS(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/devtools/page/")
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
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
			Params struct {
				Expression   string `json:"expression"`
				AwaitPromise bool   `json:"awaitPromise"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil || req.ID == nil {
			continue
		}
		result := `{"result":{"type":"undefined"}}`
		if req.Method == "Runtime.evaluate" && req.Params.AwaitPromise &&
			strings.Contains(req.Params.Expression, "resolveConfiguration") {
			m.mu.Lock()
			uri, ok := m.uriByID[id]
			m.mu.Unlock()
			if ok {
				b, _ := json.Marshal(uri)
				result = `{"result":{"type":"string","value":` + string(b) + `}}`
			}
		}
		resp := `{"id":` + strconv.Itoa(*req.ID) + `,"result":` + result + `}`
		if err := conn.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
			return
		}
	}
}

// fakeClient builds a client with no live connection: enough surface for
// the send-queue logic under test.
func fakeClient(wsPage bool) *client {
	return &client{send: make(chan any, 64), done: make(chan struct{}), logger: discardLog(), wsPage: wsPage}
}

func TestNewWorkspacesStore(t *testing.T) {
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	// Missing storage.json: the store must be nil (the HTTP API falls back
	// to a fresh read per request).
	m := New(disc, discardLog(), "127.0.0.1:0", nil, "", filepath.Join(t.TempDir(), "nope.json"), "", "")
	if m.wsStore != nil {
		t.Fatal("expected a nil store for a missing storage.json")
	}
	// Readable storage.json: the store is created with the initial list.
	m = New(disc, discardLog(), "127.0.0.1:0", nil, "", writeStorage(t, minStorage), "", "")
	if m.wsStore == nil {
		t.Fatal("expected a store for a readable storage.json")
	}
	if ws := m.wsStore.Load(); len(ws) != 1 || !ws[0].Open {
		t.Fatalf("unexpected initial list: %+v", ws)
	}
}

func TestSendWorkspaces(t *testing.T) {
	st, err := workspaces.NewStore(writeStorage(t, minStorage), discardLog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	m := &Mirror{log: discardLog(), wsStore: st, disc: disc}
	c := fakeClient(true)
	m.sendWorkspaces(c)
	select {
	case msg := <-c.send:
		wm, ok := msg.(workspacesMsg)
		if !ok {
			t.Fatalf("expected workspacesMsg, got %T", msg)
		}
		if wm.Type != "workspaces" || len(wm.Workspaces) != 1 {
			t.Fatalf("bad message: %+v", wm)
		}
		// No live window: the CDP overlay closes the workspace even
		// though storage.json claims it open.
		if wm.Workspaces[0].Open {
			t.Fatalf("expected the workspace closed (no live window): %+v", wm.Workspaces)
		}
	default:
		t.Fatal("no message queued")
	}

	// A nil store (initial load failed) and no readable storage.json
	// queue nothing.
	m.wsStore = nil
	c2 := fakeClient(true)
	m.sendWorkspaces(c2)
	if len(c2.send) != 0 {
		t.Fatal("expected no message for a nil store")
	}
}

func TestPushWorkspaces(t *testing.T) {
	m := &Mirror{log: discardLog()}
	page := fakeClient(true)
	pane := fakeClient(false)
	m.addClient(page)
	m.addClient(pane)

	m.pushWorkspaces([]workspaces.Workspace{{URI: "file:///x", Name: "x", Open: true}})

	select {
	case msg := <-page.send:
		wm, ok := msg.(workspacesMsg)
		if !ok || wm.Type != "workspaces" || len(wm.Workspaces) != 1 {
			t.Fatalf("bad push: %T %+v", msg, msg)
		}
	default:
		t.Fatal("workspaces-page client got no push")
	}
	if len(pane.send) != 0 {
		t.Fatal("mirror client must not receive workspaces pushes")
	}
}

func TestGroupClientsExcludesWsPage(t *testing.T) {
	m := &Mirror{log: discardLog()}
	page := fakeClient(true)
	pane := fakeClient(false)
	m.addClient(page)
	m.addClient(pane)

	groups := m.groupClients(nil)
	total := 0
	for _, cs := range groups {
		for _, c := range cs {
			if c.wsPage {
				t.Fatal("workspaces-page client must not be grouped into a pane")
			}
		}
		total += len(cs)
	}
	// With no live windows only the pane client is grouped (into the
	// empty default-window key).
	if total != 1 {
		t.Fatalf("expected exactly the pane client in the groups, got %d", total)
	}
}

// TestWorkspacesWSPushEndToEnd runs the real server against a mock VS Code
// (mockLiveCDP): the workspaces page's open flags follow the LIVE windows
// (CDP probe), not storage.json's lagging windowsState.
func TestWorkspacesWSPushEndToEnd(t *testing.T) {
	uriA := "file:///c%3A/Users/deort/ws-a"
	uriB := "file:///c%3A/Users/deort/ws-b"
	uriC := "file:///c%3A/Users/deort/ws-c" // never in storage.json

	// storage.json knows A and B and claims B is open — stale, because the
	// live window actually has A.
	storage := writeStorage(t, `{
	  "profileAssociations": {
	    "workspaces": {
	      "`+uriA+`": "__default__profile__",
	      "`+uriB+`": "__default__profile__"
	    }
	  },
	  "windowsState": {
	    "lastActiveWindow": { "folder": "`+uriB+`" },
	    "openedWindows": [ { "folder": "`+uriB+`" } ]
	  }
	}`)

	mock := newMockLiveCDP(t)
	mock.set(true, map[string]string{"t1": uriA})
	disc := cdp.NewDiscovery(strings.TrimPrefix(mock.url, "http://"), make(chan cdp.Event, 1), discardLog(), 50)
	disc.Poll = 20 * time.Millisecond
	addr := freeAddr(t)
	m := New(disc, discardLog(), addr, nil, "", storage, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disc.Run(ctx)
	go m.Run(ctx)

	// Wait until discovery has attached the live window AND its workspace
	// probe answers (a just-created session may not be dialled yet).
	waitFor(t, "t1 workspace probe", func() bool {
		s := disc.SessionForID("t1")
		return s != nil && cdp.WorkspaceURI(s) == uriA
	})

	// The listener comes up asynchronously; retry the dial until it does.
	var conn *websocket.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		conn, _, err = websocket.DefaultDialer.Dial("ws://"+addr+"/ws?page=workspaces", nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not connect to ws://%s: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer conn.Close()

	// The first message: the live window has A open, so A is open and B is
	// closed — the CDP probe overrides storage.json's claim that B is open.
	first := readWorkspacesMsgUntil(t, conn, func(ws []workspaces.Workspace) bool {
		return len(ws) == 2 && openURI(ws, uriA) && !openURI(ws, uriB)
	})
	if len(first) != 2 || !openURI(first, uriA) || openURI(first, uriB) {
		t.Fatalf("unexpected initial list: %+v", first)
	}

	// The window switches to a workspace storage.json does not know yet:
	// it must appear with the open flag, and A must close.
	mock.set(true, map[string]string{"t2": uriC})
	second := readWorkspacesMsgUntil(t, conn, func(ws []workspaces.Workspace) bool {
		return len(ws) == 3 && openURI(ws, uriC) && !openURI(ws, uriA) && !openURI(ws, uriB)
	})
	if len(second) != 3 || !openURI(second, uriC) || openURI(second, uriA) || openURI(second, uriB) {
		t.Fatalf("unexpected second list: %+v", second)
	}

	// All windows close: back to the known list, nothing open.
	mock.set(true, nil)
	third := readWorkspacesMsgUntil(t, conn, func(ws []workspaces.Workspace) bool {
		return len(ws) == 2 && !openURI(ws, uriA) && !openURI(ws, uriB)
	})
	if len(third) != 2 || openURI(third, uriA) || openURI(third, uriB) {
		t.Fatalf("unexpected third list: %+v", third)
	}
}

// TestWorkspacesStoragePushEndToEnd runs the real server with NO live
// windows: rewriting storage.json (a new known workspace appears) must
// still push the updated list over the workspaces-page socket.
func TestWorkspacesStoragePushEndToEnd(t *testing.T) {
	uriA := "file:///c%3A/Users/deort/ws-a"
	uriB := "file:///c%3A/Users/deort/ws-b"
	storage := writeStorage(t, `{
	  "profileAssociations": {
	    "workspaces": {
	      "`+uriA+`": "__default__profile__"
	    }
	  },
	  "windowsState": { "openedWindows": [] }
	}`)
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	addr := freeAddr(t)
	m := New(disc, discardLog(), addr, nil, "", storage, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	var conn *websocket.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		conn, _, err = websocket.DefaultDialer.Dial("ws://"+addr+"/ws?page=workspaces", nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not connect to ws://%s: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer conn.Close()

	first := readWorkspacesMsgUntil(t, conn, func(ws []workspaces.Workspace) bool {
		return len(ws) == 1 && !openURI(ws, uriA)
	})
	if len(first) != 1 {
		t.Fatalf("unexpected initial list: %+v", first)
	}

	// Rewrite storage.json: a second known workspace appears.
	if err := os.WriteFile(storage, []byte(`{
	  "profileAssociations": {
	    "workspaces": {
	      "`+uriA+`": "__default__profile__",
	      "`+uriB+`": "__default__profile__"
	    }
	  },
	  "windowsState": { "openedWindows": [] }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	second := readWorkspacesMsgUntil(t, conn, func(ws []workspaces.Workspace) bool {
		return len(ws) == 2 && !openURI(ws, uriA) && !openURI(ws, uriB)
	})
	if len(second) != 2 || openURI(second, uriA) || openURI(second, uriB) {
		t.Fatalf("expected a pushed update with both workspaces closed: %+v", second)
	}
}

// TestResolveWindowURI checks the jump-to-window lookup: the live window
// whose workbench reports the given workspace URI.
func TestResolveWindowURI(t *testing.T) {
	uriA := "file:///c%3A/Users/deort/ws-a"
	uriB := "file:///c%3A/Users/deort/ws-b"
	mock := newMockLiveCDP(t)
	mock.set(true, map[string]string{"t1": uriA, "t2": uriB})
	disc := cdp.NewDiscovery(strings.TrimPrefix(mock.url, "http://"), make(chan cdp.Event, 1), discardLog(), 50)
	disc.Poll = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go disc.Run(ctx)

	waitFor(t, "both windows probed", func() bool {
		s1 := disc.SessionForID("t1")
		s2 := disc.SessionForID("t2")
		return s1 != nil && s2 != nil &&
			cdp.WorkspaceURI(s1) == uriA && cdp.WorkspaceURI(s2) == uriB
	})

	m := &Mirror{log: discardLog(), disc: disc}
	id, ok := m.resolveWindowURI(uriA)
	if !ok || id != "t1" {
		t.Fatalf("resolveWindowURI(%q) = %q, %v; want t1, true", uriA, id, ok)
	}
	// Drive-letter case differences still match (SameURI semantics).
	id, ok = m.resolveWindowURI("file:///C%3A/Users/deort/ws-b")
	if !ok || id != "t2" {
		t.Fatalf("resolveWindowURI(case variant) = %q, %v; want t2, true", id, ok)
	}
	if _, ok := m.resolveWindowURI("file:///c%3A/Users/deort/ws-none"); ok {
		t.Fatal("expected no match for an unknown workspace")
	}
}

// freeAddr returns a free 127.0.0.1 TCP address for the test server.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// readWorkspacesMsgUntil reads "workspaces" messages until one satisfies
// want (intermediate pushes are skipped — a recompute can race a window's
// CDP dial, or a storage.json reload can land mid-flight) or the deadline
// passes.
func readWorkspacesMsgUntil(t *testing.T, conn *websocket.Conn, want func([]workspaces.Workspace) bool) []workspaces.Workspace {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last []workspaces.Workspace
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a workspaces list satisfying the predicate, last: %+v", last)
		}
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read workspaces message: %v (last: %+v)", err, last)
		}
		var m workspacesMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal workspaces message: %v", err)
		}
		if m.Type != "workspaces" {
			t.Fatalf("unexpected message type %q", m.Type)
		}
		last = m.Workspaces
		if want(m.Workspaces) {
			return m.Workspaces
		}
	}
}

// openURI reports whether the workspace with the given URI is in the list
// and marked open (SameURI semantics).
func openURI(ws []workspaces.Workspace, uri string) bool {
	for _, w := range ws {
		if workspaces.SameURI(w.URI, uri) {
			return w.Open
		}
	}
	return false
}
