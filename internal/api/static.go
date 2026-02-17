package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

func StaticFileHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("static handler", "path", r.URL.Path, "method", r.Method)
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Serve _next/ static assets directly (CSS, JS, media)
		if strings.HasPrefix(path, "_next/") {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Try path + ".html" first (Next.js static export: /settings -> settings.html)
		if !strings.HasSuffix(path, ".html") {
			if _, err := fs.Stat(webFS, path+".html"); err == nil {
				r.URL.Path = "/" + path + ".html"
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Try exact file (images, favicon, etc.)
		if info, err := fs.Stat(webFS, path); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Try path + "/index.html" (directory index)
		if _, err := fs.Stat(webFS, path+"/index.html"); err == nil {
			r.URL.Path = "/" + path + "/index.html"
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for unmatched routes
		r.URL.Path = "/index.html"
		fileServer.ServeHTTP(w, r)
	})
}

func StaticFileHandlerFromDir(dir string) http.Handler {
	return StaticFileHandler(os.DirFS(dir))
}
