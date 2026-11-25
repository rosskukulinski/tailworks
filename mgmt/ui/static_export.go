package ui

import (
	"io/fs"
	"net/http"
)

// StaticHandler serves the admin UI's static files (app.css, app.js) at
// the request's path, for another local page set that wants the same look:
// `tw dev`'s console (cmd/tw/devconsole.go) mounts it under /static/.
func StaticHandler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	return http.FileServerFS(static)
}
