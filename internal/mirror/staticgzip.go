package mirror

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// The static HTML pages (the mirror page, the workspaces page) are large
// constant strings: the mirror page alone is ~98KB, nearly all of it the
// page CSS + JS. Compressing it once and serving the bytes with
// Content-Encoding: gzip turns that into ~20KB, which matters on a weak
// network where the page is re-fetched on every reload. The dynamic state
// stream is compressed separately, by the WebSocket's permessage-deflate
// (see the upgrader in New).
//
// gzipStatic memoizes one compressed copy per page, so the cost is paid
// once per process rather than per request.
type gzipStatic struct {
	src  string
	once sync.Once
	body []byte
}

// bytes returns the precompressed page, or nil when compression failed (the
// caller then serves the identity encoding).
func (g *gzipStatic) bytes() []byte {
	g.once.Do(func() {
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return // leave body nil: the caller falls back to identity
		}
		if _, err := zw.Write([]byte(g.src)); err != nil {
			return
		}
		if err := zw.Close(); err != nil {
			return
		}
		g.body = buf.Bytes()
	})
	return g.body
}

var (
	mirrorPageGz     = &gzipStatic{src: pageHTML}
	workspacesPageGz = &gzipStatic{src: workspacesHTML}
)

// writeStaticHTML serves a constant HTML page, gzip-encoded when the client
// advertises support and a precompressed copy is available.
func writeStaticHTML(w http.ResponseWriter, r *http.Request, src string, gz *gzipStatic) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Vary is required so a shared cache does not serve the gzipped body to
	// a client that cannot decode it.
	w.Header().Add("Vary", "Accept-Encoding")
	if body := gz.bytes(); len(body) > 0 && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
		return
	}
	_, _ = w.Write([]byte(src))
}

// acceptsGzip reports whether the request's Accept-Encoding header includes
// gzip (an explicit q=0 rejects it).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(name, "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if strings.TrimSpace(q) == "0" || strings.TrimSpace(q) == "0.0" {
				return false
			}
		}
		return true
	}
	return false
}
