package cdp

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"vscode-load-llama/internal/jscheck"
)

// The JS under test and the committed node:test suites that pin its
// behavior. Shared by TestInjectedJSUnit (sandbox run) and
// TestJSUnitStandalone (direct run against jscheck/testdata).
var jsUnitModules = map[string]string{
	"extract_fn":   extractFn,
	"html_expr":    htmlExpr,
	"fp_expr":      fpExpr,
	"css_expr":     cssExpr,
	"click_point":  clickPointExpr,
	"rect":         rectExpr,
	"image_fetch":  imageFetchExpr,
	"image_canvas": imageCanvasExpr,
}

var jsUnitRaw = map[string]string{
	"inject_js":        InjectJS,
	"mirror_inject_js": MirrorInjectJS,
}

var jsUnitTestFiles = []string{
	"cdp/extract_fn.test.js",
	"cdp/inject.test.js",
	"cdp/mirror_inject.test.js",
	"cdp/html_expr.test.js",
	"cdp/fp_expr.test.js",
	"cdp/css_expr.test.js",
	"cdp/click_rect.test.js",
	"cdp/image.test.js",
}

// TestInjectedJSUnit runs the committed node:test suites against the actual
// JS constants of this package: extraction probes (htmlExpr, fpExpr,
// cssExpr, clickPointExpr, rectExpr), image fetchers, and the two injected
// IIFEs (InjectJS, MirrorInjectJS). The DOM is a committed stub
// (jscheck/testdata/domstub.js); see the test files for the behavior each
// case pins down. The run happens in a temp sandbox, so it is always
// evaluated against the live constants.
func TestInjectedJSUnit(t *testing.T) {
	jscheck.RunJSUnit(t, jsUnitModules, jsUnitRaw, jsUnitTestFiles...)
}

// regenCmd is shown in failure messages pointing at the fixture
// regeneration command.
const regenCmd = "go test -run TestJSUnitStandalone ./internal/cdp -gen"

// TestJSFixturesCurrent verifies that the committed node fixtures
// (jscheck/testdata/modules and jscheck/testdata/raw) still match the live
// JS constants of this package. It runs on every `go test ./internal/...`
// so a JS constant edit that forgot to regenerate the fixtures fails the
// suite instead of silently testing stale JS.
func TestJSFixturesCurrent(t *testing.T) {
	testdata := jscheck.TestdataDir()
	if testdata == "" {
		t.Fatal("cannot locate jscheck testdata dir")
	}
	for name, src := range jsUnitModules {
		p := filepath.Join(testdata, "modules", name+".js")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("missing fixture %s: %v (regenerate: %s)", p, err, regenCmd)
			continue
		}
		if string(b) != "module.exports = "+src+";\n" {
			t.Errorf("stale fixture %s (regenerate: %s)", p, regenCmd)
		}
	}
	for name, src := range jsUnitRaw {
		p := filepath.Join(testdata, "raw", name+".js")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("missing fixture %s: %v (regenerate: %s)", p, err, regenCmd)
			continue
		}
		if string(b) != src {
			t.Errorf("stale fixture %s (regenerate: %s)", p, regenCmd)
		}
	}
}

// genFixtures, passed as `go test ... -gen`, rewrites the committed
// fixtures from the live constants before the standalone run.
var genFixtures = flag.Bool("gen", false, "regenerate the committed JS fixtures before running")

// TestJSUnitStandalone runs the committed node:test suites directly against
// jscheck/testdata with `node --test` — the exact command that also works
// by hand, with no Go prerequisite (the fixtures are committed):
//
//	node --test internal/jscheck/testdata/cdp/*.test.js
//
// (node 21+ expands the glob; on older node pass the file list explicitly.)
// After editing any JS constant, regenerate the fixtures first:
//
//	go test -run TestJSUnitStandalone ./internal/cdp -gen
func TestJSUnitStandalone(t *testing.T) {
	testdata := jscheck.TestdataDir()
	if testdata == "" {
		t.Fatal("cannot locate jscheck testdata dir")
	}
	if *genFixtures {
		if err := jscheck.WriteFixtures(testdata, jsUnitModules, jsUnitRaw); err != nil {
			t.Fatal(err)
		}
		t.Log("regenerated committed JS fixtures")
	}
	files := make([]string, len(jsUnitTestFiles))
	for i, tf := range jsUnitTestFiles {
		files[i] = filepath.Join(testdata, tf)
	}
	jscheck.RunNodeTest(t, files...)
}
