package mirror

import (
	"fmt"
	"testing"

	"copilot-bridge/internal/jscheck"
)

// TestPageHTMLInlineScriptsCompiles syntax-checks the body of every inline
// <script> element of the served pages. The HTML is a Go raw string, so
// neither the HTML nor its embedded JS gets any compiler coverage elsewhere.
func TestPageHTMLInlineScriptsCompiles(t *testing.T) {
	checkPageScripts(t, "page", pageHTML)
	checkPageScripts(t, "workspaces", workspacesHTML)
}

func checkPageScripts(t *testing.T, name, htmlSrc string) {
	t.Helper()
	scripts := inlineScripts(htmlSrc)
	if len(scripts) == 0 {
		t.Fatalf("no inline <script> elements found in %s", name)
	}
	for i, s := range scripts {
		t.Run(name+fmt.Sprintf("_script%d", i), func(t *testing.T) {
			jscheck.Check(t, name+fmt.Sprintf("_script%d", i), s)
		})
	}
}
