// Package jscheck gives the project's injected JavaScript test coverage.
// Check syntax-checks one source with node --check; RunNodeTest executes
// the committed node:test files (which load the committed JS sources
// directly) with `node --test`. Both skip cleanly on machines without node
// on PATH.
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
