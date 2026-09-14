// Package config loads the VS Code settings.json and builds the model
// lookup table used to map a displayed model name to its id + baseUrl.
// The Store wrapper (watch.go) hot-reloads the file via fsnotify and
// publishes each snapshot through an atomic pointer.
package config

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Model is a loadable model entry from oaicopilot.models.
type Model struct {
	ID      string
	BaseURL string
}

// Settings holds the lookup table: key is the display name shown in the
// UI (and, as an alias, the raw id). Proxy/NoProxy mirror VS Code's
// http.proxy / http.noProxy settings (both optional).
type Settings struct {
	Models  map[string]Model
	Proxy   string
	NoProxy []string
}

type rawModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	BaseURL     string `json:"baseUrl"`
}

// Load parses path and returns the model table. Entries whose id starts
// with "__provider__" are provider group headers and are skipped.
// Entries without a displayName are keyed by id only.
func Load(path string) (*Settings, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	// VS Code settings.json uses the FLAT key "oaicopilot.models"
	// (a single top-level string key), not a nested object.
	var raw struct {
		Models  []rawModel `json:"oaicopilot.models"`
		Proxy   string     `json:"http.proxy"`
		NoProxy []string   `json:"http.noProxy"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	s := &Settings{
		Models:  make(map[string]Model),
		Proxy:   strings.TrimSpace(raw.Proxy),
		NoProxy: raw.NoProxy,
	}
	for _, m := range raw.Models {
		if m.ID == "" || strings.HasPrefix(m.ID, "__provider__") {
			continue
		}
		if m.BaseURL == "" {
			continue
		}
		model := Model{ID: m.ID, BaseURL: strings.TrimRight(m.BaseURL, "/")}
		s.Models[m.ID] = model
		if name := strings.TrimSpace(m.DisplayName); name != "" {
			s.Models[name] = model
		}
	}
	return s, nil
}

// ProxyFunc returns an http.Transport.Proxy function implementing the
// VS Code http.proxy / http.noProxy settings. It returns nil when no
// proxy is configured, in which case the HTTP client connects directly.
// noProxy entries are host suffixes: "qwen.lan" matches qwen.lan and
// any *.qwen.lan host. Wildcards follow VS Code: "*" (or "*.domain")
// matches everything (resp. that domain and its subdomains).
func (s *Settings) ProxyFunc() func(*http.Request) (*url.URL, error) {
	if s == nil || s.Proxy == "" {
		return nil
	}
	proxy, err := url.Parse(s.Proxy)
	if err != nil {
		return nil
	}
	wildcard := false
	var noProxy []string
	for _, h := range s.NoProxy {
		if h = strings.TrimSpace(h); h == "*" {
			wildcard = true
			continue
		}
		h = strings.TrimPrefix(h, "*.")
		if h = strings.TrimPrefix(h, "."); h != "" {
			noProxy = append(noProxy, h)
		}
	}
	return func(req *http.Request) (*url.URL, error) {
		if wildcard {
			return nil, nil // noProxy "*": direct connection everywhere
		}
		host := req.URL.Hostname()
		for _, h := range noProxy {
			if host == h || strings.HasSuffix(host, "."+h) {
				return nil, nil // direct connection
			}
		}
		return proxy, nil
	}
}
