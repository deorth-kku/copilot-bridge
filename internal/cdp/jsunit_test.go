package cdp

import (
	"path/filepath"
	"testing"

	"copilot-bridge/internal/jscheck"
)

// TestJSUnit runs the committed node:test suites against the committed JS
// sources in js/: the test files assemble the templated expressions the
// same way the Go package does (the js/lib fragments spliced into the
// __PLACEHOLDER__ markers), so the run is always evaluated against the
// live files — no fixture copy to keep in sync. The DOM is a committed
// stub (jscheck/testdata/domstub.js); see the test files for the behavior
// each case pins down. The same command works by hand, with no Go
// prerequisite:
//
//	node --test internal/jscheck/testdata/cdp/*.test.js
//
// (node 21+ expands the glob; on older node pass the file list explicitly.)
func TestJSUnit(t *testing.T) {
	testdata := jscheck.TestdataDir()
	if testdata == "" {
		t.Fatal("cannot locate jscheck testdata dir")
	}
	files := []string{
		"cdp/extract_fn.test.js",
		"cdp/inject.test.js",
		"cdp/mirror_inject.test.js",
		"cdp/html_expr.test.js",
		"cdp/fp_expr.test.js",
		"cdp/css_expr.test.js",
		"cdp/click_rect.test.js",
		"cdp/toggle.test.js",
		"cdp/image.test.js",
	}
	abs := make([]string, len(files))
	for i, f := range files {
		abs[i] = filepath.Join(testdata, f)
	}
	jscheck.RunNodeTest(t, abs...)
}
