// Package jscheck gives the project's injected JavaScript test coverage.
// Check syntax-checks one source with node --check; RunJSUnit builds a
// temp sandbox (the committed domstub harness + generated module files +
// committed node:test files) and executes it with `node --test`. Both skip
// cleanly on machines without node on PATH.
package jscheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Available reports whether a node executable is on PATH.
func Available() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

// checkFile runs node --check on p, returning its combined output.
func checkFile(p string) ([]byte, error) {
	return exec.Command("node", "--check", p).CombinedOutput()
}

// Check runs node --check on src, failing t with the diagnostic output on
// a syntax error. It skips the test when node is unavailable.
func Check(t *testing.T, name, src string) {
	t.Helper()
	if !Available() {
		t.Skip("node not on PATH; skipping JS syntax check")
	}
	p := filepath.Join(t.TempDir(), name+".js")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := checkFile(p)
	if err != nil {
		t.Errorf("node --check %s: %v\n%s", name, err, out)
	}
}

// TestdataDir returns the absolute path of this package's testdata
// directory (domstub.js and the committed node:test suites live there).
func TestdataDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

// writeModuleFiles writes the generated JS fixtures under dir:
// modules[name] becomes modules/name.js wrapped as `module.exports = <src>;`
// and raw[name] becomes raw/name.js verbatim.
func writeModuleFiles(dir string, modules, raw map[string]string) error {
	mdir := filepath.Join(dir, "modules")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		return err
	}
	for name, src := range modules {
		if err := os.WriteFile(filepath.Join(mdir, name+".js"), []byte("module.exports = "+src+";\n"), 0o644); err != nil {
			return err
		}
	}
	rdir := filepath.Join(dir, "raw")
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		return err
	}
	for name, src := range raw {
		if err := os.WriteFile(filepath.Join(rdir, name+".js"), []byte(src), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// WriteFixtures writes the generated JS fixtures under dir so the committed
// node:test files can be run directly against the testdata tree (no
// sandbox). The directories are gitignored; regenerate after changing any
// JS constant (TestJSUnitStandalone does this automatically).
func WriteFixtures(dir string, modules, raw map[string]string) error {
	return writeModuleFiles(dir, modules, raw)
}

// RunNodeTest runs `node --test` on the given absolute test file paths and
// fails t with node's combined output on any failure.
func RunNodeTest(t *testing.T, files ...string) {
	t.Helper()
	if !Available() {
		t.Skip("node not on PATH; skipping JS unit tests")
	}
	args := append([]string{"--test"}, files...)
	out, err := exec.Command("node", args...).CombinedOutput()
	if err != nil {
		t.Errorf("node --test: %v\n%s", err, out)
	}
}

// RunJSUnit builds a temp sandbox and runs the committed node:test files
// against generated copies of the JS under test:
//   - testdata/domstub.js is copied to the sandbox root (the DOM stub all
//     test files share);
//   - each modules[name] is written to modules/name.js wrapped as
//     `module.exports = <src>;` (for arrow-function constants);
//   - each raw[name] is written to raw/name.js verbatim (for IIFEs that
//     the test evals with new Function);
//   - each testFile (relative to this package's testdata dir) is copied to
//     the sandbox root and executed by `node --test`.
//
// It fails t with node's output on any failure and skips when node is
// unavailable.
func RunJSUnit(t *testing.T, modules, raw map[string]string, testFiles ...string) {
	t.Helper()
	if !Available() {
		t.Skip("node not on PATH; skipping JS unit tests")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate jscheck package dir")
	}
	testdata := filepath.Join(filepath.Dir(thisFile), "testdata")

	dir := t.TempDir()
	copyFile(t, filepath.Join(testdata, "domstub.js"), filepath.Join(dir, "domstub.js"))
	// Test files keep their testdata-relative subpath (e.g. cdp/foo.test.js
	// lands in <sandbox>/cdp/foo.test.js) so their require('../domstub.js')
	// and require('../modules/...') paths resolve.
	var copied []string
	for _, tf := range testFiles {
		dst := filepath.Join(dir, tf)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		copyFile(t, filepath.Join(testdata, tf), dst)
		copied = append(copied, dst)
	}
	if err := writeModuleFiles(dir, modules, raw); err != nil {
		t.Fatal(err)
	}
	// Explicit file list (a bare directory arg is not reliably treated as
	// a test root across node versions).
	args := append([]string{"--test"}, copied...)
	out, err := exec.Command("node", args...).CombinedOutput()
	if err != nil {
		t.Errorf("node --test: %v\n%s", err, out)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
