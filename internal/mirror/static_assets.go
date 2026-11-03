package mirror

import "net/http"

// The split static assets (page/workspaces CSS and JS) are served with the
// same gzip idiom as the HTML pages: one precompressed copy per process,
// identity encoding for clients without gzip support.

func (m *Mirror) handlePageCSS(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/css; charset=utf-8", pageCSS, mirrorCSSGz)
}

func (m *Mirror) handlePageJS(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/javascript; charset=utf-8", pageJS, mirrorJSGz)
}

func (m *Mirror) handleWorkspacesCSS(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/css; charset=utf-8", workspacesCSS, workspacesCSSGz)
}

func (m *Mirror) handleWorkspacesJS(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/javascript; charset=utf-8", workspacesJS, workspacesJSGz)
}
