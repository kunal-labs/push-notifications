package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/index.html
var staticFS embed.FS

// Handler returns an HTTP handler serving the embedded Google Cloud Control Room UI.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// IndexHTML returns the raw embedded index.html bytes for inspection and testing.
func IndexHTML() ([]byte, error) {
	return staticFS.ReadFile("static/index.html")
}
