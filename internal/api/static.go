package api

import (
	"io/fs"
	"net/http"
	"os"
	"strings"
)

func StaticFileHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Try to serve the exact file
		if _, err := fs.Stat(webFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Try path + ".html" (Next.js static export convention)
		if _, err := fs.Stat(webFS, path+".html"); err == nil {
			r.URL.Path = "/" + path + ".html"
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
		if _, err := fs.Stat(webFS, "index.html"); err == nil {
			r.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r)
			return
		}

		http.NotFound(w, r)
	})
}

func StaticFileHandlerFromDir(dir string) http.Handler {
	return StaticFileHandler(os.DirFS(dir))
}
