package api

import (
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// sensitiveSPAFallbackBackupSuffixes are file extensions that strongly
// suggest a backup/swap/editor artifact and should never resolve via the
// SPA fallback. Real Next.js routes don't end with these.
var sensitiveSPAFallbackBackupSuffixes = []string{".bak", ".old", ".orig", ".save", ".swp", ".tmp", ".backup", ".sav", "~"}

// isSensitiveSPAFallbackPath reports whether a request path is one that
// scanners commonly probe (dotfiles, VCS dirs, editor backups) and should
// 404 rather than fall through to the SPA index. This stops content-
// discovery fuzzers from drowning in 200 OK noise and prevents the
// impression that paths like /.htpasswd or /.git/config "exist".
//
// `.well-known` is exempted at the segment level only (RFC 8615), not
// for the whole path: /.well-known/.git/config is still blocked because
// the nested `.git` segment is independently sensitive. Without this
// scoping a future RFC 8615 deployment that nests user content under
// .well-known would inherit a probe-friendly subtree by accident.
func isSensitiveSPAFallbackPath(p string) bool {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".well-known" {
			continue
		}
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	low := strings.ToLower(p)
	for _, suf := range sensitiveSPAFallbackBackupSuffixes {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return false
}

// webPlaceholderFile mirrors web.PlaceholderFile — the tracked stub that
// keeps web/out/ non-empty so `//go:embed all:out` resolves in a fresh
// clone or worktree (#1567). Duplicated rather than imported because the
// web package is `//go:build !clionly` and internal/api is not.
const webPlaceholderFile = ".placeholder.html"

// placeholderHandler serves the "web UI was not built" page on every
// route with 503.
//
// The point is that the degraded binary ANNOUNCES itself. Before the
// tracked placeholder existed, contributors unblocked the embed by hand
// with `echo '<!doctype html>' > web/out/index.html`; the resulting
// binary served a blank HTTP 200, which reads as a broken frontend
// rather than a skipped build step. A 503 plus a page that names the
// command to run cannot be mistaken for a working UI — by a human or by
// an uptime check.
func placeholderHandler(body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		// Machine-readable twin of the 503, so a deploy check or a
		// smoke test can assert "this binary has a real UI" without
		// scraping the HTML.
		w.Header().Set("X-Crewship-Web-Build", "placeholder")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodHead {
			return
		}
		w.Write(body)
	})
}

// StaticFileHandler returns an HTTP handler that serves the Next.js static export from the given filesystem.
// It handles .html extension resolution, directory indexes, and SPA client-side routing fallback.
func StaticFileHandler(webFS fs.FS) http.Handler {
	// Degraded mode: web/out/ holds only the tracked placeholder, i.e.
	// `pnpm build` + the embed sync never ran for this binary. There is
	// no export to serve, so serve the explanation instead of letting
	// every route fall through to a missing index.html.
	if _, err := fs.Stat(webFS, "index.html"); err != nil {
		if body, readErr := fs.ReadFile(webFS, webPlaceholderFile); readErr == nil {
			return placeholderHandler(body)
		}
	}

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

		// Serve _next/ static assets directly via FileServer (no redirect
		// issues). Hashed chunk filenames already encode content, so a
		// long-lived cache is safe and avoids re-downloading the same
		// JS/CSS on every navigation.
		if strings.HasPrefix(path, "_next/") {
			// Block directory listings. http.FileServer's default
			// behaviour when the path resolves to a directory and has
			// no index.html is to emit an HTML autoindex -- which on
			// /_next/static/* enumerates every chunk filename + build
			// ID. The chunk URLs themselves are already public (the
			// HTML references them); the *listing* is what exposes the
			// full deploy surface. Trailing slash is the canonical
			// signal; the no-trailing-slash variant is caught by the
			// pre-Stat below before FileServer's redirect normalises
			// it. Audit M10.
			if strings.HasSuffix(path, "/") {
				http.NotFound(w, r)
				return
			}
			if info, err := fs.Stat(webFS, path); err == nil && info.IsDir() {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(path, "_next/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
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

		// Block sensitive paths (dotfiles, VCS dirs, editor backups) from
		// falling through to SPA. ffuf/Nikto/etc. otherwise see HTTP 200
		// for every probe, drowning real findings and leaking the illusion
		// that paths like /.htpasswd or /.git/config exist. .well-known/*
		// is preserved (RFC 8615).
		if isSensitiveSPAFallbackPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		// SPA fallback: serve root index.html for client-side routing
		slog.Debug("SPA fallback", "path", r.URL.Path)
		serveFile(w, "index.html")
	})
}
