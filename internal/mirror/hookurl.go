package mirror

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"copilot-bridge/internal/sshconfig"
	"copilot-bridge/internal/workspaces"
)

// hookMirrorURL resolves the mirror URL of the workspace the hook event
// belongs to, so the /api/hook response can point the user straight at it:
//
//	<scheme>://<site>?ws=<workspace URI>
//
// The site and scheme are the ones the REQUEST used (X-Forwarded-Host /
// X-Forwarded-Proto when present, else r.Host / the TLS state), so the
// answer stays correct behind a reverse proxy. The machine is identified
// from the request's source IP: loopback is the local machine; any other
// IP is forward-DNS'd and matched against the ssh client config's Host
// aliases / HostName values (the same config VS Code uses for its
// ssh-remote workspaces). The workspace is the one whose root path equals
// the payload's cwd on that machine. When nothing matches, the default
// mirror page (no ws=) is returned instead.
func (m *Mirror) hookMirrorURL(r *http.Request, cwd string) string {
	scheme := "http"
	if p := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); p != "" {
		scheme = p
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	base := scheme + "://" + host

	peer := firstHeaderValue(r.Header.Get("X-Forwarded-For"))
	if peer == "" {
		peer = r.RemoteAddr
	}
	peerIP := peer
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peerIP = h
	}
	remoteHost := ""
	if ip := net.ParseIP(peerIP); ip == nil || !ip.IsLoopback() {
		if names, err := lookupHosts(peerIP); err != nil {
			m.log.Debug("hook: forward DNS failed", "ip", peerIP, "err", err)
		} else if h, ok := m.matchSSHHost(names); ok {
			remoteHost = h
		}
	}

	if ws, ok := matchWorkspace(m.knownWorkspaces(), remoteHost, cwd); ok {
		m.log.Info("hook: mirror url", "peer", peer, "host", remoteHost, "uri", ws.URI)
		return base + "/?ws=" + url.QueryEscape(ws.URI)
	}
	m.log.Info("hook: mirror url (no workspace match)", "peer", peer, "host", remoteHost, "cwd", cwd)
	return base + "/"
}

// lookupHosts forward-resolves an IP to its host names (2s cap). It is a
// package variable so tests can stub it (a real lookup depends on the
// machine's DNS, which tests must not touch).
var lookupHosts = func(ip string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, ip)
}

// matchSSHHost loads the ssh client config (fresh on every hook event: the
// events are rare and the file is small, so no watcher is needed) and
// reports which machine the given host names identify. An unreadable or
// missing config means no match (the caller falls back to the local
// machine).
func (m *Mirror) matchSSHHost(names []string) (string, bool) {
	if m.sshConfigPath == "" {
		return "", false
	}
	cfg, err := sshconfig.Load(m.sshConfigPath)
	if err != nil {
		m.log.Warn("hook: ssh config unreadable", "path", m.sshConfigPath, "err", err)
		return "", false
	}
	return cfg.Match(names)
}

// knownWorkspaces returns the in-memory workspace list (the hot-reloaded
// storage.json snapshot), falling back to a fresh read when the store is
// nil (the initial load failed).
func (m *Mirror) knownWorkspaces() []workspaces.Workspace {
	if m.wsStore != nil {
		return m.wsStore.Load()
	}
	known, err := workspaces.List(m.storagePath)
	if err != nil {
		m.log.Warn("hook: workspace list failed", "err", err)
		return nil
	}
	return known
}

// matchWorkspace finds the workspace whose root path equals cwd on the
// given machine. host == "" means the local machine: only local
// workspaces qualify. A non-empty host means that remote machine: only
// its remote workspaces qualify (a remote cwd can never be a local path).
// The path match is exact and case-insensitive (Windows drive letters).
func matchWorkspace(known []workspaces.Workspace, host, cwd string) (workspaces.Workspace, bool) {
	if cwd == "" {
		return workspaces.Workspace{}, false
	}
	for _, ws := range known {
		switch {
		case host == "":
			if ws.Remote != "" {
				continue
			}
		default:
			if strings.TrimPrefix(ws.Remote, "ssh-remote+") != host {
				continue
			}
		}
		if strings.EqualFold(ws.Path, cwd) {
			return ws, true
		}
	}
	return workspaces.Workspace{}, false
}

// firstHeaderValue returns the first comma-separated value of a
// multi-valued header ("" when the header is empty).
func firstHeaderValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
