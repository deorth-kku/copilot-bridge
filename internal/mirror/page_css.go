package mirror

import _ "embed"

// pageCSS is the mirror's own responsive/patch CSS, served at /page.css.
//
//go:embed static/page.css
var pageCSS string
