// Package editororigin serves a qualified third-party editor bundle from a
// dedicated browser origin.
//
// The editor is foreign code. It runs only inside an iframe on its own
// origin, holds no Crewship credential and talks to the host page over one
// MessagePort. This package is the server side of that boundary:
//
//   - The editor host serves nothing except allowlisted bundle files. API,
//     SPA and probes are never reachable on it (Gate), and every variant of
//     the host name (port, trailing dot, case) lands here, not in the app.
//   - The editor document carries CSP `sandbox allow-scripts`. It runs in an
//     opaque origin: it cannot read or write cookies of any site, use storage,
//     navigate the top window, open popups or submit forms, even though the
//     editor host is a sibling of the Studio site. Policy sources therefore
//     name the editor origin explicitly instead of 'self'.
//   - Assets are public constant code, readable by any origin in CORS mode
//     (Access-Control-Allow-Origin: *, never with credentials), which is how
//     an opaque-origin document fetches its scripts and wasm.
//   - A bundle is pinned by the sha256 of its exact manifest.json bytes; the
//     manifest pins every file. Files are verified at load and served from
//     memory, so a later change on disk cannot alter what browsers receive.
//   - Inline scripts in index.html are allowed only by their sha256 hash,
//     computed from the verified file; there is no 'unsafe-inline' script.
//   - Every response carries headers set here, not by the bundle. A <meta>
//     policy inside a compromised bundle can only tighten them.
//   - Cookies are never read and never set.
package editororigin

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// BundlePrefix is the content-addressed path prefix on the editor origin.
const BundlePrefix = "/b/"

const (
	maxBundles       = 4
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

type bundle struct {
	files map[string]file
	// indexCSP adds the hashes of index.html's inline scripts.
	indexCSP string
}

// Handler serves allowlisted bundles on the editor host. A nil *Handler is a
// disabled editor origin.
type Handler struct {
	host    string
	all     bool // Deny: match every host, serve nothing.
	csp     string
	bundles map[string]bundle
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

// FrameSrc is the frame-src source that Studio adds for the editor, or ""
// when no editor origin is configured.
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

// hostname is the request host without port or trailing dot, lowercased.
func hostname(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
}

func site(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// ValidateOrigin checks the editor origin against the other browser origins of
// the instance. The editor host must differ from, and must not be a parent
// domain of, the Studio host and the Page runtime host on every port. It must
// use HTTPS and a canonical DNS name below a registrable domain, and must be on
// a different site from Studio unless developmentSameSite is set. It must not
// be a trusted API caller (CREWSHIP_ALLOWED_ORIGINS).
func ValidateOrigin(editor, studio, pageRuntime string, developmentSameSite bool, allowedOrigins []string) error {
	e, err := parseOrigin(editor)
	if err != nil {
		return fmt.Errorf("editor origin %w", err)
	}
	if e.Scheme != "https" {
		return errors.New("editor origin requires HTTPS")
	}
	host := strings.ToLower(e.Hostname())
	if net.ParseIP(host) != nil {
		return errors.New("editor origin requires a DNS name")
	}
	if strings.HasSuffix(host, ".") {
		return errors.New("editor origin must not end with a dot")
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err != nil || d == host {
		return errors.New("editor origin must be a subdomain of a registrable domain, not an apex or public suffix")
	}
	s, err := parseOrigin(studio)
	if err != nil {
		return fmt.Errorf("Studio origin %w", err)
	}
	others := []string{hostname(s.Host)}
	if pageRuntime != "" {
		r, err := parseOrigin(pageRuntime)
		if err != nil {
			return fmt.Errorf("Page runtime origin %w", err)
		}
		others = append(others, hostname(r.Host))
	}
	for _, other := range others {
		if host == other || strings.HasSuffix(other, "."+host) {
			return errors.New("editor origin must not share a host with, or be a parent of, Studio or the Page runtime")
		}
	}
	if site(host) == site(hostname(s.Host)) && !developmentSameSite {
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

func basePolicy(self, ancestor, extraScript string) string {
	return "default-src 'none'; script-src " + self + " 'wasm-unsafe-eval'" + extraScript +
		"; style-src " + self + " 'unsafe-inline'; img-src " + self + " data: blob:; font-src " + self +
		"; connect-src " + self + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " +
		ancestor + "; sandbox allow-scripts"
}

// Deny returns a handler that matches every host and serves nothing. It is
// the fail-closed answer when an editor origin is configured but unusable.
func Deny() *Handler {
	return &Handler{all: true, csp: "default-src 'none'; frame-ancestors 'none'; sandbox", bundles: map[string]bundle{}}
}

// New builds the editor origin handler. Bundles that fail verification are
// not served and are reported in the returned error, but the handler is still
// returned so that the editor host stays isolated from the main application.
// It returns nil only when the editor origin itself cannot be parsed.
func New(o Options) (*Handler, error) {
	u, err := parseOrigin(o.Origin)
	if err != nil {
		return nil, fmt.Errorf("editor origin %w", err)
	}
	self := u.Scheme + "://" + hostKey(u.Scheme, u.Host)
	var errs []error
	ancestor := CanonicalOrigin(o.StudioOrigin)
	if ancestor == "" {
		ancestor = "'none'"
		errs = append(errs, errors.New("editor origin requires a Studio origin for frame-ancestors"))
	}
	h := &Handler{
		host:    hostname(u.Host),
		csp:     basePolicy(self, ancestor, ""),
		bundles: map[string]bundle{},
	}
	if len(o.Bundles) > maxBundles {
		return h, fmt.Errorf("at most %d editor bundles may be pinned", maxBundles)
	}
	for _, digest := range o.Bundles {
		files, err := loadBundle(o.BundleDir, digest)
		if err != nil {
			errs = append(errs, fmt.Errorf("editor bundle %.12s: %w", digest, err))
			continue
		}
		extra := ""
		for _, hash := range inlineScriptHashes(files["index.html"].body) {
			extra += " " + hash
		}
		h.bundles[digest] = bundle{files: files, indexCSP: basePolicy(self, ancestor, extra)}
	}
	return h, errors.Join(errs...)
}

// inlineScriptHashes returns the CSP hash source of every inline script that
// a browser would execute, in document order without duplicates.
func inlineScriptHashes(doc []byte) []string {
	var out []string
	z := html.NewTokenizer(bytes.NewReader(doc))
	inScript, executable := false, false
	var text bytes.Buffer
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken:
			t := z.Token()
			if t.Data != "script" {
				continue
			}
			inScript, executable = true, true
			text.Reset()
			for _, a := range t.Attr {
				switch strings.ToLower(a.Key) {
				case "src":
					executable = false
				case "type":
					switch strings.ToLower(strings.TrimSpace(a.Val)) {
					case "", "module", "text/javascript", "application/javascript":
					default:
						executable = false
					}
				}
			}
		case html.TextToken:
			if inScript {
				text.Write(z.Text())
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "script" && inScript {
				if executable {
					sum := sha256.Sum256(text.Bytes())
					hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
					if !slices.Contains(out, hash) {
						out = append(out, hash)
					}
				}
				inScript = false
			}
		}
	}
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
	// os.Root refuses symlinks and .. that leave the bundle directory, and
	// readRegular refuses a symlink as the file itself. Content is pinned by
	// hash either way.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	bundleRoot, err := root.OpenRoot(digest)
	if err != nil {
		return nil, err
	}
	defer bundleRoot.Close()
	raw, err := readRegular(bundleRoot, "manifest.json", maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("manifest.json does not match the pin")
	}
	var m manifest
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if len(m.Files) == 0 || len(m.Files) > maxBundleFiles {
		return nil, errors.New("manifest must list between 1 and 512 files")
	}
	if _, ok := m.Files["index.html"]; !ok {
		return nil, errors.New("manifest does not list index.html")
	}
	files := make(map[string]file, len(m.Files))
	total := 0
	for name, sum := range m.Files {
		if !validName(name) || !IsDigest(sum) {
			return nil, fmt.Errorf("manifest entry %q is not a clean relative path with a digest", name)
		}
		body, err := readRegular(bundleRoot, name, maxBundleBytes-total)
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

// MatchesHost reports whether a request Host header names the editor host,
// ignoring case, port and a trailing dot.
func (h *Handler) MatchesHost(host string) bool {
	return h != nil && (h.all || hostname(host) == h.host)
}

func (h *Handler) setHeaders(w http.ResponseWriter, csp string) {
	hdr := w.Header()
	hdr.Del("X-Frame-Options")
	hdr.Set("Content-Security-Policy", csp)
	// Fail closed: a document framed under COEP is embeddable only through a
	// credentialless iframe. Do not relax to cross-origin.
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
	hdr.Set("Origin-Agent-Cluster", "?1")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Strict-Transport-Security", "max-age=31536000")
	hdr.Set("Permissions-Policy", permissionsPolicy)
	hdr.Set("Cache-Control", "no-store")
}

// ServeHTTP serves one verified bundle file. It never looks at cookies or
// credentials; the bundle is public, constant code with no user data.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setHeaders(w, h.csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), BundlePrefix)
	digest, name, ok2 := strings.Cut(rest, "/")
	b, known := h.bundles[digest]
	f, found := b.files[name]
	if !ok || !ok2 || !known || !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if f.index {
		w.Header().Set("Content-Security-Policy", b.indexCSP)
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Access-Control-Allow-Origin", "*")
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
