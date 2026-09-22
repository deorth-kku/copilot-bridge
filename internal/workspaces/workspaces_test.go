package workspaces

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fixtureStorage is a minimal storage.json covering local workspaces
// (including a drive root), a remote workspace, and the windowsState
// open/last-active markers.
const fixtureStorage = `{
  "profileAssociations": {
    "workspaces": {
      "file:///c%3A/Users/deort/vscode-load-llama": "__default__profile__",
      "file:///e%3A/": "__default__profile__",
      "vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d": "__default__profile__"
    }
  },
  "windowsState": {
    "lastActiveWindow": { "folder": "file:///e%3A/" },
    "openedWindows": [
      { "folder": "file:///c%3A/Users/deort/vscode-load-llama" }
    ]
  }
}`

func writeFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(p, []byte(fixtureStorage), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestList(t *testing.T) {
	ws, err := List(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 {
		t.Fatalf("got %d workspaces, want 3: %+v", len(ws), ws)
	}
	// Open workspaces first, then local before remote, each group sorted
	// by name ("E:\" < "vscode-load-llama" case-insensitively).
	if ws[0].URI != "file:///c%3A/Users/deort/vscode-load-llama" || ws[1].URI != "file:///e%3A/" || ws[2].URI != "vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d" {
		t.Fatalf("wrong order: %+v", ws)
	}
	if runtime.GOOS == "windows" {
		if ws[0].Name != "vscode-load-llama" || ws[0].Path != `C:\Users\deort\vscode-load-llama` {
			t.Errorf("local: name %q path %q", ws[0].Name, ws[0].Path)
		}
		if ws[1].Name != "E:\\" || ws[1].Path != "E:\\" {
			t.Errorf("drive root: name %q path %q", ws[1].Name, ws[1].Path)
		}
	}
	if ws[2].Remote != "ssh-remote+pve" || ws[2].Path != "/etc/dnsmasq.d" || ws[2].Name != "pve:/etc/dnsmasq.d" {
		t.Errorf("remote: %+v", ws[2])
	}
	// Markers come from windowsState (exact URI match).
	if !ws[0].Open {
		t.Error("vscode-load-llama should be marked open")
	}
	if ws[1].Open || ws[2].Open {
		t.Error("only the opened workspace may be marked open")
	}
	if !ws[1].LastActive || ws[0].LastActive || ws[2].LastActive {
		t.Error("only the last-active workspace may be marked last-active")
	}
}

func TestListMissingFile(t *testing.T) {
	if _, err := List(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing storage.json")
	}
}

func TestListCorruptFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(p, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(p); err == nil {
		t.Fatal("expected an error for a corrupt storage.json")
	}
}

func TestLaunchArgs(t *testing.T) {
	cases := []struct {
		uri  string
		want []string
	}{
		{"file:///c%3A/Users/deort/vscode-load-llama", []string{"--folder-uri", "file:///c%3A/Users/deort/vscode-load-llama"}},
		{"vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d", []string{"--folder-uri", "vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d"}},
	}
	for _, c := range cases {
		got, err := LaunchArgs(c.uri)
		if err != nil {
			t.Errorf("LaunchArgs(%q): %v", c.uri, err)
			continue
		}
		if len(got) != len(c.want) || got[0] != c.want[0] || got[1] != c.want[1] {
			t.Errorf("LaunchArgs(%q) = %v, want %v", c.uri, got, c.want)
		}
	}
	for _, bad := range []string{"http://example.com/x", "ftp://x/y", "not a uri"} {
		if _, err := LaunchArgs(bad); err == nil {
			t.Errorf("LaunchArgs(%q): expected an error", bad)
		}
	}
}

func TestOverlay(t *testing.T) {
	known, err := List(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// Live windows contradict storage.json: one window has the drive-root
	// workspace open (storage says vscode-load-llama), and a second
	// window has a workspace storage.json does not know yet.
	live := map[string]string{
		"w1": "file:///e%3A/",
		"w2": "file:///c%3A/Users/deort/newws",
	}
	got := Overlay(known, live)
	// The three known workspaces plus the live-only one.
	if len(got) != 4 {
		t.Fatalf("got %d workspaces, want 4: %+v", len(got), got)
	}
	// Open first (the live state wins over storage.json's openedWindows),
	// local before remote, each group name-sorted.
	want := []struct {
		uri  string
		open bool
	}{
		{"file:///e%3A/", true},
		{"file:///c%3A/Users/deort/newws", true},
		{"file:///c%3A/Users/deort/vscode-load-llama", false},
		{"vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d", false},
	}
	for i, w := range want {
		if got[i].URI != w.uri || got[i].Open != w.open {
			t.Fatalf("got[%d] = %+v, want uri %q open %v", i, got[i], w.uri, w.open)
		}
	}
	// storage.json claimed vscode-load-llama open; the live probe must
	// have cleared it.
	for _, w := range got {
		if w.URI == "file:///c%3A/Users/deort/vscode-load-llama" && w.Open {
			t.Fatal("stale storage.json open flag must be cleared by the live probe")
		}
	}

	// No live windows: every known workspace is closed, order unchanged
	// (local name-sorted, then remote).
	got = Overlay(known, nil)
	if len(got) != 3 {
		t.Fatalf("got %d workspaces, want 3: %+v", len(got), got)
	}
	for _, w := range got {
		if w.Open {
			t.Fatalf("no live window, but %q is open: %+v", w.URI, w)
		}
	}
	if got[0].URI != "file:///e%3A/" || got[1].URI != "file:///c%3A/Users/deort/vscode-load-llama" || got[2].URI != "vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d" {
		t.Fatalf("wrong order: %+v", got)
	}

	// A live URI differing only in drive-letter case matches the known
	// entry (SameURI semantics).
	got = Overlay(known, map[string]string{"w1": "file:///E%3A/"})
	if len(got) != 3 || !got[0].Open || got[0].URI != "file:///e%3A/" {
		t.Fatalf("case-insensitive match failed: %+v", got)
	}
}

func TestSameURI(t *testing.T) {
	if !SameURI("file:///c%3A/Users/deort/vscode-load-llama", "file:///c%3A/Users/deort/vscode-load-llama") {
		t.Error("exact match must be true")
	}
	// Windows drive letters may differ in case between the two sources.
	if !SameURI("file:///c%3A/Users/deort/vscode-load-llama", "file:///C%3A/Users/deort/vscode-load-llama") {
		t.Error("case-insensitive drive letter must match")
	}
	if SameURI("file:///c%3A/a", "file:///c%3A/b") {
		t.Error("different paths must not match")
	}
	if SameURI("", "file:///c%3A/a") {
		t.Error("empty window URI must not match")
	}
}

// TestNormalizeAuthority pins the authority decoding: %2B must decode to
// '+' (net/url rejects it in the host), but a LITERAL '+' must survive —
// url.PathUnescape would turn it into a space and corrupt the authority.
func TestNormalizeAuthority(t *testing.T) {
	cases := []struct{ in, want string }{
		{"vscode-remote://ssh-remote%2Bpve/etc/dnsmasq.d", "vscode-remote://ssh-remote+pve/etc/dnsmasq.d"},
		{"vscode-remote://ssh-remote+pve/etc/dnsmasq.d", "vscode-remote://ssh-remote+pve/etc/dnsmasq.d"},
		{"file:///c%3A/Users/deort/vscode-load-llama", "file:///c%3A/Users/deort/vscode-load-llama"},
		{"not a uri", "not a uri"},
	}
	for _, c := range cases {
		if got := normalizeAuthority(c.in); got != c.want {
			t.Errorf("normalizeAuthority(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseWorkspaceLiteralPlus(t *testing.T) {
	// An unencoded '+' in the remote authority must not be turned into a
	// space (which would make the URI unparseable and drop the workspace).
	ws, ok := parseWorkspace("vscode-remote://ssh-remote+pve/etc/dnsmasq.d")
	if !ok {
		t.Fatalf("parseWorkspace failed for literal '+': %+v", ws)
	}
	if ws.Remote != "ssh-remote+pve" || ws.Name != "pve:/etc/dnsmasq.d" {
		t.Errorf("literal '+': %+v", ws)
	}
}

func TestPercentDecode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ssh-remote%2Bpve", "ssh-remote+pve", true},
		{"ssh-remote+pve", "ssh-remote+pve", true},
		{"%41%42c", "ABc", true},
		{"100%", "", false}, // dangling %
		{"%2", "", false},   // truncated escape
		{"%zz", "", false},  // non-hex digits
	}
	for _, c := range cases {
		got, ok := percentDecode(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("percentDecode(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
