package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	content := `{
  "oaicopilot.models": [
    {"id": "__provider__local", "displayName": "Local"},
    {"id": "Qwen3.8-27B", "displayName": "Qwen3.8 27B", "baseUrl": "http://127.0.0.1:8080/", "optimization": "llama.cpp"},
    {"id": "Ornith-1.5-35B", "baseUrl": "http://10.0.0.5:8080"}
  ]
}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}

	// displayName key with trailing slash trimmed from baseUrl
	if got := s.Models["Qwen3.8 27B"]; got.ID != "Qwen3.8-27B" || got.BaseURL != "http://127.0.0.1:8080" {
		t.Errorf("displayName key wrong: %+v", got)
	}
	// optimization field parsed
	if got := s.Models["Qwen3.8 27B"]; got.Optimization != "llama.cpp" {
		t.Errorf("optimization wrong: %+v", got)
	}
	// id alias key
	if got := s.Models["Qwen3.8-27B"]; got.BaseURL != "http://127.0.0.1:8080" {
		t.Errorf("id alias key wrong: %+v", got)
	}
	// entry without displayName keyed by id only
	if got := s.Models["Ornith-1.5-35B"]; got.BaseURL != "http://10.0.0.5:8080" {
		t.Errorf("no-displayName entry wrong: %+v", got)
	}
	// absent optimization stays empty
	if got := s.Models["Ornith-1.5-35B"]; got.Optimization != "" {
		t.Errorf("expected empty optimization, got %q", got.Optimization)
	}
	// provider header skipped
	if _, ok := s.Models["__provider__local"]; ok {
		t.Error("provider header should be skipped")
	}
	if len(s.Models) != 3 {
		t.Errorf("expected 3 keys, got %d: %v", len(s.Models), s.Models)
	}
}

func TestProxyFunc(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	content := `{
  "oaicopilot.models": [
    {"id": "m1", "baseUrl": "http://127.0.0.1:8080"}
  ],
  "http.proxy": "http://proxy.lan:1080",
  "http.noProxy": ["qwen.lan", ".chatapi.deorth.xyz"]
}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	fn := s.ProxyFunc()
	if fn == nil {
		t.Fatal("expected non-nil proxy func")
	}
	check := func(host, wantProxy string) {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/models/load", nil)
		got, err := fn(req)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		gotStr := ""
		if got != nil {
			gotStr = got.String()
		}
		if gotStr != wantProxy {
			t.Errorf("%s: got proxy %q, want %q", host, gotStr, wantProxy)
		}
	}
	// noProxy: exact host and subdomain (leading-dot entry too)
	check("qwen.lan", "")
	check("foo.qwen.lan", "")
	check("chatapi.deorth.xyz", "")
	check("a.chatapi.deorth.xyz", "")
	// everything else goes through the proxy
	check("33dank.com", "http://proxy.lan:1080")
	check("openrouter.ai", "http://proxy.lan:1080")
}

func TestProxyFuncAbsent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	// no http.proxy / http.noProxy at all
	if err := os.WriteFile(p, []byte(`{"oaicopilot.models": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if fn := s.ProxyFunc(); fn != nil {
		t.Errorf("expected nil proxy func when http.proxy is absent")
	}
	if s.Proxy != "" || len(s.NoProxy) != 0 {
		t.Errorf("expected empty proxy settings, got %q %v", s.Proxy, s.NoProxy)
	}
}

// TestLoadRealSettings guards the flat-key ("oaicopilot.models") parsing
// against the real VS Code settings.json. Skipped when the file is absent.
func TestLoadRealSettings(t *testing.T) {
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		t.Skip("not on Windows")
	}
	p := filepath.Join(appdata, "Code", "User", "settings.json")
	if _, err := os.Stat(p); err != nil {
		t.Skip("real settings.json not found")
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Models) == 0 {
		t.Fatal("expected at least one model from real settings")
	}
}
