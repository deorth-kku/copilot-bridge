package mirror

import (
	"testing"

	"copilot-bridge/internal/jscheck"
)

// TestPageScriptsCompile syntax-checks the client scripts of the served
// pages. The JS lives in committed files (static/page.js,
// static/workspaces.js, embedded via //go:embed), so neither the HTML nor
// the JS gets any compiler coverage elsewhere.
func TestPageScriptsCompile(t *testing.T) {
	t.Run("page", func(t *testing.T) {
		jscheck.Check(t, "page", pageJS)
	})
	t.Run("workspaces", func(t *testing.T) {
		jscheck.Check(t, "workspaces", workspacesJS)
	})
}
