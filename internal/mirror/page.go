package mirror

// pageHTML is the single-page mirror client. It renders the extracted pane
// HTML (styled by the extracted workbench CSS) inside #pane, forwards all
// mouse/keyboard events to the server as pane-relative coordinates, and
// patches the pane DOM in place whenever a new state arrives — only the
// changed nodes are touched, so the browser re-lays-out/repaints just the
// updated parts instead of the whole page. The extracted HTML carries no
// scripts, so the mirror is purely a visual replica plus an input-capture
// surface; all real behavior happens in the live VS Code page.
const pageHTML = pageHead + pageCSS + pageMid + pageJS + pageTail

const pageHead = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VS Code Copilot Mirror</title>
<style id="mirror-css"></style>
<style id="mirror-theme"></style>
<style>
`

const pageMid = `</style>
</head>
<body>
<select id="status"><option>connecting…</option></select>
<!-- #pane carries ancestor classes the extracted subtree is missing, so that
     workbench CSS rules scoped to those ancestors still match:
     - monaco-workbench: the ~1900 rules scoped to ".monaco-workbench
       <descendant>" (input box box-sizing, border-radius, background, the
       --vscode-* palette vars, pill text colors, etc.).
     - monaco-pane-view: pane-section rules, e.g.
       ".monaco-pane-view .pane > .pane-header.hidden { display: none }"
       (hides the "Chat" section header in merged-header mode) and the
       ".monaco-pane-view .pane > .pane-header" flex layout.
     It is the direct parent of the extracted pane root (the full chat pane
     container: title bar + session list + conversation + input), so
     pane.firstElementChild is the pane root and the DOM-path click mapping is
     unaffected. -->
<div id="pane" class="monaco-workbench monaco-pane-view" tabindex="0"></div>
<script>
`

const pageTail = `</script>
</body>
</html>
`

