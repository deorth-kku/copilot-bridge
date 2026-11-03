package mirror

import (
	"path/filepath"
	"testing"

	"copilot-bridge/internal/jscheck"
)

// TestMirrorJSUnit runs the committed node:test suite against the mirror
// page script (static/page.js): the mirror client IIFE (WebSocket state
// sync, incremental DOM patching, popup placement, input capture). The test
// file reads the committed source file directly, so the run is always
// evaluated against the live file — no fixture copy to keep in sync. The
// DOM is the committed stub (jscheck/testdata/domstub.js); see the test
// file for the behavior each case pins down. The same command works by
// hand, with no Go prerequisite:
//
//	node --test internal/jscheck/testdata/mirror/mirror_js.test.js
func TestMirrorJSUnit(t *testing.T) {
	testdata := jscheck.TestdataDir()
	if testdata == "" {
		t.Fatal("cannot locate jscheck testdata dir")
	}
	jscheck.RunNodeTest(t, filepath.Join(testdata, "mirror", "mirror_js.test.js"))
}
