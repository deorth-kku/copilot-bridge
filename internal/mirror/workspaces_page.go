package mirror

import _ "embed"

// workspacesHTML is the workspaces page served at /workspaces. It lists
// every workspace VS Code knows about (from globalStorage storage.json)
// with a live search box; clicking a row asks the server to launch a VS
// Code window for it via the code CLI. The green dots (open workspaces)
// follow the LIVE windows — the server probes each window's workbench
// over CDP — not storage.json's lagging windowsState. Same idiom as the
// mirror page: no framework. The look is a HAND-WRITTEN replica of the
// VS Code (Dark Modern) UI: the page must render with no live VS Code
// window, so no workbench CSS is borrowed — the font stack and color
// values are pinned to the live theme in the CSS. Its CSS and JS live in
// static/workspaces.css and static/workspaces.js, served at
// /workspaces.css and /workspaces.js.
//
//go:embed static/workspaces.html
var workspacesHTML string

// workspacesCSS is the workspaces page's stylesheet, served at
// /workspaces.css.
//
//go:embed static/workspaces.css
var workspacesCSS string

// workspacesJS is the workspaces page's client script, served at
// /workspaces.js.
//
//go:embed static/workspaces.js
var workspacesJS string
