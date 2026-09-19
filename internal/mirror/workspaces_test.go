package mirror

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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

// closedStorage is the same workspace with no open windows: the open flag
// must flip to false after a reload.
const closedStorage = `{
  "profileAssociations": {
    "workspaces": {
      "file:///c%3A/Users/deort/vscode-load-llama": "__default__profile__"
    }
  },
  "windowsState": {
    "lastActiveWindow": {},
    "openedWindows": []
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

// fakeClient builds a client with no live connection: enough surface for
// the send-queue logic under test.
func fakeClient(wsPage bool) *client {
	return &client{send: make(chan any, 64), done: make(chan struct{}), logger: discardLog(), wsPage: wsPage}
}

func TestNewWorkspacesStore(t *testing.T) {
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	// Missing storage.json: the store must be nil (the HTTP API falls back
	// to a fresh read per request).
	m := New(disc, discardLog(), "127.0.0.1:0", nil, "", filepath.Join(t.TempDir(), "nope.json"), "")
	if m.wsStore != nil {
		t.Fatal("expected a nil store for a missing storage.json")
	}
	// Readable storage.json: the store is created with the initial list.
	m = New(disc, discardLog(), "127.0.0.1:0", nil, "", writeStorage(t, minStorage), "")
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
	m := &Mirror{log: discardLog(), wsStore: st}
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
	default:
		t.Fatal("no message queued")
	}

	// A nil store (initial load failed) queues nothing.
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

// TestWorkspacesWSPushEndToEnd runs the real server: a workspaces-page tab
// receives the list on connect, and rewriting storage.json pushes the
// updated list over the same socket (the green-dot live update).
func TestWorkspacesWSPushEndToEnd(t *testing.T) {
	storage := writeStorage(t, minStorage)
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	addr := freeAddr(t)
	m := New(disc, discardLog(), addr, nil, "", storage, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

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

	// The first message is the initial list (open workspace).
	first := readWorkspacesMsg(t, conn)
	if len(first) != 1 || !first[0].Open {
		t.Fatalf("unexpected initial list: %+v", first)
	}

	// Rewrite storage.json: the watcher must push the updated list
	// (workspace now closed) over the same socket.
	if err := os.WriteFile(storage, []byte(closedStorage), 0o644); err != nil {
		t.Fatal(err)
	}
	second := readWorkspacesMsg(t, conn)
	if len(second) != 1 || second[0].Open {
		t.Fatalf("expected a pushed update with the workspace closed: %+v", second)
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

// readWorkspacesMsg reads one WebSocket frame and decodes it as a
// "workspaces" message.
func readWorkspacesMsg(t *testing.T, conn *websocket.Conn) []workspaces.Workspace {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read workspaces message: %v", err)
	}
	var m workspacesMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal workspaces message: %v", err)
	}
	if m.Type != "workspaces" {
		t.Fatalf("unexpected message type %q", m.Type)
	}
	return m.Workspaces
}
