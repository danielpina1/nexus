package hubserver

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticHandler serves a built single-page app from dir. Existing files are
// served as-is; unknown extension-less paths (client-side routes) and
// directories get index.html. Vite's hashed /assets/ are cached forever.
func staticHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		switch {
		case err == nil && !fi.IsDir():
			if strings.HasPrefix(p, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
		case err == nil || path.Ext(p) == "":
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
		default:
			http.NotFound(w, r)
		}
	})
}

// placeholderHandler is used when no dashboard is configured.
func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("nexusd is running. The web dashboard is not configured (server.web_dir).\n"))
	})
}
