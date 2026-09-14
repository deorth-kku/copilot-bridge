package mirror

import (
	"fmt"
	"testing"

	"vscode-load-llama/internal/jscheck"
)

// TestPageHTMLInlineScriptsCompiles syntax-checks the body of every inline
// <script> element of the served mirror page. The HTML is a Go raw string,
// so neither the HTML nor its embedded JS gets any compiler coverage
// elsewhere.
func TestPageHTMLInlineScriptsCompiles(t *testing.T) {
	scripts := pageScripts()
	if len(scripts) == 0 {
		t.Fatal("no inline <script> elements found in pageHTML")
	}
	for i, s := range scripts {
		t.Run(fmt.Sprintf("script%d", i), func(t *testing.T) {
			jscheck.Check(t, fmt.Sprintf("page_script%d", i), s)
		})
	}
}
