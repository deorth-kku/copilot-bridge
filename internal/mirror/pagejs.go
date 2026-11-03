package mirror

import _ "embed"

// pageJS is the mirror's client-side script, served at /page.js.
//
//go:embed static/page.js
var pageJS string
