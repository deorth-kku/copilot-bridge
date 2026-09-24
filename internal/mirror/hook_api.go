package mirror

import (
	"encoding/json/v2"
	"io"
	"net/http"
)

// handleHook receives one VS Code agent hook event, forwarded verbatim by
// the "hook" subcommand (its stdin payload becomes this request body),
// and feeds the event name to the shutdown planner. The planner only acts
// on "Stop" and "SessionStart"; everything else is accepted and ignored.
// The response also carries the mirror URL of the workspace the event
// belongs to (resolved from the request's source machine and the payload's
// cwd), so the caller can jump straight to it.
func (m *Mirror) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if m.planner == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.MarshalWrite(w, map[string]string{"err": "shutdown planner not configured"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	var ev struct {
		Name string `json:"hook_event_name"`
		Cwd  string `json:"cwd"`
	}
	if jerr := json.Unmarshal(body, &ev); jerr != nil {
		// Not fatal: the hook subcommand always exits 0 anyway, but keep
		// the log useful.
		m.log.Warn("hook: bad payload", "err", jerr)
	}
	m.log.Info("hook event", "name", ev.Name)
	m.planner.HookEvent(ev.Name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]any{"ok": true, "mirror": m.hookMirrorURL(r, ev.Cwd)})
}

// armReq is the browser -> server request to arm or disarm the power-off.
type armReq struct {
	Armed bool `json:"armed"`
}

// handleShutdownArm arms or disarms the "power off at the next task end"
// toggle and pushes the new state to the workspaces-page clients.
func (m *Mirror) handleShutdownArm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if m.planner == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.MarshalWrite(w, map[string]string{"err": "shutdown planner not configured"})
		return
	}
	var req armReq
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.MarshalWrite(w, map[string]string{"err": err.Error()})
		return
	}
	if req.Armed {
		m.planner.Arm()
	} else {
		m.planner.Disarm()
	}
	armed := m.planner.Armed()
	m.log.Info("power-off toggle", "armed", armed)
	m.pushShutdown(armed)
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]bool{"ok": true, "armed": armed})
}

// pushShutdown fans the armed state out to every connected
// workspaces-page client (the mirror page does not show the toggle).
// Non-blocking per client (sendTo drops for slow clients).
func (m *Mirror) pushShutdown(armed bool) {
	m.clients.All()(func(c *client, _ struct{}) bool {
		if c.wsPage {
			c.sendTo(shutdownMsg{Type: "shutdown", Armed: armed})
		}
		return true
	})
}
