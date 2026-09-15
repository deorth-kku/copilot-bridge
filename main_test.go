package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vscode-load-llama/internal/cdp"
	"vscode-load-llama/internal/config"
	"vscode-load-llama/internal/loader"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestProcessEvent(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, r.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()

	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	content := fmt.Sprintf(`{"oaicopilot.models":[`+
		`{"id":"Qwen3.8-27B","displayName":"Qwen3.8 27B","baseUrl":"%s/v1","optimization":"llama.cpp"},`+
		`{"id":"Remote-Model","displayName":"Remote","baseUrl":"%s/v1","optimization":"openrouter"}]}`, srv.URL, srv.URL)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewStore(p, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	ld := loader.New(30*time.Second, discardLog, nil)
	log := discardLog

	// startup empty snapshot (POC-observed behavior) -> no load
	processEvent(cdp.Event{Window: "w", Input: "", Model: "Qwen3.8 27B"}, settings, ld, log)
	// nbsp/whitespace only -> no load
	processEvent(cdp.Event{Window: "w", Input: "  \u00a0 ", Model: "Qwen3.8 27B"}, settings, ld, log)
	// no model -> no load
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: ""}, settings, ld, log)
	// unknown model -> no load
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: "Unknown"}, settings, ld, log)
	// non-llama.cpp optimization -> no load
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: "Remote"}, settings, ld, log)

	mu.Lock()
	n := len(reqs)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no load requests, got %d: %v", n, reqs)
	}

	// real input -> exactly one load, at the SERVER ROOT, with the model id
	processEvent(cdp.Event{Window: "w", Input: "hello", Model: "Qwen3.8 27B"}, settings, ld, log)
	waitForReqs(t, &mu, &reqs, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 load request, got %d", len(reqs))
	}
	if reqs[0] != `/models/load {"model":"Qwen3.8-27B"}` {
		t.Errorf("unexpected request: %s", reqs[0])
	}
}

// waitForReqs polls until the server has seen at least n requests.
// Needed because processEvent dispatches Load in a goroutine.
func waitForReqs(t *testing.T, mu *sync.Mutex, reqs *[]string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := len(*reqs)
		mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d load requests, got %d", n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
