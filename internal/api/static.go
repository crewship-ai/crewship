package api

import (
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// StaticFileHandler returns an HTTP handler that serves the Next.js static export from the given filesystem.
// It handles .html extension resolution, directory indexes, and SPA client-side routing fallback.
func StaticFileHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))

	// serveFile reads a file from webFS and writes it to the response.
	// This avoids http.FileServer redirects (e.g. /index.html → ./).
	//
	// Cache headers are critical: HTML must NOT be cached (it references
	// hashed chunk names that change every deploy — caching the HTML
	// pins users to a stale chunk graph), while _next/static/* chunks
	// CAN be cached forever (their filenames already include a content
	// hash, so a code change always picks a new URL). Without these
	// headers, browser default heuristics keep stale HTML for hours and
	// users see "fix not deployed" symptoms after every release.
	serveFile := func(w http.ResponseWriter, name string) {
		f, err := webFS.Open(name)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)

		switch {
		case strings.HasPrefix(name, "_next/static/"):
			// Hashed bundle assets — safe to cache for a year.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasSuffix(name, ".html"):
			// HTML and dynamic-route placeholders — always revalidate so
			// users pick up new chunk references the moment we redeploy.
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		default:
			// Everything else (favicon, robots.txt, public assets) —
			// cache for 5 minutes by default.
			w.Header().Set("Cache-Control", "public, max-age=300")
		}

		io.Copy(w, f)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Serve _next/ static assets directly via FileServer (no redirect issues)
		if strings.HasPrefix(path, "_next/") {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Try path + ".html" first (Next.js static export: /login → login.html)
		if !strings.HasSuffix(path, ".html") {
			if _, err := fs.Stat(webFS, path+".html"); err == nil {
				serveFile(w, path+".html")
				return
			}
		}

		// Try exact file (images, favicon, etc.)
		if info, err := fs.Stat(webFS, path); err == nil && !info.IsDir() {
			serveFile(w, path)
			return
		}

		// Try path + "/index.html" (directory index)
		if _, err := fs.Stat(webFS, path+"/index.html"); err == nil {
			serveFile(w, path+"/index.html")
			return
		}

		// Dynamic-route placeholder lookup: Next.js static export with
		// generateStaticParams returning [{ id: "_" }] builds the page
		// HTML at `<segment>/_.html` (the directory `<segment>/_/` is
		// only used for Next's internal manifest .txt files, no
		// index.html lives there). Rewrite a request like /chat/filip
		// → chat/_.html so the right page bundle hydrates with the
		// runtime slug (read via useParams in the client component).
		//
		// Only resolve EXACTLY one level above the leaf (parent of the
		// last segment) — walking deeper used to misroute /chat/a/b
		// onto chat/_.html, hydrating the wrong page shell. If the
		// leaf-1 lookup fails, fall through to the SPA index.
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 2 {
			parent := strings.Join(parts[:len(parts)-1], "/")
			for _, candidate := range []string{parent + "/_.html", parent + "/_/index.html"} {
				if _, err := fs.Stat(webFS, candidate); err == nil {
					slog.Debug("dynamic placeholder", "path", r.URL.Path, "served", candidate)
					serveFile(w, candidate)
					return
				}
			}
		}

		// SPA fallback: serve root index.html for client-side routing
		slog.Debug("SPA fallback", "path", r.URL.Path)
		serveFile(w, "index.html")
	})
}

// StaticFileHandlerFromDir returns a StaticFileHandler backed by the given directory path.
func StaticFileHandlerFromDir(dir string) http.Handler {
	return StaticFileHandler(os.DirFS(dir))
}
