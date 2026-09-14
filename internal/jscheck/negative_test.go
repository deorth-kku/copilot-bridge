package jscheck

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBrokenJSIsRejected is a harness self-test (the "negative" path): it
// verifies the node --check plumbing actually rejects broken JS, so a
// silently no-op harness cannot mask a regression in the JS under test.
func TestBrokenJSIsRejected(t *testing.T) {
	if !Available() {
		t.Skip("node not on PATH")
	}
	p := filepath.Join(t.TempDir(), "broken.js")
	if err := os.WriteFile(p, []byte("function ( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := checkFile(p)
	if err == nil {
		t.Fatalf("node --check accepted broken JS: %s", out)
	}
}
