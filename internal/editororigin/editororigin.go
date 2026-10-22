// Package editororigin serves a qualified third-party editor bundle from a
// dedicated browser origin.
//
// The editor is foreign code. It runs only inside an iframe on its own
// origin, holds no Crewship credential and talks to the host page over one
// MessagePort. This package is the server side of that boundary:
//
//   - The editor host serves nothing except allowlisted bundle files. API,
//     SPA and probes are never reachable on it (Gate), so connect-src 'self'
//     inside the frame cannot reach Crewship data.
//   - Only bundles whose index.html digest is in the operator's allowlist are
//     served, from content-addressed paths /b/<sha256>/<file>. Every file is
//     verified against the bundle manifest at load and served from memory, so
//     a later change on disk cannot alter what browsers receive.
//   - Every response carries headers set here, not by the bundle. A <meta>
//     policy inside a compromised bundle can only tighten them.
//   - Cookies are never read and never set.
package editororigin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// BundlePrefix is the content-addressed path prefix on the editor origin.
const BundlePrefix = "/b/"

const (
	maxBundleBytes   = 64 << 20
	maxManifestBytes = 1 << 20
	maxBundleFiles   = 512
)

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), clipboard-read=(), usb=(), serial=(), hid=()"

// Options configures the editor origin. Origin and StudioOrigin are browser
// origins; BundleDir holds one directory per allowlisted digest.
type Options struct {
	Origin       string
	StudioOrigin string
	BundleDir    string
	Bundles      []string
}

type file struct {
	body        []byte
	contentType string
	index       bool
}

// Handler serves allowlisted bundles on the editor host. A nil *Handler is a
// disabled editor origin.
type Handler struct {
	host    string
	csp     string
	bundles map[string]map[string]file
}

// IsDigest reports whether s is a lowercase hex SHA-256 digest, the only
// accepted bundle pin.
func IsDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// CanonicalOrigin serializes an origin the way a browser does: lowercase
// host, no default port, no trailing slash. It returns "" for anything that
// is not a plain scheme://host[:port].
func CanonicalOrigin(raw string) string {
	u, err := parseOrigin(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + hostKey(u.Scheme, u.Host)
}

// FrameSrc is the frame-src source that the Studio page adds for the editor,
// or "" when no editor origin is configured.
func FrameSrc(origin string) string {
	if strings.TrimSpace(origin) == "" {
		return ""
	}
	return CanonicalOrigin(origin)
}

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("must be an HTTP(S) origin without credentials, paths or queries")
	}
	return u, nil
}

// hostKey lowercases host and drops the scheme's default port.
func hostKey(scheme, host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if (scheme == "https" && strings.HasSuffix(host, ":443")) || (scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	return host
}

func site(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// ValidateOrigin checks the editor origin against the other browser origins of
// the instance. The editor host must differ from the Studio host and the Page
// runtime host on every port, must use HTTPS and a DNS name, and must be on a
// different site from Studio unless developmentSameSite is set. It must not be
// a trusted API caller (CREWSHIP_ALLOWED_ORIGINS).
func ValidateOrigin(editor, studio, pageRuntime string, developmentSameSite bool, allowedOrigins []string) error {
	e, err := parseOrigin(editor)
	if err != nil {
		return fmt.Errorf("editor origin %w", err)
	}
	if e.Scheme != "https" {
		return errors.New("editor origin requires HTTPS")
	}
	if net.ParseIP(e.Hostname()) != nil {
		return errors.New("editor origin requires a DNS name")
	}
	s, err := parseOrigin(studio)
	if err != nil {
		return fmt.Errorf("Studio origin %w", err)
	}
	if strings.EqualFold(e.Hostname(), s.Hostname()) {
		return errors.New("editor origin must not share a host with Studio")
	}
	if pageRuntime != "" {
		r, err := parseOrigin(pageRuntime)
		if err != nil {
			return fmt.Errorf("Page runtime origin %w", err)
		}
		if strings.EqualFold(e.Hostname(), r.Hostname()) {
			return errors.New("editor origin must not share a host with the Page runtime")
		}
	}
	if site(e.Hostname()) == site(s.Hostname()) && !developmentSameSite {
		return errors.New("editor origin must use a different site from Studio; a sibling subdomain needs the explicit development exception")
	}
	self := CanonicalOrigin(editor)
	for _, a := range allowedOrigins {
		if CanonicalOrigin(a) == self {
			return errors.New("editor origin must not be a trusted API origin")
		}
	}
	return nil
}

// New builds the editor origin handler. Bundles that fail verification are
// not served and are reported in the returned error, but the handler is still
// returned so that the editor host stays isolated from the main application.
func New(o Options) (*Handler, error) {
	u, err := parseOrigin(o.Origin)
	if err != nil {
		return nil, fmt.Errorf("editor origin %w", err)
	}
	ancestor := CanonicalOrigin(o.StudioOrigin)
	if ancestor == "" {
		return nil, errors.New("editor origin requires a Studio origin for frame-ancestors")
	}
	h := &Handler{
		host:    hostKey(u.Scheme, u.Host),
		csp:     "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + ancestor,
		bundles: map[string]map[string]file{},
	}
	var errs []error
	for _, digest := range o.Bundles {
		files, err := loadBundle(o.BundleDir, digest)
		if err != nil {
			errs = append(errs, fmt.Errorf("editor bundle %.12s: %w", digest, err))
			continue
		}
		h.bundles[digest] = files
	}
	return h, errors.Join(errs...)
}

type manifest struct {
	Files map[string]string `json:"files"`
}

func loadBundle(dir, digest string) (map[string]file, error) {
	if !IsDigest(digest) {
		return nil, errors.New("pin must be a lowercase hex SHA-256 digest")
	}
	if dir == "" {
		return nil, errors.New("bundle directory is not configured")
	}
	// os.Root refuses symlinks and .. that leave the bundle directory.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	bundle, err := root.OpenRoot(digest)
	if err != nil {
		return nil, err
	}
	defer bundle.Close()
	raw, err := readRegular(bundle, "manifest.json", maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if len(m.Files) == 0 || len(m.Files) > maxBundleFiles {
		return nil, errors.New("manifest must list between 1 and 512 files")
	}
	if m.Files["index.html"] != digest {
		return nil, errors.New("index.html digest does not match the pin")
	}
	files := make(map[string]file, len(m.Files))
	total := 0
	for name, sum := range m.Files {
		if !validName(name) || !IsDigest(sum) {
			return nil, fmt.Errorf("manifest entry %q is not a clean relative path with a digest", name)
		}
		body, err := readRegular(bundle, name, maxBundleBytes-total)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		total += len(body)
		got := sha256.Sum256(body)
		if hex.EncodeToString(got[:]) != sum {
			return nil, fmt.Errorf("%s does not match its manifest digest", name)
		}
		files[name] = file{body: body, contentType: contentType(name), index: name == "index.html"}
	}
	return files, nil
}

func validName(name string) bool {
	return name != "" && name != "manifest.json" && !strings.HasPrefix(name, "/") &&
		!strings.ContainsAny(name, "\\\x00?#%") && path.Clean(name) == name && fs.ValidPath(name)
}

func readRegular(root *os.Root, name string, limit int) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, errors.New("bundle exceeds 64 MiB")
	}
	return body, nil
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}

// MatchesHost reports whether a request Host header names the editor origin.
func (h *Handler) MatchesHost(host string) bool {
	return h != nil && hostKey("https", host) == h.host
}

func (h *Handler) setHeaders(w http.ResponseWriter) {
	hdr := w.Header()
	hdr.Del("X-Frame-Options")
	hdr.Set("Content-Security-Policy", h.csp)
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
	hdr.Set("Origin-Agent-Cluster", "?1")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Permissions-Policy", permissionsPolicy)
	hdr.Set("Cache-Control", "no-store")
}

// ServeHTTP serves one verified bundle file. It never looks at cookies or
// credentials; the bundle is public, constant code with no user data.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), BundlePrefix)
	digest, name, ok2 := strings.Cut(rest, "/")
	f, found := h.bundles[digest][name]
	if !ok || !ok2 || !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !f.index {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(f.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(f.body)
	}
}

// Gate routes every request for the editor host to h and everything else to
// next. With a nil h it returns next unchanged.
func Gate(h *Handler, next http.Handler) http.Handler {
	if h == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.MatchesHost(r.Host) {
			h.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
