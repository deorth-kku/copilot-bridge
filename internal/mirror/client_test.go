package mirror

import (
	"testing"

	"copilot-bridge/internal/cdp"
	"copilot-bridge/internal/workspaces"
)

func TestMergeStatePayloadInheritance(t *testing.T) {
	old := stateMsg{
		Type: "state", HTML: "<div>old</div>", CSS: "body{}",
		ThemeVars: "--fg:#fff", ThemeBg: "#1e1e1e",
		Scroll:  cdp.PaneScroll{Top: 10, ScrollH: 100},
		Windows: []cdp.Window{{ID: "a"}},
	}
	new := stateMsg{Type: "state", Scroll: cdp.PaneScroll{Top: 20, ScrollH: 120}}
	m := mergeState(old, new)
	if m.HTML != old.HTML || m.CSS != old.CSS || m.ThemeVars != old.ThemeVars || m.ThemeBg != old.ThemeBg {
		t.Fatalf("full payloads not inherited from the older frame: %+v", m)
	}
	if m.Scroll.Top != 20 || m.Scroll.ScrollH != 120 {
		t.Fatalf("newer scroll must win: %+v", m.Scroll)
	}
	if len(m.Windows) != 1 || m.Windows[0].ID != "a" {
		t.Fatalf("windows lost: %+v", m.Windows)
	}
}

func TestMergeStateNewerPayloadWins(t *testing.T) {
	old := stateMsg{Type: "state", HTML: "<div>old</div>", CSS: "old{}"}
	new := stateMsg{Type: "state", HTML: "<div>new</div>", CSS: "new{}"}
	m := mergeState(old, new)
	if m.HTML != new.HTML || m.CSS != new.CSS {
		t.Fatalf("newer payloads must win: %+v", m)
	}
}

func TestMergeStatePopup(t *testing.T) {
	// A nil popup in the newer frame means "closed": never resurrected.
	old := stateMsg{Type: "state", Popup: &cdp.PopupState{HTML: "menu", Left: 1, Top: 2}}
	new := stateMsg{Type: "state"}
	if m := mergeState(old, new); m.Popup != nil {
		t.Fatalf("closed popup must not be resurrected: %+v", m.Popup)
	}
	// A position-only popup inherits the latest payload from the older frame.
	new = stateMsg{Type: "state", Popup: &cdp.PopupState{Left: 9, Top: 9}}
	m := mergeState(old, new)
	if m.Popup == nil || m.Popup.HTML != "menu" || m.Popup.Left != 9 || m.Popup.Top != 9 {
		t.Fatalf("position-only popup must inherit the older HTML: %+v", m.Popup)
	}
	// A newer popup HTML wins.
	new = stateMsg{Type: "state", Popup: &cdp.PopupState{HTML: "menu2", Left: 3}}
	if m := mergeState(old, new); m.Popup.HTML != "menu2" {
		t.Fatalf("newer popup HTML must win: %+v", m.Popup)
	}
}

func TestMergeStateErr(t *testing.T) {
	old := stateMsg{Type: "state", Err: "pane not found"}
	if m := mergeState(old, stateMsg{Type: "state"}); m.Err != "" {
		t.Fatalf("a good newer frame must clear the older err: %q", m.Err)
	}
	if m := mergeState(stateMsg{Type: "state"}, stateMsg{Type: "state", Err: "gone"}); m.Err != "gone" {
		t.Fatalf("newer err must win: %q", m.Err)
	}
}

func TestCoalesceStateBacklog(t *testing.T) {
	c := fakeClient(false)
	full := stateMsg{Type: "state", HTML: "<div>full</div>", CSS: "body{}", Scroll: cdp.PaneScroll{Top: 1}}
	c.sendTo(full)
	c.sendTo(stateMsg{Type: "state", Scroll: cdp.PaneScroll{Top: 2}})
	c.sendTo(stateMsg{Type: "state", Scroll: cdp.PaneScroll{Top: 3}})

	frames, drained := c.coalesce(stateMsg{Type: "state", Scroll: cdp.PaneScroll{Top: 0}})
	if drained != 4 || len(frames) != 1 {
		t.Fatalf("expected 4 drained frames folded into 1, got drained=%d frames=%d", drained, len(frames))
	}
	m, ok := frames[0].(stateMsg)
	if !ok {
		t.Fatalf("merged frame is not a stateMsg: %T", frames[0])
	}
	// The LATEST scroll wins; the full payloads survive from the backlog.
	if m.Scroll.Top != 3 || m.HTML != "<div>full</div>" || m.CSS != "body{}" {
		t.Fatalf("merged frame wrong: %+v", m)
	}
	if len(c.send) != 0 {
		t.Fatalf("coalesce must drain the queue, %d left", len(c.send))
	}
}

func TestCoalesceLatestPerType(t *testing.T) {
	c := fakeClient(true)
	c.sendTo(workspacesMsg{Type: "workspaces"})
	c.sendTo(shutdownMsg{Type: "shutdown", Armed: false})
	c.sendTo(workspacesMsg{Type: "workspaces", Workspaces: []workspaces.Workspace{{Name: "ws-a"}}})
	c.sendTo(shutdownMsg{Type: "shutdown", Armed: true})

	frames, drained := c.coalesce(workspacesMsg{Type: "workspaces"})
	if drained != 5 {
		t.Fatalf("expected 5 drained, got %d", drained)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames (latest per type), got %d", len(frames))
	}
	ws, ok := frames[0].(workspacesMsg)
	if !ok || len(ws.Workspaces) != 1 || ws.Workspaces[0].Name != "ws-a" {
		t.Fatalf("frame 0 must be the latest workspacesMsg: %+v", frames[0])
	}
	sd, ok := frames[1].(shutdownMsg)
	if !ok || !sd.Armed {
		t.Fatalf("frame 1 must be the latest shutdownMsg (armed): %+v", frames[1])
	}
}

func TestCoalesceSingleFrame(t *testing.T) {
	c := fakeClient(false)
	m := stateMsg{Type: "state", HTML: "x"}
	frames, drained := c.coalesce(m)
	if drained != 1 || len(frames) != 1 || frames[0].(stateMsg).HTML != "x" {
		t.Fatalf("single frame must pass through: drained=%d frames=%d", drained, len(frames))
	}
}
