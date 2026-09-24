package mirror

import (
	"net/http"
	"strings"
	"time"

	"copilot-bridge/internal/cdp"
)

// handleImage serves one cached image resource under /img/<hash>.
func (m *Mirror) handleImage(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/img/")
	if key == "" || strings.Contains(key, "/") {
		http.NotFound(w, r)
		return
	}
	e, ok := m.imgs.Load(key)
	if !ok {
		m.log.Debug("image: serve miss", "key", key)
		http.NotFound(w, r)
		return
	}
	m.log.Debug("image: serve hit", "key", key, "bytes", len(e.data), "type", e.contentType)
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(e.data)
}

// rewriteImages replaces blob:/vscode-file: image srcs in extracted pane or
// popup HTML with /img/<hash> URLs. Each source URL is fetched from the live
// page once (via CDP) and cached; on fetch failure the original src is left
// untouched (the mirror shows a broken image for that one, as before).
func (m *Mirror) rewriteImages(s *cdp.Session, html string) string {
	for _, mth := range imgSrcRe.FindAllStringSubmatch(html, -1) {
		u := mth[1]
		key := hashStr(u)
		if _, ok := m.imgs.Load(key); !ok {
			m.log.Debug("image: fetch attempt", "url", u, "key", key)
			fStart := time.Now()
			data, ctype, via, err := cdp.FetchImage(s, u)
			if err != nil {
				m.log.Warn("image fetch failed", "url", u, "err", err)
				continue
			}
			if ctype == "" {
				ctype = guessImageType(data)
			}
			m.log.Debug("image: fetched", "key", key, "via", via, "bytes", len(data), "type", ctype, "ms", time.Since(fStart).Milliseconds())
			m.imgs.Store(key, imgEntry{data: data, contentType: ctype})
		}
		html = strings.ReplaceAll(html, `src="`+u+`"`, `src="/img/`+key+`"`)
	}
	return html
}

// guessImageType falls back to magic-byte sniffing when a blob fetch reports
// no content type.
func guessImageType(data []byte) string {
	switch {
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return "image/gif"
	case len(data) >= 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return "image/png"
	}
}

// imgCount returns the number of cached image resources.
func (m *Mirror) imgCount() int {
	n := 0
	m.imgs.All()(func(_ string, _ imgEntry) bool { n++; return true })
	return n
}
