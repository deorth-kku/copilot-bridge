package mirror

import (
	"encoding/json/v2"
	"io"
	"net/http"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/workspaces"
)

// handleWorkspacesPage serves the workspaces page. The mux pattern
// "/workspaces" matches only that exact path, so no path check is needed.
func (m *Mirror) handleWorkspacesPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(workspacesHTML))
}

// handleWorkspaceList serves the current workspace list: the known
// workspaces with the live open state overlaid from CDP (falls back to a
// fresh read of storage.json when the store is nil).
func (m *Mirror) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	ws, err := m.currentWorkspaces()
	if err != nil {
		m.log.Warn("workspaces: list failed", "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, ws)
}

// openReq is the browser -> server request to launch one workspace.
type openReq struct {
	URI string `json:"uri"`
}

// handleWorkspaceOpen launches a VS Code window for one workspace via the
// code CLI (fire-and-forget).
func (m *Mirror) handleWorkspaceOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req openReq
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.MarshalWrite(w, map[string]string{"err": "bad request: " + err.Error()})
		return
	}
	if err := m.opener.Open(req.URI); err != nil {
		m.log.Warn("workspaces: open failed", "uri", req.URI, "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]bool{"ok": true})
}

// liveWorkspaces probes every live window for the workspace it has open
// (the same workbench probe the jump-to-window logic uses: the main
// process hands every renderer its window configuration, so the answer
// is exact, not a title guess) and returns window id -> workspace URI
// (storage.json key format). Empty windows (no folder open) and failed
// probes are omitted.
func (m *Mirror) liveWorkspaces() map[string]string {
	out := make(map[string]string)
	for _, win := range m.disc.Windows() {
		s := m.disc.SessionForID(win.ID)
		if s == nil {
			continue
		}
		if uri := cdp.WorkspaceURI(s); uri != "" {
			out[win.ID] = uri
		}
	}
	return out
}

// resolveWindowURI finds the live window that has the given workspace
// open. ok is false when no live window matches.
func (m *Mirror) resolveWindowURI(uri string) (string, bool) {
	for id, wuri := range m.liveWorkspaces() {
		if workspaces.SameURI(uri, wuri) {
			return id, true
		}
	}
	return "", false
}

// currentWorkspaces returns the workspace list to show: the known
// workspaces (hot-reloaded storage.json) with the live open state
// overlaid from CDP, so the open flags follow the actual windows instead
// of storage.json's lagging windowsState.
func (m *Mirror) currentWorkspaces() ([]workspaces.Workspace, error) {
	var known []workspaces.Workspace
	var err error
	if m.wsStore != nil {
		known = m.wsStore.Load()
	} else {
		known, err = workspaces.List(m.storagePath)
		if err != nil {
			return nil, err
		}
	}
	return workspaces.Overlay(known, m.liveWorkspaces()), nil
}

// sendWorkspaces queues the current CDP-overlaid workspace list for one
// workspaces-page client (sent on connect). The list is computed on demand
// so a just-appeared tab sees the live windows, not a stale snapshot.
func (m *Mirror) sendWorkspaces(c *client) {
	list, err := m.currentWorkspaces()
	if err != nil || list == nil {
		return
	}
	c.sendTo(workspacesMsg{Type: "workspaces", Workspaces: list})
}

// pushWorkspaces fans the workspace list out to every connected
// workspaces-page client. Non-blocking per client (sendTo drops for slow
// clients), so it is safe to call from any goroutine.
func (m *Mirror) pushWorkspaces(list []workspaces.Workspace) {
	m.clients.All()(func(c *client, _ struct{}) bool {
		if c.wsPage {
			c.sendTo(workspacesMsg{Type: "workspaces", Workspaces: list})
		}
		return true
	})
}

// onWorkspacesChanged is the Store onChange callback: the known workspace
// list changed, so the CDP-overlaid list must be recomputed. The publish
// loop owns the CDP probes, so the watcher only signals it.
func (m *Mirror) onWorkspacesChanged(_ []workspaces.Workspace) {
	select {
	case m.wsRefresh <- struct{}{}:
	default:
	}
}

// refreshWorkspaces recomputes the CDP-overlaid workspace list and pushes
// it to the workspaces-page clients when it changed. Only the publish-loop
// goroutine calls it; a failed computation keeps the previous list.
func (m *Mirror) refreshWorkspaces() {
	list, err := m.currentWorkspaces()
	if err != nil {
		m.log.Warn("workspaces: live list failed, keeping previous", "err", err)
		return
	}
	if equalWorkspaces(m.lastWsPush, list) {
		return
	}
	m.lastWsPush = list
	m.pushWorkspaces(list)
}

// equalWorkspaces reports whether two workspace lists are identical (same
// entries in the same order; Name/Path/Remote derive from the URI, so URI
// + Open + LastActive fully describe an entry).
func equalWorkspaces(a, b []workspaces.Workspace) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].URI != b[i].URI || a[i].Open != b[i].Open || a[i].LastActive != b[i].LastActive {
			return false
		}
	}
	return true
}

// wsPageClients counts the connected workspaces-page tabs.
func (m *Mirror) wsPageClients() int {
	n := 0
	m.clients.All()(func(c *client, _ struct{}) bool {
		if c.wsPage {
			n++
		}
		return true
	})
	return n
}
