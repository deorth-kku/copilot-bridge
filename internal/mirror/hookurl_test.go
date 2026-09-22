package mirror

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/shutdown"
	"copilot-bridge/internal/workspaces"
)

// hookurlStorage is a storage.json with one local and one remote workspace
// (fictional paths and hosts).
const hookurlStorage = `{
  "profileAssociations": {
    "workspaces": {
      "file:///c%3A/Users/tester/demo": "__default__profile__",
      "vscode-remote://ssh-remote%2Balpha/home/tester/demo": "__default__profile__"
    }
  }
}`

// hookurlSSHConfig is a minimal ssh config (fictional hosts).
const hookurlSSHConfig = `host alpha
HostName alpha.example
`

func writeHookurlFixtures(t *testing.T) (storagePath, sshPath string) {
	t.Helper()
	dir := t.TempDir()
	storagePath = filepath.Join(dir, "storage.json")
	sshPath = filepath.Join(dir, "ssh-config")
	if err := os.WriteFile(storagePath, []byte(hookurlStorage), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sshPath, []byte(hookurlSSHConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return storagePath, sshPath
}

// stubLookupHosts replaces the forward-DNS function for the test (a real
// lookup depends on the machine's DNS, which tests must not touch).
func stubLookupHosts(t *testing.T, names []string, err error) {
	t.Helper()
	orig := lookupHosts
	lookupHosts = func(ip string) ([]string, error) { return names, err }
	t.Cleanup(func() { lookupHosts = orig })
}

func TestMatchWorkspace(t *testing.T) {
	known := []workspaces.Workspace{
		{URI: "file:///c%3A/Users/tester/demo", Path: `C:\Users\tester\demo`},
		{URI: "vscode-remote://ssh-remote%2Balpha/home/tester/demo", Remote: "ssh-remote+alpha", Path: "/home/tester/demo"},
	}
	if ws, ok := matchWorkspace(known, "", `c:\users\tester\demo`); !ok || ws.Path != `C:\Users\tester\demo` {
		t.Fatalf("local match: got (%+v, %v)", ws, ok)
	}
	if _, ok := matchWorkspace(known, "", "/home/tester/demo"); ok {
		t.Fatal("a remote path must not match on the local machine")
	}
	if ws, ok := matchWorkspace(known, "alpha", "/home/tester/demo"); !ok || ws.Remote != "ssh-remote+alpha" {
		t.Fatalf("remote match: got (%+v, %v)", ws, ok)
	}
	if _, ok := matchWorkspace(known, "beta", "/home/tester/demo"); ok {
		t.Fatal("a workspace of another machine must not match")
	}
	if _, ok := matchWorkspace(known, "alpha", `C:\Users\tester\demo`); ok {
		t.Fatal("a local path must not match on a remote machine")
	}
	if _, ok := matchWorkspace(known, "", ""); ok {
		t.Fatal("an empty cwd must not match")
	}
}

func TestHookMirrorURL(t *testing.T) {
	storagePath, sshPath := writeHookurlFixtures(t)
	st, err := workspaces.NewStore(storagePath, discardLog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &Mirror{log: discardLog(), storagePath: storagePath, sshConfigPath: sshPath, wsStore: st}

	localURI := "file:///c%3A/Users/tester/demo"
	remoteURI := "vscode-remote://ssh-remote%2Balpha/home/tester/demo"

	// Local request: loopback peer, no forwarded headers.
	r := httptest.NewRequest(http.MethodPost, "/api/hook", nil)
	r.Host = "mirror.lan:9527"
	r.RemoteAddr = "127.0.0.1:54321"
	if got := m.hookMirrorURL(r, `C:\Users\tester\demo`); got != "http://mirror.lan:9527/?ws="+url.QueryEscape(localURI) {
		t.Fatalf("local: got %q", got)
	}

	// Remote request: the source IP forward-resolves to a known host.
	stubLookupHosts(t, []string{"alpha.example"}, nil)
	r2 := httptest.NewRequest(http.MethodPost, "/api/hook", nil)
	r2.Host = "mirror.lan:9527"
	r2.RemoteAddr = "192.0.2.1:54322"
	if got := m.hookMirrorURL(r2, "/home/tester/demo"); got != "http://mirror.lan:9527/?ws="+url.QueryEscape(remoteURI) {
		t.Fatalf("remote: got %q", got)
	}

	// Behind a reverse proxy: the forwarded headers carry the client's
	// URL and IP.
	stubLookupHosts(t, []string{"alpha.example"}, nil)
	r3 := httptest.NewRequest(http.MethodPost, "/api/hook", nil)
	r3.Host = "127.0.0.1:9527"
	r3.RemoteAddr = "127.0.0.1:54323"
	r3.Header.Set("X-Forwarded-Host", "mirror.lan:8443")
	r3.Header.Set("X-Forwarded-Proto", "https, http")
	r3.Header.Set("X-Forwarded-For", "192.0.2.1, 127.0.0.1")
	if got := m.hookMirrorURL(r3, "/home/tester/demo"); got != "https://mirror.lan:8443/?ws="+url.QueryEscape(remoteURI) {
		t.Fatalf("forwarded: got %q", got)
	}

	// DNS failure: treated as the local machine.
	stubLookupHosts(t, nil, errors.New("no PTR record"))
	r4 := httptest.NewRequest(http.MethodPost, "/api/hook", nil)
	r4.Host = "mirror.lan:9527"
	r4.RemoteAddr = "192.0.2.1:54324"
	if got := m.hookMirrorURL(r4, `C:\Users\tester\demo`); got != "http://mirror.lan:9527/?ws="+url.QueryEscape(localURI) {
		t.Fatalf("dns failure: got %q", got)
	}

	// No workspace match: the default mirror page.
	if got := m.hookMirrorURL(r, "/nonexistent"); got != "http://mirror.lan:9527/" {
		t.Fatalf("fallback: got %q", got)
	}
}

// TestHandleHookMirrorEndToEnd exercises the full endpoint: the response
// carries the mirror URL of the workspace whose cwd the payload names,
// with the site/scheme of the request itself.
func TestHandleHookMirrorEndToEnd(t *testing.T) {
	storagePath, sshPath := writeHookurlFixtures(t)
	disc := cdp.NewDiscovery("127.0.0.1:1", make(chan cdp.Event, 1), discardLog(), 50)
	m := New(disc, discardLog(), "127.0.0.1:0", nil, "", storagePath, "", sshPath)
	m.SetPlanner(shutdown.NewPlanner(time.Second, nil))
	stubLookupHosts(t, []string{"alpha.example"}, nil)

	localURI := "file:///c%3A/Users/tester/demo"
	remoteURI := "vscode-remote://ssh-remote%2Balpha/home/tester/demo"
	localBody := `{"hook_event_name":"Stop","cwd":"C:\\Users\\tester\\demo"}`
	remoteBody := `{"hook_event_name":"Stop","cwd":"/home/tester/demo"}`

	type hookResp struct {
		OK     bool   `json:"ok"`
		Mirror string `json:"mirror"`
	}
	post := func(t *testing.T, url, body string, hdr http.Header) hookResp {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if hdr != nil {
			req.Header = hdr
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST status = %d", resp.StatusCode)
		}
		data, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatal(rerr)
		}
		var out hookResp
		if jerr := json.Unmarshal(data, &out); jerr != nil {
			t.Fatal(jerr)
		}
		return out
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.handleHook(w, r)
	}))
	defer srv.Close()

	// Local client: the URL keeps the request's site.
	out := post(t, srv.URL+"/api/hook", localBody, nil)
	if !out.OK || out.Mirror != srv.URL+"/?ws="+url.QueryEscape(localURI) {
		t.Fatalf("local: got %+v", out)
	}

	// Remote client behind a proxy: XFF carries the client IP that DNS
	// maps to the remote machine.
	out = post(t, srv.URL+"/api/hook", remoteBody, http.Header{"X-Forwarded-For": []string{"192.0.2.1"}})
	if !out.OK || out.Mirror != srv.URL+"/?ws="+url.QueryEscape(remoteURI) {
		t.Fatalf("remote: got %+v", out)
	}

	// No matching cwd: the default mirror page.
	out = post(t, srv.URL+"/api/hook", `{"hook_event_name":"Stop","cwd":"/nowhere"}`, nil)
	if !out.OK || out.Mirror != srv.URL+"/" {
		t.Fatalf("fallback: got %+v", out)
	}

	// TLS request: the URL keeps the https scheme.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.handleHook(w, r)
	}))
	defer tlsSrv.Close()
	req, err := http.NewRequest(http.MethodPost, tlsSrv.URL+"/api/hook", strings.NewReader(localBody))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tlsSrv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var out2 hookResp
	if jerr := json.Unmarshal(data, &out2); jerr != nil {
		t.Fatal(jerr)
	}
	if !out2.OK || out2.Mirror != tlsSrv.URL+"/?ws="+url.QueryEscape(localURI) {
		t.Fatalf("tls: got %+v", out2)
	}
}
