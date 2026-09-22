// Package sshconfig parses a minimal subset of the SSH client config
// (~/.ssh/config): the Host/HostName pairs that let the bridge identify
// which machine an incoming request came from. The request's source IP is
// forward-DNS'd and the resulting host names are matched against the known
// Host aliases and HostName values (the same config VS Code uses for its
// ssh-remote workspaces).
package sshconfig

import (
	"bufio"
	"os"
	"strings"
)

// Entry is one Host pattern and the HostName it maps to. An empty HostName
// means the alias IS the machine name (the ssh default).
type Entry struct {
	Host     string // Host pattern, lowercased
	HostName string // HostName value, lowercased ("" = the alias itself)
}

// Config is the parsed ssh client config.
type Config struct {
	Entries []Entry
}

// Load reads and parses the ssh config at path. Only the Host and HostName
// keywords are honored (case-insensitive); a Host line may carry several
// patterns (space-separated) and a HostName applies to every pattern of
// its block. Comments (#), blank lines, and every other keyword are
// ignored, so the parser never fails on an unknown directive.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	var block []string // Host patterns the pending HostName applies to
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToLower(fields[0]) {
		case "host":
			if len(fields) < 2 {
				continue
			}
			block = nil
			for _, p := range fields[1:] {
				p = strings.ToLower(p)
				cfg.Entries = append(cfg.Entries, Entry{Host: p})
				block = append(block, p)
			}
		case "hostname":
			if len(fields) < 2 || len(block) == 0 {
				continue
			}
			name := strings.ToLower(fields[1])
			for _, p := range block {
				cfg.Entries = append(cfg.Entries, Entry{Host: p, HostName: name})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Match reports which machine (ssh Host alias) the given names identify.
// The names are the forward-DNS results of the request's source IP; each is
// compared case-insensitively against every entry's Host alias and
// HostName. It returns the ssh Host alias of the first matching entry — the
// identifier VS Code uses in its ssh-remote authority (e.g. "pve" for
// ssh-remote+pve) — not the DNS name itself. ok is false when no name is
// known.
func (c *Config) Match(names []string) (string, bool) {
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		for _, e := range c.Entries {
			if n == e.Host || (e.HostName != "" && n == e.HostName) {
				return e.Host, true
			}
		}
	}
	return "", false
}
