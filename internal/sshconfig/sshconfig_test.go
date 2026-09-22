package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture mirrors the SHAPE of a real ssh config (fictional hosts).
const fixture = `# a comment line
host alpha
HostName alpha.example

Host BETA GAMMA
	HostName shared.example
host solo
HostName 192.0.2.10
Host ignored
User somebody
Port 2222
host trailing
`

func writeFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	cfg, err := Load(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// Fold the entries the way Match does: the last entry for a Host
	// pattern wins (a HostName line extends its block's patterns).
	byHost := map[string]string{}
	for _, e := range cfg.Entries {
		byHost[e.Host] = e.HostName
	}
	want := map[string]string{
		"alpha":    "alpha.example",
		"beta":     "shared.example",
		"gamma":    "shared.example",
		"solo":     "192.0.2.10",
		"ignored":  "",
		"trailing": "",
	}
	for h, hn := range want {
		if got := byHost[h]; got != hn {
			t.Errorf("host %q: HostName = %q, want %q", h, got, hn)
		}
	}
	if len(byHost) != len(want) {
		t.Errorf("parsed %d hosts, want %d: %v", len(byHost), len(want), byHost)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want an error for a missing config file")
	}
}

func TestMatch(t *testing.T) {
	cfg, err := Load(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		names []string
		want  string
		ok    bool
	}{
		{[]string{"alpha.example"}, "alpha", true},              // HostName -> its alias
		{[]string{"ALPHA.EXAMPLE"}, "alpha", true},              // case-insensitive
		{[]string{"beta"}, "beta", true},                        // alias itself
		{[]string{"unrelated.example", "gamma"}, "gamma", true}, // first matching name wins
		{[]string{"192.0.2.10"}, "solo", true},                  // numeric HostName -> its alias
		{[]string{"unrelated.example"}, "", false},
		{nil, "", false},
	}
	for _, c := range cases {
		got, ok := cfg.Match(c.names)
		if got != c.want || ok != c.ok {
			t.Errorf("Match(%v) = (%q, %v), want (%q, %v)", c.names, got, ok, c.want, c.ok)
		}
	}
}
