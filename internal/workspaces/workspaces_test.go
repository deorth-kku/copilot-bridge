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
      "file:///c%3A/Users/user0/copilot-bridge": "__default__profile__",
      "file:///e%3A/": "__default__profile__",
      "vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d": "__default__profile__"
    }
  },
  "windowsState": {
    "lastActiveWindow": { "folder": "file:///e%3A/" },
    "openedWindows": [
      { "folder": "file:///c%3A/Users/user0/copilot-bridge" }
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
	// Open workspaces first (copilot-bridge is the open one), then local
	// before remote, each group sorted by name ("copilot-bridge" < "E:\"
	// case-insensitively).
	if ws[0].URI != "file:///c%3A/Users/user0/copilot-bridge" || ws[1].URI != "file:///e%3A/" || ws[2].URI != "vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d" {
		t.Fatalf("wrong order: %+v", ws)
	}
	if runtime.GOOS == "windows" {
		if ws[0].Name != "copilot-bridge" || ws[0].Path != `C:\Users\user0\copilot-bridge` {
			t.Errorf("local: name %q path %q", ws[0].Name, ws[0].Path)
		}
		if ws[1].Name != "E:\\" || ws[1].Path != "E:\\" {
			t.Errorf("drive root: name %q path %q", ws[1].Name, ws[1].Path)
		}
	}
	if ws[2].Remote != "ssh-remote+debian" || ws[2].Path != "/etc/dnsmasq.d" || ws[2].Name != "debian:/etc/dnsmasq.d" {
		t.Errorf("remote: %+v", ws[2])
	}
	// Markers come from windowsState (exact URI match).
	if !ws[0].Open {
		t.Error("copilot-bridge should be marked open")
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
		{"file:///c%3A/Users/user0/copilot-bridge", []string{"--folder-uri", "file:///c%3A/Users/user0/copilot-bridge"}},
		{"vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d", []string{"--folder-uri", "vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d"}},
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
	// workspace open (storage says copilot-bridge), and a second
	// window has a workspace storage.json does not know yet.
	live := map[string]string{
		"w1": "file:///e%3A/",
		"w2": "file:///c%3A/Users/user0/newws",
	}
	got := Overlay(known, live)
	// The three known workspaces plus the live-only one.
	if len(got) != 4 {
		t.Fatalf("got %d workspaces, want 4: %+v", len(got), got)
	}
	// Open first (the live state wins over storage.json's openedWindows),
	// local before remote, each group name-sorted; the live window ids
	// ride along (the workspaces page passes them to the mirror page).
	want := []struct {
		uri  string
		open bool
		win  string
	}{
		{"file:///e%3A/", true, "w1"},
		{"file:///c%3A/Users/user0/newws", true, "w2"},
		{"file:///c%3A/Users/user0/copilot-bridge", false, ""},
		{"vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d", false, ""},
	}
	for i, w := range want {
		if got[i].URI != w.uri || got[i].Open != w.open || got[i].Window != w.win {
			t.Fatalf("got[%d] = %+v, want uri %q open %v win %q", i, got[i], w.uri, w.open, w.win)
		}
	}
	// storage.json claimed copilot-bridge open; the live probe must
	// have cleared it.
	for _, w := range got {
		if w.URI == "file:///c%3A/Users/user0/copilot-bridge" && w.Open {
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
	if got[0].URI != "file:///c%3A/Users/user0/copilot-bridge" || got[1].URI != "file:///e%3A/" || got[2].URI != "vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d" {
		t.Fatalf("wrong order: %+v", got)
	}

	// A live URI differing only in drive-letter case matches the known
	// entry (SameURI semantics).
	got = Overlay(known, map[string]string{"w1": "file:///E%3A/"})
	if len(got) != 3 || !got[0].Open || got[0].URI != "file:///e%3A/" || got[0].Window != "w1" {
		t.Fatalf("case-insensitive match failed: %+v", got)
	}

	// Several windows sharing one workspace: the smallest id wins
	// (deterministic, unlike map iteration order).
	got = Overlay(known, map[string]string{"w9": "file:///e%3A/", "w2": "file:///e%3A/"})
	if len(got) != 3 || !got[0].Open || got[0].Window != "w2" {
		t.Fatalf("shared workspace: %+v", got)
	}
}

func TestSameURI(t *testing.T) {
	if !SameURI("file:///c%3A/Users/user0/copilot-bridge", "file:///c%3A/Users/user0/copilot-bridge") {
		t.Error("exact match must be true")
	}
	// Windows drive letters may differ in case between the two sources.
	if !SameURI("file:///c%3A/Users/user0/copilot-bridge", "file:///C%3A/Users/user0/copilot-bridge") {
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
		{"vscode-remote://ssh-remote%2Bdebian/etc/dnsmasq.d", "vscode-remote://ssh-remote+debian/etc/dnsmasq.d"},
		{"vscode-remote://ssh-remote+debian/etc/dnsmasq.d", "vscode-remote://ssh-remote+debian/etc/dnsmasq.d"},
		{"file:///c%3A/Users/user0/copilot-bridge", "file:///c%3A/Users/user0/copilot-bridge"},
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
	ws, ok := parseWorkspace("vscode-remote://ssh-remote+debian/etc/dnsmasq.d")
	if !ok {
		t.Fatalf("parseWorkspace failed for literal '+': %+v", ws)
	}
	if ws.Remote != "ssh-remote+debian" || ws.Name != "debian:/etc/dnsmasq.d" {
		t.Errorf("literal '+': %+v", ws)
	}
}

func TestPercentDecode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ssh-remote%2Bdebian", "ssh-remote+debian", true},
		{"ssh-remote+debian", "ssh-remote+debian", true},
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
