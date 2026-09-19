// Package web embeds the built board (web/dist) and serves it as a single-page app.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// all: keeps dist/.gitkeep so the package compiles before the first `pnpm build`.
//
//go:embed all:dist
var distFS embed.FS

// Dist is the built board rooted at web/dist.
var Dist fs.FS = mustSub(distFS, "dist")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler serves files from fsys and falls back to index.html for client-side routes.
// Mount it last: mux.Handle("GET /", web.Handler(web.Dist)).
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if strings.HasPrefix(name, "api/") || name == "api" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such route"}}`))
			return
		}
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "board not built: run make web-build", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(index)
	})
}
