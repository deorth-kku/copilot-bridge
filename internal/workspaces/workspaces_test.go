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
