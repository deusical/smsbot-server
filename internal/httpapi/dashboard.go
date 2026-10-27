package httpapi

import (
	"io/fs"
	"net/http"
	"slices"
	"strings"
)

// dashboardPages are the first path segments the dashboard owns (see
// web/src/lib/routes.ts). Each page has its own URL, so a reload or bookmark of
// one has to get the app; any other unknown path stays a 404.
var dashboardPages = []string{"conversations", "commands", "activity", "archive", "settings"}

// dashboardHandler serves the embedded dashboard files, and its index page for
// the dashboard's own page URLs.
func dashboardHandler(files fs.FS) http.Handler {
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && slices.Contains(dashboardPages, first) {
			index := r.Clone(r.Context())
			index.URL.Path = "/"
			static.ServeHTTP(w, index)
			return
		}
		static.ServeHTTP(w, r)
	})
}
