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
	"time"

	"github.com/go4org/hashtriemap"
	"golang.org/x/sync/singleflight"

	"copilot-bridge/internal/config"
)

// Loader deduplicates load requests per (server, model) pair.
type Loader struct {
	cooldown time.Duration
	client   *http.Client
	log      *slog.Logger

	// unloadOthers, when true, makes Load send a /models/unload request to
	// every other llama.cpp model (the `others` argument) before loading the
	// requested one, so only the active model occupies the server.
	unloadOthers bool

	// last records when each (server, model) load last succeeded, keyed by
	// the same composite key used for the in-flight dedup. It is a lock-free
	// hash-trie map, so its Load/Store never block and are never held across
	// the (up to 10s) HTTP request.
	last hashtriemap.HashTrieMap[string, time.Time]

	// sf dedups concurrent load requests for the same (server, model) pair:
	// only one HTTP request runs at a time per pair; a caller that arrives
	// while one is in flight waits for it and shares the outcome instead of
	// firing a duplicate request.
	sf singleflight.Group
}

// New creates a Loader. proxyFunc (from config.Settings.ProxyFunc)
// controls proxying of the load requests; nil means connect directly.
// unloadOthers, when true, makes Load unload every other llama.cpp model
// (the `others` argument) before loading the requested one.
func New(cooldown time.Duration, log *slog.Logger, proxyFunc func(*http.Request) (*url.URL, error), unloadOthers bool) *Loader {
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
		cooldown:     cooldown,
		client:       client,
		log:          log,
		unloadOthers: unloadOthers,
	}
}

// UnloadOthers reports whether the loader was created with unloadOthers
// enabled, so callers can decide whether to gather the list of other models
// to pass to Load.
func (l *Loader) UnloadOthers() bool { return l.unloadOthers }

// Load requests a model load if the cooldown for this (server, model)
// pair has expired. The cooldown timestamp is armed only on a successful
// reply (success or "already running"), so a genuine failure or a dead
// server does not suppress the next retry.
//
// A model id alone is NOT a unique key: the same id can be served by
// several different servers (e.g. a cloud baseUrl and a local backup).
// The (server root, model id) pair is what identifies a distinct load,
// so both the cooldown and the in-flight dedup are keyed by it.
//
// The cooldown bookkeeping lives in the lock-free `last` hash-trie map, so
// its Load/Store never block and are never held across the (up to 10s)
// request. Concurrent requests for the same (server, model) pair are
// deduped by singleflight: the first caller runs the request, and any
// caller that arrives while it is in flight waits for it and shares the
// outcome rather than firing a duplicate request.
//
// `others` is a set of other llama.cpp server BaseURLs to query before this
// load, when the loader was created with unloadOthers enabled. For each
// server, the loader GETs /models to discover which models are actually
// loaded, then unloads those (excluding the target model). It is nil
// (or empty) otherwise.
func (l *Loader) Load(m config.Model, others map[string]struct{}) {
	// The load endpoint is relative to the server ROOT, not to baseUrl:
	// baseUrl http://abc.com/v1 -> POST http://abc.com/models/load
	u, err := url.Parse(m.BaseURL)
	if err != nil {
		l.log.Error("parse baseUrl", "model", m.ID, "err", err)
		return
	}
	root := u.Scheme + "://" + u.Host
	loadURL := root + "/models/load"
	// Unique key for this load: the server root plus the model id. The
	// id can repeat across servers, so it must be combined with the root.
	key := root + "\x00" + m.ID

	if last, ok := l.last.Load(key); ok && time.Since(last) < l.cooldown {
		l.log.Debug("cooldown active, skip", "model", m.ID)
		return
	}

	// Dedup concurrent loads for this (server, model) pair. The closure
	// runs at most once per in-flight window; its return value is ignored
	// (logging happens inside), only the dedup matters.
	_, _, _ = l.sf.Do(key, func() (any, error) {
		// Best-effort: before loading this model, query each other llama.cpp
		// server for its currently-loaded models and unload them (excluding
		// the target). Failures are logged and never block the load.
		if l.unloadOthers {
			for baseURL := range others {
				omU, err := url.Parse(baseURL)
				if err != nil {
					continue
				}
				omRoot := omU.Scheme + "://" + omU.Host
				loadedIDs := l.listLoadedModels(omRoot)
				for _, id := range loadedIDs {
					// Skip only the target on the target's own server: the same
					// model id can be served by several servers, so a loaded
					// model with the target's id on a *different* server is a
					// distinct model and must still be unloaded.
					if omRoot == root && id == m.ID {
						continue
					}
					l.unload(omRoot, id)
				}
			}
		}
		body, _ := json.Marshal(map[string]string{"model": m.ID})
		req, err := http.NewRequest(http.MethodPost, loadURL, bytes.NewReader(body))
		if err != nil {
			l.log.Error("build request failed", "model", m.ID, "err", err)
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := l.client.Do(req)
		if err != nil {
			l.log.Error("models/load failed", "model", m.ID, "url", loadURL, "err", err)
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		loaded := l.classify(m, loadURL, resp.StatusCode, b)
		if loaded {
			l.last.Store(key, time.Now())
		}
		return nil, nil
	})
}

// listLoadedModels queries GET {root}/models and returns the IDs of models
// whose status is "loaded". Best-effort: any failure returns nil.
func (l *Loader) listLoadedModels(root string) []string {
	listURL := root + "/models"
	req, err := http.NewRequest(http.MethodGet, listURL, nil)
	if err != nil {
		l.log.Error("list models: build request failed", "url", listURL, "err", err)
		return nil
	}
	resp, err := l.client.Do(req)
	if err != nil {
		l.log.Error("list models: request failed", "url", listURL, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		l.log.Warn("list models: unexpected status", "url", listURL, "status", resp.StatusCode)
		return nil
	}

	var parsed struct {
		Data []struct {
			ID     string `json:"id"`
			Status struct {
				Value string `json:"value"`
			} `json:"status"`
		} `json:"data"`
	}
	// Stream-parse directly from the response body so a large model list is
	// not truncated by a fixed read cap.
	if err := json.UnmarshalRead(resp.Body, &parsed); err != nil {
		l.log.Error("list models: parse failed", "url", listURL, "err", err)
		return nil
	}

	var loaded []string
	for _, item := range parsed.Data {
		if item.Status.Value == "loaded" {
			loaded = append(loaded, item.ID)
		}
	}
	return loaded
}

// unload requests a model unload on the server root for the given model ID.
// It is best-effort: any failure is logged but never propagated to the
// caller, so a dead or non-llama server cannot block the load that follows.
func (l *Loader) unload(root, modelID string) {
	unloadURL := root + "/models/unload"
	body, _ := json.Marshal(map[string]string{"model": modelID})
	req, err := http.NewRequest(http.MethodPost, unloadURL, bytes.NewReader(body))
	if err != nil {
		l.log.Error("unload: build request failed", "model", modelID, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		l.log.Error("models/unload failed", "model", modelID, "url", unloadURL, "err", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if status := resp.StatusCode; status >= 200 && status < 300 {
		l.log.Info("model unloaded", "model", modelID, "url", unloadURL)
	} else {
		l.log.Warn("models/unload unexpected response", "model", modelID, "url", unloadURL,
			"status", status, "resp", string(b))
	}
}

// loadResponse is the /models/load reply shape. Both known replies share
// this struct:
//
//	{"success": true}
//	{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}
//
// Matching is deliberately loose (plain Unmarshal, unknown fields
// ignored): the status code plus the success flag / exact error message
// identifies a genuine llama.cpp reply, and anything else falls through
// to the warn in classify.
type loadResponse struct {
	Success bool `json:"success"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// classify interprets the load response and logs it accordingly.
func (l *Loader) classify(m config.Model, loadURL string, status int, b []byte) bool {
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
	l.log.Warn("models/load unexpected response", "model", m.ID, "url", loadURL,
		"status", status, "resp", string(b))
	return false
}
