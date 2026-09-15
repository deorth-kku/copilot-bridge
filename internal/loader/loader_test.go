package loader

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"copilot-bridge/internal/config"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLoadPostsToServerRoot(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	// baseUrl carries a path that must be STRIPPED: the load endpoint
	// is relative to the server root, not to baseUrl.
	m := config.Model{ID: "Qwen3.8-27B", BaseURL: srv.URL + "/v1"}
	l.Load(m)

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("expected 1 request, got %d", len(paths))
	}
	if paths[0] != "/models/load" {
		t.Errorf("expected path /models/load (server root), got %s", paths[0])
	}
	if bodies[0] != `{"model":"Qwen3.8-27B"}` {
		t.Errorf("unexpected body: %s", bodies[0])
	}
}

func TestLoadCooldownSkips(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true}`))
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	m := config.Model{ID: "m1", BaseURL: srv.URL}
	l.Load(m)
	l.Load(m) // within cooldown -> skipped
	if n != 1 {
		t.Fatalf("expected 1 request after cooldown skip, got %d", n)
	}
}

func TestLoadDifferentModelsIndependent(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	l := New(30*time.Second, discardLog, nil)
	l.Load(config.Model{ID: "m1", BaseURL: srv.URL})
	l.Load(config.Model{ID: "m2", BaseURL: srv.URL})
	if n != 2 {
		t.Fatalf("expected 2 requests for different models, got %d", n)
	}
}

func TestLoadSameIDDifferentServersIndependent(t *testing.T) {
	var n1, n2 int
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true}`))
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n2++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true}`))
	}))
	defer srv2.Close()

	l := New(30*time.Second, discardLog, nil)
	// Same model id on two different servers: the (server, id) keys differ,
	// so each server gets its own load (no cross-server cooldown sharing).
	l.Load(config.Model{ID: "m1", BaseURL: srv1.URL})
	l.Load(config.Model{ID: "m1", BaseURL: srv2.URL})
	if n1 != 1 || n2 != 1 {
		t.Fatalf("expected 1 request per server, got srv1=%d srv2=%d", n1, n2)
	}
}

func TestLoadAlreadyRunningIsNormal(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	// short cooldown: if "already running" wrongly marked the endpoint
	// as non-llama, the 2nd call would be skipped and n would stay 1.
	l := New(10*time.Millisecond, discardLog, nil)
	m := config.Model{ID: "m1", BaseURL: srv.URL}
	l.Load(m)
	time.Sleep(20 * time.Millisecond)
	l.Load(m)
	if n != 2 {
		t.Fatalf("expected 2 requests (already running is normal), got %d", n)
	}
}

func TestLoadRespectsProxy(t *testing.T) {
	var targetN, proxyN int
	// target llama server
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetN++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	// minimal forwarding proxy (absolute-URI form)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyN++
		req2, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(req2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	l := New(30*time.Second, discardLog, func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	l.Load(config.Model{ID: "m1", BaseURL: target.URL})

	if proxyN != 1 {
		t.Fatalf("expected request to go through the proxy, proxy saw %d", proxyN)
	}
	if targetN != 1 {
		t.Fatalf("expected target to receive the forwarded request, got %d", targetN)
	}
}

func TestLoadClassifiesResponsesStrictly(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantLog string
	}{
		{"success exact", http.StatusOK, `{"success":true}`, "model loaded"},
		{"success extra field", http.StatusOK, `{"success":true,"foo":1}`, "model loaded"},
		{"success loose body", http.StatusOK, `{}`, "unexpected response"},
		{"already running exact", http.StatusBadRequest, `{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}`, "model already running"},
		{"already running wrong message", http.StatusBadRequest, `{"error":{"code":400,"message":"nope","type":"invalid_request_error"}}`, "unexpected response"},
		{"malformed json", http.StatusOK, `not json`, "unexpected response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			var buf bytes.Buffer
			l := New(30*time.Second, slog.New(slog.NewTextHandler(&buf, nil)), nil)
			l.Load(config.Model{ID: "m1", BaseURL: srv.URL})
			if !strings.Contains(buf.String(), tc.wantLog) {
				t.Fatalf("expected %q in log, got: %s", tc.wantLog, buf.String())
			}
		})
	}
}

// TestLoadCooldownOnlyOnCorrectResponse pins the core invariant: the
// per-model cooldown is armed ONLY on a strictly-correct reply (success,
// or the exact "already running" error). Any other reply — loose body,
// malformed JSON, wrong status, or a genuine llama.cpp "File Not Found" —
// must NOT arm the cooldown, so a Load issued immediately after still
// reaches the server.
func TestLoadCooldownOnlyOnCorrectResponse(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSecond bool // true => 2nd Load inside the window sends another request
	}{
		{"success", http.StatusOK, `{"success":true}`, false},
		{"already running", http.StatusBadRequest, `{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}`, false},
		{"loose body no success", http.StatusOK, `{}`, true},
		{"malformed json", http.StatusOK, `not json`, true},
		{"success but 500", http.StatusInternalServerError, `{"success":true}`, true},
		{"model not found", http.StatusNotFound, `{"error":{"code":400,"message":"File Not Found","type":"invalid_request_error"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			l := New(30*time.Second, discardLog, nil)
			m := config.Model{ID: "m1", BaseURL: srv.URL}
			l.Load(m)
			l.Load(m) // immediately, still inside the 30s cooldown window

			want := 1
			if tc.wantSecond {
				want = 2
			}
			if n != want {
				t.Fatalf("expected %d requests, got %d", want, n)
			}
		})
	}
}
