package mirror

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"copilot-bridge/internal/jscheck"
)

// pageScripts returns the bodies of all inline <script> elements in
// pageHTML. The HTML is a Go raw string, so the embedded JS gets no
// compiler coverage elsewhere.
func pageScripts() []string { return inlineScripts(pageHTML) }

// inlineScripts returns the bodies of all inline <script> elements in an
// HTML source string.
func inlineScripts(htmlSrc string) []string {
	doc, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return nil
	}
	var scripts []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && n.FirstChild != nil {
			scripts = append(scripts, n.FirstChild.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return scripts
}

// mirrorRegenCmd is shown in failure messages pointing at the fixture
// regeneration command.
const mirrorRegenCmd = "go test -run TestMirrorJSFixturesCurrent ./internal/mirror -gen"

// mirrorGenFixtures, passed as `go test ... -gen`, rewrites the committed
// mirror page JS fixture from the live pageHTML constant.
var mirrorGenFixtures = flag.Bool("gen", false, "regenerate the committed mirror JS fixture before running")

// TestMirrorJSFixturesCurrent verifies that the committed mirror page JS
// fixture (jscheck/testdata/raw/mirror_page_js.js) still matches the inline
// <script> of pageHTML. It runs on every `go test ./internal/...` so an
// edit to the mirror page that forgot to regenerate the fixture fails the
// suite instead of silently testing stale JS.
func TestMirrorJSFixturesCurrent(t *testing.T) {
	scripts := pageScripts()
	if len(scripts) != 1 {
		t.Fatalf("expected exactly 1 inline <script> in pageHTML, got %d", len(scripts))
	}
	p := filepath.Join(jscheck.TestdataDir(), "raw", "mirror_page_js.js")
	if *mirrorGenFixtures {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(scripts[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("regenerated mirror page JS fixture")
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing fixture %s: %v (regenerate: %s)", p, err, mirrorRegenCmd)
	}
	if string(b) != scripts[0] {
		t.Errorf("stale fixture %s (regenerate: %s)", p, mirrorRegenCmd)
	}
}

// TestMirrorJSUnit runs the committed node:test suite against the actual
// inline <script> of pageHTML: the mirror client IIFE (WebSocket state
// sync, incremental DOM patching, popup placement, input capture). The DOM
// is the committed stub (jscheck/testdata/domstub.js); see the test file
// for the behavior each case pins down. The run happens in a temp sandbox,
// so it is always evaluated against the live constant.
func TestMirrorJSUnit(t *testing.T) {
	scripts := pageScripts()
	if len(scripts) != 1 {
		t.Fatalf("expected exactly 1 inline <script> in pageHTML, got %d", len(scripts))
	}
	jscheck.RunJSUnit(t, nil, map[string]string{"mirror_page_js": scripts[0]}, "mirror/mirror_js.test.js")
}

// TestMirrorJSUnitStandalone runs the committed node:test suite directly
// against jscheck/testdata with `node --test` — the exact command that also
// works by hand, with no Go prerequisite (the fixture is committed):
//
//	node --test internal/jscheck/testdata/mirror/mirror_js.test.js
//
// After editing the mirror page's inline JS, regenerate the fixture first:
//
//	go test -run TestMirrorJSFixturesCurrent ./internal/mirror -gen
func TestMirrorJSUnitStandalone(t *testing.T) {
	testdata := jscheck.TestdataDir()
	if testdata == "" {
		t.Fatal("cannot locate jscheck testdata dir")
	}
	jscheck.RunNodeTest(t, filepath.Join(testdata, "mirror", "mirror_js.test.js"))
}
