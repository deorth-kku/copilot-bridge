// Package loader sends POST {server root}/models/load requests with a
// per-model cooldown.
package loader

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"vscode-load-llama/internal/config"
)

// Loader deduplicates load requests per model id.
type Loader struct {
	cooldown time.Duration
	client   *http.Client
	log      *slog.Logger

	mu       sync.Mutex
	last     map[string]time.Time
	nonLlama map[string]struct{} // server roots proven to not be llama.cpp
}

// New creates a Loader. proxyFunc (from config.Settings.ProxyFunc)
// controls proxying of the load requests; nil means connect directly.
func New(cooldown time.Duration, log *slog.Logger, proxyFunc func(*http.Request) (*url.URL, error)) *Loader {
	// Don't follow redirects: a redirect (e.g. 308) already proves the
	// server is not llama.cpp, and following it can land on HTML pages.
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if proxyFunc != nil {
		client.Transport = &http.Transport{Proxy: proxyFunc}
	}
	return &Loader{
		cooldown: cooldown,
		client:   client,
		log:      log,
		last:     make(map[string]time.Time),
		nonLlama: make(map[string]struct{}),
	}
}

// Load requests a model load if the cooldown for m.ID has expired.
// The cooldown timestamp is refreshed when the request is SENT (not on
// success), so a dead server cannot be hammered. Server roots that answer
// with a code proving they are not llama.cpp are remembered and skipped
// entirely from then on.
func (l *Loader) Load(m config.Model) {
	// The load endpoint is relative to the server ROOT, not to baseUrl:
	// baseUrl http://abc.com/v1 -> POST http://abc.com/models/load
	u, err := url.Parse(m.BaseURL)
	if err != nil {
		l.log.Error("parse baseUrl", "model", m.ID, "err", err)
		return
	}
	root := u.Scheme + "://" + u.Host
	loadURL := root + "/models/load"

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.nonLlama[root]; ok {
		l.log.Debug("skip: not a llama.cpp endpoint", "model", m.ID, "url", root)
		return
	}
	if last, ok := l.last[m.ID]; ok && time.Since(last) < l.cooldown {
		l.log.Debug("cooldown active, skip", "model", m.ID)
		return
	}

	body, _ := json.Marshal(map[string]string{"model": m.ID})
	req, err := http.NewRequest(http.MethodPost, loadURL, bytes.NewReader(body))
	if err != nil {
		l.log.Error("build request failed", "model", m.ID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		l.log.Error("models/load failed", "model", m.ID, "url", loadURL, "err", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	loaded := l.classify(m, root, loadURL, resp.StatusCode, b)
	if loaded {
		l.last[m.ID] = time.Now()
	}
}

// loadResponse is the exact /models/load reply shape. Both replies
// share this struct; pointer fields plus DisallowUnknownFields make
// the match strict:
//
//	{"success": true}
//	{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}
type loadResponse struct {
	Success bool `json:"success"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// classify interprets the load response and logs it accordingly.
func (l *Loader) classify(m config.Model, root, loadURL string, status int, b []byte) bool {
	// Unmarshal once, before the switch: the success and "already
	// running" branches validate the same struct. Malformed JSON and
	// unknown fields fail the decode, so reset to zero-valued and let
	// both fall through to the warn below.
	var parsed loadResponse
	err := json.Unmarshal(b, &parsed)
	if err == nil {
		switch {
		case status >= 200 && status < 300 && parsed.Success:
			l.log.Info("model loaded", "model", m.ID, "url", loadURL)
			return true
		case status == http.StatusBadRequest && parsed.Error.Message == "model is already running":
			// normal for llama.cpp: the model is already loaded
			l.log.Info("model already running", "model", m.ID, "url", loadURL)
			return true
		case status == http.StatusNotFound && parsed.Error.Message == "File Not Found":
			l.log.Info("model not found", "model", m.ID, "url", loadURL)
			return false
		}
	}
	switch status {
	case
		http.StatusMultipleChoices,
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusNotModified,
		http.StatusUseProxy,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect: // openrouter use redirect
		fallthrough
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnauthorized, http.StatusForbidden:
		// no /models/load route (or auth-walled): not llama.cpp,
		// remember the endpoint and stop trying
		l.nonLlama[root] = struct{}{}
		l.log.Info("endpoint is not llama.cpp, will skip", "model", m.ID, "url", root, "status", status)
		return false
	}
	l.log.Warn("models/load unexpected response", "model", m.ID, "url", loadURL,
		"status", status, "resp", string(b))
	return false
}
