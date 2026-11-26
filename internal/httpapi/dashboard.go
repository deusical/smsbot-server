package httpapi

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// dashboardPages are the first path segments the dashboard owns (see
// web/src/lib/routes.ts). Each page has its own URL, so a reload or bookmark of
// one has to get the app; any other unknown path stays a 404.
var dashboardPages = []string{"conversations", "commands", "activity", "archive", "settings"}

type precompressedAsset struct {
	content []byte
	mime    string
	modTime time.Time
}

func precompressAssets(files fs.FS) map[string]precompressedAsset {
	out := make(map[string]precompressedAsset)
	_ = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".js", ".css", ".html", ".webmanifest", ".svg", ".json":
		default:
			return nil
		}
		data, err := fs.ReadFile(files, path)
		if err != nil || len(data) == 0 {
			return nil
		}
		info, _ := d.Info()
		var modTime time.Time
		if info != nil {
			modTime = info.ModTime()
		}
		var buf bytes.Buffer
		gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return nil
		}
		_, _ = gz.Write(data)
		_ = gz.Close()

		mType := mime.TypeByExtension(ext)
		if mType == "" {
			switch ext {
			case ".js":
				mType = "text/javascript; charset=utf-8"
			case ".css":
				mType = "text/css; charset=utf-8"
			case ".html":
				mType = "text/html; charset=utf-8"
			case ".webmanifest":
				mType = "application/manifest+json"
			case ".svg":
				mType = "image/svg+xml"
			case ".json":
				mType = "application/json"
			}
		}
		clean := strings.TrimPrefix(filepath.ToSlash(path), "/")
		out[clean] = precompressedAsset{
			content: buf.Bytes(),
			mime:    mType,
			modTime: modTime,
		}
		if clean == "index.html" {
			out[""] = out[clean]
		}
		return nil
	})
	return out
}

// dashboardHandler serves the embedded dashboard files, and its index page for
// the dashboard's own page URLs.
func dashboardHandler(files fs.FS) http.Handler {
	static := http.FileServer(http.FS(files))
	pre := precompressAssets(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		isDashboardPage := (r.Method == http.MethodGet || r.Method == http.MethodHead) && slices.Contains(dashboardPages, first)
		targetPath := strings.TrimPrefix(r.URL.Path, "/")
		if isDashboardPage {
			targetPath = ""
		}

		if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.Header.Get("Range") == "" &&
			strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			if pf, ok := pre[targetPath]; ok {
				w.Header().Set("Content-Type", pf.mime)
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Add("Vary", "Accept-Encoding")
				w.Header().Set("Content-Length", strconv.Itoa(len(pf.content)))
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(pf.content)
				return
			}
		}

		if isDashboardPage {
			index := r.Clone(r.Context())
			index.URL.Path = "/"
			static.ServeHTTP(w, index)
			return
		}
		static.ServeHTTP(w, r)
	})
}
