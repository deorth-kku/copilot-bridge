package cdp

import (
	"testing"

	"vscode-load-llama/internal/jscheck"
)

// TestInjectedJSCompiles syntax-checks every JavaScript constant that is
// evaluated in a live page (via Runtime.evaluate) or spliced into one.
// These strings have no other syntax coverage: a typo only surfaces as a
// silent injection failure at runtime.
func TestInjectedJSCompiles(t *testing.T) {
	cases := []struct {
		name string
		js   string
	}{
		// inject.go
		{"extractFn", extractFn},
		{"InjectJS", InjectJS},
		{"MirrorInjectJS", MirrorInjectJS},
		// extract.go
		{"htmlExpr", htmlExpr},
		{"scrollContainerJS", scrollContainerJS},
		{"scrollPathJS", scrollPathJS},
		{"fpExpr", fpExpr},
		{"cssExpr", cssExpr},
		{"clickPointExpr", clickPointExpr},
		{"rectExpr", rectExpr},
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
