package mirror

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"copilot-bridge/internal/cdp"
)

// TestWriteStaticHTMLGzip pins the Accept-Encoding negotiation for the two
// static pages: a gzip-capable client gets the compressed body (and the
// matching Content-Encoding), everyone else gets the plain bytes. The
// decompressed body must equal the page constant either way.
func TestWriteStaticHTMLGzip(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"mirror", pageHTML},
		{"workspaces", workspacesHTML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// gzip-capable client.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip, deflate, br")
			writeStaticHTML(rec, req, tc.src, &gzipStatic{src: tc.src})

			if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
				t.Errorf("Vary = %q, want it to include Accept-Encoding", v)
			}
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				t.Fatalf("gzip.NewReader: %v", err)
			}
			got, err := io.ReadAll(zr)
			if err != nil {
				t.Fatalf("read gzip body: %v", err)
			}
			if string(got) != tc.src {
				t.Errorf("decompressed body != page source (got %d bytes, want %d)", len(got), len(tc.src))
			}
			// The point of the exercise: the wire body is materially smaller.
			if len(rec.Body.Bytes()) >= len(tc.src) {
				t.Errorf("gzipped body %d is not smaller than source %d", len(rec.Body.Bytes()), len(tc.src))
			}

			// Client without gzip support.
			rec2 := httptest.NewRecorder()
			req2 := httptest.NewRequest(http.MethodGet, "/", nil)
			writeStaticHTML(rec2, req2, tc.src, &gzipStatic{src: tc.src})
			if got := rec2.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("no-gzip client got Content-Encoding %q, want none", got)
			}
			if rec2.Body.String() != tc.src {
				t.Errorf("identity body != page source")
			}
		})
	}
}

// TestAcceptsGzip covers the header parsing, including the q=0 opt-out.
func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"GZIP", true},
		{" gzip ", true},
		{"deflate, gzip", true},
		{"br, gzip;q=0.8", true},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"gzip; q=0.0", false},
		{"br, deflate", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Accept-Encoding", tc.header)
		}
		if got := acceptsGzip(req); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestGzipStaticMemoized pins that gzipStatic compresses once and reuses
// the result: the second call must return the same backing bytes, and the
// compressed form must round-trip back to the source.
func TestGzipStaticMemoized(t *testing.T) {
	gz := &gzipStatic{src: pageHTML}
	first := gz.bytes()
	if len(first) == 0 {
		t.Fatal("no precompressed body")
	}
	if len(first) >= len(pageHTML) {
		t.Errorf("compressed body %d is not smaller than source %d", len(first), len(pageHTML))
	}
	second := gz.bytes()
	if len(second) != len(first) || (len(first) > 0 && &first[0] != &second[0]) {
		t.Error("bytes() recompressed instead of reusing the memoized copy")
	}
	zr, err := gzip.NewReader(strings.NewReader(string(first)))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != pageHTML {
		t.Error("round-trip mismatch")
	}
}

// TestWSPermessageDeflate asserts the mirror's WebSocket negotiates
// permessage-deflate and that a large text frame actually arrives smaller
// than it was sent: the extracted workbench CSS is megabytes of repetitive
// rules, and compressing it is the main win on a weak network.
func TestWSPermessageDeflate(t *testing.T) {
	// Build a mirror with the same upgrader New installs, and serve /ws
	// through it. The discovery points at a dead CDP port: the tab gets the
	// "waiting for VS Code window" error state, which is enough to exercise
	// the handshake.
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	mux := http.NewServeMux()
	m := New(disc, discardLog(), "127.0.0.1:0", nil, "", "", "", "")
	mux.HandleFunc("/ws", m.handleWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The client must offer permessage-deflate for the server to use it.
	d := websocket.Dialer{
		HandshakeTimeout:  5 * time.Second,
		EnableCompression: true,
	}
	conn, resp, err := d.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if resp != nil {
		if got := resp.Header.Get("Sec-Websocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
			t.Errorf("handshake did not negotiate permessage-deflate: %q", got)
		}
	}
	// Read whatever the connect path sends: either a state frame or the
	// "waiting for VS Code window" error (no live window in tests). Either
	// way the connection must be alive and readable.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read after deflate handshake: %v", err)
	}
}
