package workspaces

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

// updatedStorage is the same fixture with different open/last-active
// markers, so a hot reload is observable through the Open flags.
const updatedStorage = `{
  "profileAssociations": {
    "workspaces": {
      "file:///c%3A/Users/deort/vscode-load-llama": "__default__profile__",
      "file:///e%3A/": "__default__profile__",
      "vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d": "__default__profile__"
    }
  },
  "windowsState": {
    "lastActiveWindow": { "folder": "file:///e%3A/" },
    "openedWindows": [
      { "folder": "file:///e%3A/" },
      { "folder": "file:///c%3A/Users/deort/vscode-load-llama" }
    ]
  }
}`

func openCount(ws []Workspace) int {
	n := 0
	for _, w := range ws {
		if w.Open {
			n++
		}
	}
	return n
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStoreInitialLoad(t *testing.T) {
	st, err := NewStore(writeFixture(t), discardLog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := st.Load()
	if len(ws) != 3 {
		t.Fatalf("got %d workspaces, want 3", len(ws))
	}
	if openCount(ws) != 1 {
		t.Fatalf("expected the initial snapshot to carry the open flags: %+v", ws)
	}
}

func TestStoreHotReloadAndOnChange(t *testing.T) {
	p := writeFixture(t)
	var mu sync.Mutex
	var pushed [][]Workspace
	st, err := NewStore(p, discardLog(), func(ws []Workspace) {
		mu.Lock()
		pushed = append(pushed, ws)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := st.Watch(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for the watcher to be armed before mutating.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(p, []byte(updatedStorage), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if openCount(st.Load()) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if openCount(st.Load()) != 2 {
		t.Fatalf("store did not hot-reload: %+v", st.Load())
	}

	// The onChange callback must have fired with the new snapshot.
	mu.Lock()
	defer mu.Unlock()
	if len(pushed) == 0 {
		t.Fatal("onChange was never called")
	}
	if openCount(pushed[len(pushed)-1]) != 2 {
		t.Fatalf("last pushed snapshot is stale: %+v", pushed[len(pushed)-1])
	}
}

func TestStoreReloadKeepsPreviousOnInvalidJSON(t *testing.T) {
	p := writeFixture(t)
	st, err := NewStore(p, discardLog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := st.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// Corrupt the file: reload should fail and keep the previous snapshot.
	if err := os.WriteFile(p, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Give the watcher time to attempt (and fail) the reload.
	time.Sleep(500 * time.Millisecond)
	if len(st.Load()) != 3 {
		t.Fatal("expected previous snapshot to be retained after invalid reload")
	}
}
