package mirror

import _ "embed"

// pageHTML is the single-page mirror client. It renders the extracted pane
// HTML (styled by the extracted workbench CSS) inside #pane, forwards all
// mouse/keyboard events to the server as pane-relative coordinates, and
// patches the pane DOM in place whenever a new state arrives — only the
// changed nodes are touched, so the browser re-lays-out/repaints just the
// updated parts instead of the whole page. The extracted HTML carries no
// scripts, so the mirror is purely a visual replica plus an input-capture
// surface; all real behavior happens in the live VS Code page. Its CSS and
// JS live in static/page.css and static/page.js, served at /page.css and
// /page.js.
//
//go:embed static/page.html
var pageHTML string
