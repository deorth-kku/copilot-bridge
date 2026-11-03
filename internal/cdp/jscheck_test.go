package cdp

import (
	"testing"

	"copilot-bridge/internal/jscheck"
)

// TestInjectedJSCompiles syntax-checks every JavaScript source that is
// evaluated in a live page (via Runtime.evaluate) or spliced into one.
// These strings have no other syntax coverage: a typo only surfaces as a
// silent injection failure at runtime.
func TestInjectedJSCompiles(t *testing.T) {
	cases := []struct {
		name string
		js   string
	}{
		// inject.go
		{"ProbeLibJS", ProbeLibJS},
		{"InjectJS", InjectJS(defaultDebounceMs)},
		{"MirrorInjectJS", MirrorInjectJS(defaultDebounceMs)},
		// extract.go
		{"htmlExpr", htmlExpr},
		{"fpExpr", fpExpr},
		{"cssExpr", cssExpr},
		{"clickPointExpr", clickPointExpr},
		{"charPointExpr", charPointExpr},
		{"rectExpr", rectExpr},
		{"healthExpr", healthExpr},
		{"workspaceExpr", workspaceExpr},
		// toggle.go
		{"toggleChatExpr", toggleChatExpr},
		// image.go
		{"imageFetchExpr", imageFetchExpr},
		{"imageCanvasExpr", imageCanvasExpr},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			jscheck.Check(t, c.name, c.js)
		})
	}
}
