// Package editororigin serves a qualified third-party editor bundle in an
// isolated browser context.
//
// The editor is foreign code. It runs only inside a sandboxed iframe with an
// opaque origin, holds no Crewship credential and talks to the host page over
// one MessagePort. This package is the server side of that boundary. It has
// two modes:
//
//   - Dedicated origin (optional hardening): an operator-configured host
//     serves nothing except allowlisted bundle files under /b/. API, SPA and
//     probes are never reachable on it (Gate), and every variant of the host
//     name (port, trailing dot, case) lands here, not in the app.
//   - Same host (zero-config default): without a dedicated origin, bundles
//     are served under SameHostPrefix on the Studio origin. Every request
//     whose path starts with the prefix (in any case or encoding) lands here,
//     never in the app, and nothing under it redirects. The Studio origin is
//     the one each request reached (its Host and scheme), unless the
//     operator configured a public Studio origin, which then is the only
//     host the editor answers on. Policy sources are
//     scoped to the bundle path, so the opaque-origin document cannot fetch
//     the Studio API or load Studio scripts, and Studio's frame-src names the
//     bundle path only, so a frame that navigates itself cannot reach a
//     Studio document. Both hold only while nothing in front of Crewship
//     redirects or normalises the prefix; CheckProxy verifies that through
//     the public URL browsers use and a confirmed failure disables the
//     editor on that origin until the check passes again. Requests a
//     credentialless sandboxed frame would not make are refused: Crewship's
//     own cookies (the iframe lacks credentialless; cookies a proxy sets are
//     ignored), a document that is not a frame, and assets not requested
//     cross-site (Studio loading the foreign code into its own origin).
//     Responses carry Studio's HSTS and are never stored by shared caches.
//
// In both modes:
//   - The editor document carries CSP `sandbox allow-scripts`. It runs in an
//     opaque origin: it cannot read or write cookies of any site, use storage,
//     navigate the top window, open popups or submit forms, even though the
//     editor host is a sibling of the Studio site. Policy sources therefore
//     name the editor origin explicitly instead of 'self'.
//   - Every request from the opaque-origin document is cross-origin. Assets
//     are public constant code: CORS loads get Access-Control-Allow-Origin: *
//     (never credentials) and no-cors loads (<link>, <img>, classic
//     <script src>) get Cross-Origin-Resource-Policy: cross-origin. Only the
//     document keeps CORP same-origin, which binds it to a credentialless
//     iframe under Studio's COEP.
//   - A bundle is pinned by the sha256 of its exact manifest.json bytes; the
//     manifest pins every file. Files are verified at load and served from
//     memory, so a later change on disk cannot alter what browsers receive.
//   - Inline scripts in index.html are allowed only by their sha256 hash,
//     computed from the verified file; there is no 'unsafe-inline' script.
//   - Every response carries headers set here, not by the bundle. A <meta>
//     policy inside a compromised bundle can only tighten them.
//   - The policy blocks WebRTC (webrtc 'block'), which connect-src does not
//     govern. Browsers do not all enforce that directive yet, so bundles
//     that reference the WebRTC API are refused at load (qualification).
//   - Cookies are never set. In same-host mode only their names are read,
//     to refuse a frame that carries Crewship's own.
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

// SameHostPrefix is where the editor is served on the Studio origin when no
// dedicated editor origin is configured: <Studio>/_editor/b/<digest>/<file>.
const SameHostPrefix = "/_editor"

// sameHostVary names every request header a same-host response depends on,
// so no cache replays an asset to a request it would refuse.
const sameHostVary = "Cookie, Sec-Fetch-Site, Sec-Fetch-Dest"

// StudioHSTS is the Strict-Transport-Security value of every Studio
// response. Same-host editor responses send the same value: a browser
// replaces a host's stored HSTS policy with each one it receives, so a
// weaker header on the editor would drop includeSubDomains for the host.
const StudioHSTS = "max-age=31536000; includeSubDomains"

// dedicatedHSTS is sent by a dedicated editor host, which has no subdomains
// of its own to cover.
const dedicatedHSTS = "max-age=31536000"

const (
	maxBundles       = 4
	maxBundleBytes   = 64 << 20
	maxManifestBytes = 1 << 20
	maxBundleFiles   = 512
)

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), clipboard-read=(), usb=(), serial=(), hid=()"

// Options configures the editor origin. Origin and StudioOrigin are browser
// origins; BundleDir holds one directory per allowlisted digest.
//
// SameHost serves the editor under SameHostPrefix on the Studio origin
// instead of a dedicated Origin, which must then be empty. In that mode
// StudioOrigin is optional: empty (the zero-config default) uses the origin
// of each request, built from its Host and Scheme; set, it is the only
// origin the editor answers on. Requests on PageRuntimeOrigin are refused.
type Options struct {
	Origin            string
	SameHost          bool
	StudioOrigin      string
	PageRuntimeOrigin string
	// Scheme returns the scheme a browser used for r ("https" or "http").
	// Nil uses TLS on the connection. Crewship passes its trusted-proxy rule
	// (X-Forwarded-Proto only from a trusted proxy).
	Scheme func(r *http.Request) string
	// LoopbackServer: the server listens on loopback only (development).
	// Only then may the same-host self-check learn and probe loopback
	// origins; otherwise a signed-in user could point it at services on the
	// server's own loopback interface.
	LoopbackServer bool
	BundleDir      string
	Bundles        []string
	// ApplicationPackages contains digest-named directories from immutable
	// release assets only. Never construct it from a request or mutable storage.
	// Every package is verified and copied with the ordinary bundle limits.
	ApplicationPackages fs.FS
}

type file struct {
	body        []byte
	contentType string
	index       bool
}

type bundle struct {
	files map[string]file
	// inline holds the CSP hash sources of index.html's inline scripts.
	inline string
}

// Handler serves allowlisted bundles on the editor host. A nil *Handler is a
// disabled editor origin.
type Handler struct {
	host     string
	all      bool // Deny: match every host, serve nothing.
	sameHost bool // Match SameHostPrefix on any host instead of a host name.
	// base is the dedicated editor origin. In same-host mode it is unused.
	base string
	// ancestor is the frame-ancestors source of a dedicated editor.
	ancestor string
	// broken: same-host mode with an unusable configured Studio origin; the
	// prefix is owned and nothing is served.
	broken bool
	// pinned is the configured public Studio origin (same host), or "".
	pinned  string
	runtime string
	// loopback: the server listens on loopback only (Options.LoopbackServer).
	loopback bool
	scheme   func(*http.Request) string
	bundles  map[string]bundle
	// order lists the loaded bundles in allowlist order; the first one is
	// the bundle new editor sessions frame (SessionBundle).
	order []string
	check proxyState
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

// trustsOpaqueOrigin reports whether the API would accept requests whose Origin
// is "null", the origin of every sandboxed editor document.
func trustsOpaqueOrigin(allowedOrigins []string) bool {
	for _, a := range allowedOrigins {
		if strings.EqualFold(strings.TrimSpace(a), "null") {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ValidateSameHost checks the zero-config mode, in which the editor is served
// under SameHostPrefix on the Studio origin. studio is the configured public
// Studio origin, or "" (the default) to use the origin of each request. A
// configured Studio must use HTTPS (plain HTTP only on loopback, where
// browsers still treat it as a secure context) and must not be the Page
// runtime origin: Studio would then list its own whole origin in frame-src,
// and a self-navigated editor frame could land on a framable Studio
// document. Without one, requests on the Page runtime origin are refused at
// request time. The opaque origin "null" must not be a trusted API caller.
func ValidateSameHost(studio, pageRuntime string, allowedOrigins []string) error {
	if strings.TrimSpace(studio) != "" {
		s, err := parseOrigin(studio)
		if err != nil {
			return fmt.Errorf("Studio origin %w", err)
		}
		if s.Scheme != "https" && !isLoopback(strings.ToLower(s.Hostname())) {
			return errors.New("the same-host editor requires an HTTPS Studio origin (plain HTTP only on loopback)")
		}
		if pageRuntime != "" && CanonicalOrigin(pageRuntime) == CanonicalOrigin(studio) {
			return errors.New("the same-host editor cannot be combined with a Page runtime on the Studio origin; configure a Page runtime origin or a dedicated editor origin")
		}
	}
	if trustsOpaqueOrigin(allowedOrigins) {
		return errors.New("the opaque origin \"null\" must not be a trusted API origin while the editor is configured")
	}
	return nil
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
	if trustsOpaqueOrigin(allowedOrigins) {
		return errors.New("the opaque origin \"null\" must not be a trusted API origin while the editor is configured")
	}
	return nil
}

// RouteHeader marks every response of a same-host Crewship: "editor" from
// the editor handler, "app" from everything else. CheckProxy uses it to
// recognise, positively, a request under the prefix that Crewship answered
// outside the editor.
const RouteHeader = "X-Crewship-Route"

func basePolicy(self, ancestor, extraScript string) string {
	return "default-src 'none'; script-src " + self + " 'wasm-unsafe-eval'" + extraScript +
		"; style-src " + self + " 'unsafe-inline'; img-src " + self + " data: blob:; font-src " + self +
		"; connect-src " + self + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " +
		ancestor + "; webrtc 'block'; sandbox allow-scripts"
}

const denyCSP = "default-src 'none'; frame-ancestors 'none'; sandbox"

// sameHostDenyCSP answers same-host requests that have no usable origin. It
// keeps the editor's shape (CheckProxy and clients recognise the handler)
// with no source and no ancestor.
var sameHostDenyCSP = basePolicy("'none'", "'none'", "")

// Deny returns a handler that matches every host and serves nothing. It is
// the fail-closed answer when an editor origin is configured but unusable.
func Deny() *Handler {
	return &Handler{all: true, bundles: map[string]bundle{}}
}

// New builds the editor origin handler. Bundles that fail verification are
// not served and are reported in the returned error, but the handler is still
// returned so that the editor host stays isolated from the main application.
// It returns nil only when a dedicated editor origin cannot be parsed. In
// same-host mode it never returns nil: an unusable Studio origin yields a
// handler that owns the prefix and serves nothing, so the application stays
// up and the prefix never reaches it.
func New(o Options) (*Handler, error) {
	var errs []error
	var h *Handler
	if o.SameHost {
		h = &Handler{sameHost: true, scheme: o.Scheme, runtime: CanonicalOrigin(o.PageRuntimeOrigin), loopback: o.LoopbackServer, bundles: map[string]bundle{}}
		h.check.init()
		if strings.TrimSpace(o.Origin) != "" {
			h.broken = true
			return h, errors.New("same-host editor must not also name a dedicated editor origin")
		}
		if strings.TrimSpace(o.StudioOrigin) != "" {
			h.pinned = CanonicalOrigin(o.StudioOrigin)
			if h.pinned == "" {
				h.broken = true
				return h, errors.New("the configured Studio origin is not an HTTP(S) origin; the same-host editor serves nothing")
			}
		}
	} else {
		ancestor := CanonicalOrigin(o.StudioOrigin)
		if ancestor == "" {
			ancestor = "'none'"
			errs = append(errs, errors.New("editor origin requires a Studio origin for frame-ancestors"))
		}
		u, err := parseOrigin(o.Origin)
		if err != nil {
			return nil, fmt.Errorf("editor origin %w", err)
		}
		self := u.Scheme + "://" + hostKey(u.Scheme, u.Host)
		h = &Handler{host: hostname(u.Host), base: self, ancestor: ancestor, bundles: map[string]bundle{}}
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
		h.bundles[digest] = bundle{files: files, inline: extra}
		h.order = append(h.order, digest)
	}
	if err := h.loadCompiledPackages(); err != nil {
		errs = append(errs, err)
	}
	if o.ApplicationPackages != nil {
		if err := h.loadPackages(o.ApplicationPackages, "."); err != nil {
			errs = append(errs, err)
		}
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

// manifestKeys are the only top-level manifest keys. Matching is exact and
// case-sensitive, like the operator's installer, so every reader of a pinned
// manifest sees the same file set.
var manifestKeys = map[string]bool{"files": true, "source_commit": true, "toolchain": true}

// decodeManifest returns the files map of a manifest, refusing unknown,
// case-variant or duplicate keys at any level, non-string digests and any
// data after the object. encoding/json alone would match keys
// case-insensitively and merge duplicates.
func decodeManifest(raw []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var files map[string]string
	if err := objectEach(dec, func(key string) error {
		if !manifestKeys[key] {
			return fmt.Errorf("unknown manifest key %q", key)
		}
		if key != "files" {
			return skipValue(dec)
		}
		files = map[string]string{}
		return objectEach(dec, func(name string) error {
			var sum string
			if err := dec.Decode(&sum); err != nil {
				return fmt.Errorf("manifest digest for %q: %w", name, err)
			}
			files[name] = sum
			return nil
		})
	}); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("manifest has data after the object")
	}
	if files == nil {
		return nil, errors.New("manifest has no files object")
	}
	return files, nil
}

// objectEach reads one JSON object, calling value for each key exactly once.
func objectEach(dec *json.Decoder, value func(key string) error) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("manifest expects a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string)
		if seen[key] {
			return fmt.Errorf("duplicate manifest key %q", key)
		}
		seen[key] = true
		if err := value(key); err != nil {
			return err
		}
	}
	_, err := dec.Token()
	return err
}

// skipValue consumes one JSON value, rejecting duplicate keys inside it.
func skipValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			if seen[key] {
				return fmt.Errorf("duplicate manifest key %q", key)
			}
			seen[key] = true
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
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
	return verifyBundle(digest, func(name string, limit int) ([]byte, error) { return readRegular(bundleRoot, name, limit) })
}

func verifyBundle(digest string, read func(string, int) ([]byte, error)) (map[string]file, error) {
	raw, err := read("manifest.json", maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("manifest.json does not match the pin")
	}
	manifestFiles, err := decodeManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if len(manifestFiles) == 0 || len(manifestFiles) > maxBundleFiles {
		return nil, errors.New("manifest must list between 1 and 512 files")
	}
	if _, ok := manifestFiles["index.html"]; !ok {
		return nil, errors.New("manifest does not list index.html")
	}
	files := make(map[string]file, len(manifestFiles))
	total := 0
	for name, sum := range manifestFiles {
		if !validName(name) || !IsDigest(sum) {
			return nil, fmt.Errorf("manifest entry %q is not a clean relative path with a digest", name)
		}
		body, err := read(name, maxBundleBytes-total)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		total += len(body)
		got := sha256.Sum256(body)
		if hex.EncodeToString(got[:]) != sum {
			return nil, fmt.Errorf("%s does not match its manifest digest", name)
		}
		if api := webRTCReference(name, body); api != "" {
			return nil, fmt.Errorf("%s references the WebRTC API (%s); a qualified editor bundle must not use WebRTC", name, api)
		}
		files[name] = file{body: body, contentType: contentType(name), index: name == "index.html"}
	}
	return files, nil
}

// webRTCAPIs are the WebRTC interfaces whose use opens a network channel
// that CSP connect-src does not govern. Matching is on the plain name, which
// also covers vendor-prefixed spellings (webkitRTCPeerConnection). It is a
// qualification check against a supplier bundle using WebRTC, not a
// boundary against deliberately obfuscated code.
var webRTCAPIs = [][]byte{
	[]byte("RTCPeerConnection"),
	[]byte("RTCDataChannel"),
	[]byte("RTCIceTransport"),
	[]byte("RTCIceCandidate"),
	[]byte("RTCSessionDescription"),
	[]byte("RTCRtpSender"),
	[]byte("RTCRtpReceiver"),
	[]byte("RTCRtpTransceiver"),
	[]byte("RTCSctpTransport"),
	[]byte("RTCDtlsTransport"),
}

// webRTCReference returns the first WebRTC API a code or markup file names,
// or "". Images and fonts are not scanned.
func webRTCReference(name string, body []byte) string {
	switch path.Ext(name) {
	case ".js", ".mjs", ".wasm", ".html", ".json", ".svg":
	default:
		return ""
	}
	for _, api := range webRTCAPIs {
		if bytes.Contains(body, api) {
			return string(api)
		}
	}
	return ""
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
// ignoring case, port and a trailing dot. A same-host handler matches no host.
func (h *Handler) MatchesHost(host string) bool {
	return h != nil && (h.all || !h.sameHost && hostname(host) == h.host)
}

// Matches reports whether r belongs to the editor: the editor host, or in
// same-host mode any path under SameHostPrefix in any case, decoded or not.
// Matching wider than ServeHTTP serves is deliberate: an odd spelling of the
// prefix gets the editor's 404, never the application.
func (h *Handler) Matches(r *http.Request) bool {
	if h == nil {
		return false
	}
	if !h.sameHost {
		return h.MatchesHost(r.Host)
	}
	for _, p := range []string{r.URL.Path, r.URL.EscapedPath()} {
		p = strings.ToLower(p)
		if p == SameHostPrefix || strings.HasPrefix(p, SameHostPrefix+"/") {
			return true
		}
	}
	return false
}

// errMisdirected, errBadHost and errRuntimeHost explain why a same-host
// request has no Studio origin the editor may use.
var (
	errMisdirected = errors.New("the editor answers only on the configured Studio origin")
	errBadHost     = errors.New("the request does not name a valid host")
	errRuntimeHost = errors.New("the editor is not served on the Page runtime origin")
)

// requestHost validates a Host header as a CSP host-source can name it: a
// DNS name or IPv4 address (letters, digits, '-' and '.' in non-empty
// labels, an optional trailing dot) with an optional numeric port. IPv6
// literals cannot appear in a CSP source and are refused. The result is
// lowercased.
func requestHost(raw string) (string, bool) {
	host := strings.ToLower(raw)
	name, port := host, ""
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		name, port = host[:i], host[i+1:]
		if port == "" || len(port) > 5 {
			return "", false
		}
		n := 0
		for _, c := range port {
			if c < '0' || c > '9' {
				return "", false
			}
			n = n*10 + int(c-'0')
		}
		if n == 0 || n > 65535 {
			return "", false
		}
	}
	if name == "" || len(name) > 253 {
		return "", false
	}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return host, true
}

// requestScheme is the scheme a browser used for r: the configured rule, or
// TLS on the connection. Anything but "https" is "http". A CSP source with
// the http scheme also matches https (CSP3 scheme-part matching), so a
// request whose TLS ended at a proxy Crewship does not trust still gets a
// policy that holds for the browser's https origin.
func (h *Handler) requestScheme(r *http.Request) string {
	if h.scheme != nil {
		if h.scheme(r) == "https" {
			return "https"
		}
		return "http"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// StudioOriginFor is the Studio origin of a same-host request: the
// configured one, which the request's Host must name, or the origin the
// request reached. It fails for a dedicated or disabled editor.
func (h *Handler) StudioOriginFor(r *http.Request) (string, error) {
	if h == nil || !h.sameHost || h.broken {
		return "", errors.New("no same-host editor")
	}
	host, ok := requestHost(r.Host)
	if !ok {
		return "", errBadHost
	}
	if h.pinned != "" {
		u, _ := url.Parse(h.pinned)
		if hostKey(u.Scheme, host) != u.Host {
			return "", errMisdirected
		}
		return h.pinned, nil
	}
	scheme := h.requestScheme(r)
	origin := scheme + "://" + hostKey(scheme, host)
	if h.runtime != "" {
		for _, s := range []string{"http", "https"} {
			if h.runtime == s+"://"+hostKey(s, host) {
				return "", errRuntimeHost
			}
		}
	}
	return origin, nil
}

// FrameSource is the source Studio adds to frame-src for a dedicated
// editor: its origin. A same-host editor has no fixed source (FrameSourceFor);
// it and a disabled editor return "".
func (h *Handler) FrameSource() string {
	if h == nil || h.sameHost || h.all {
		return ""
	}
	return h.base
}

// FrameSourceFor is the source Studio adds to frame-src of the response to
// r: the dedicated origin, or in same-host mode the bundle path on the
// request's Studio origin (never the bare origin). It is "" when nothing may
// be framed.
func (h *Handler) FrameSourceFor(r *http.Request) string {
	if h == nil || !h.sameHost {
		return h.FrameSource()
	}
	origin, err := h.StudioOriginFor(r)
	if err != nil {
		return ""
	}
	return origin + SameHostPrefix + BundlePrefix
}

// IndexURL is the editor document of the bundle pinned by digest, for the
// host page's iframe src; "" when nothing may be framed. In same-host mode
// it is the path alone (/_editor/b/<digest>/index.html), which the host page
// resolves against its own origin, so it never depends on how a proxy
// presents the Host or scheme to Crewship.
func (h *Handler) IndexURL(digest string) string {
	if h == nil || h.all || h.broken {
		return ""
	}
	if h.sameHost {
		return SameHostPrefix + BundlePrefix + digest + "/index.html"
	}
	return h.base + BundlePrefix + digest + "/index.html"
}

// SessionBundle names the bundle a host page frames for a new editor session
// opened by r, and that bundle's editor document URL (IndexURL): the first
// bundle of the allowlist that loaded. Both are "" while nothing may be framed
// for r: no editor, no loaded bundle, an unusable same-host configuration, a
// request whose origin the same-host editor does not answer on, or an origin
// the proxy self-check disabled.
func (h *Handler) SessionBundle(r *http.Request) (digest, indexURL string) {
	if h == nil || len(h.order) == 0 {
		return "", ""
	}
	return h.PinnedBundle(r, h.order[0])
}

// HasBundle reports whether the exact digest identifies a verified, loaded
// bundle. It does not authorize a browser request or select an application.
func (h *Handler) HasBundle(digest string) bool {
	if h == nil || h.all || h.broken || !IsDigest(digest) {
		return false
	}
	_, loaded := h.bundles[digest]
	return loaded
}

// PinnedBundle resolves a release-selected verified bundle without substituting
// the first allowlisted application's editor. Request-origin and proxy isolation
// checks are identical to SessionBundle; an unavailable pin fails closed.
func (h *Handler) PinnedBundle(r *http.Request, digest string) (string, string) {
	if !h.HasBundle(digest) {
		return "", ""
	}
	if h.sameHost {
		origin, err := h.StudioOriginFor(r)
		if err != nil || h.check.disabledFor(origin) {
			return "", ""
		}
	}
	indexURL := h.IndexURL(digest)
	if indexURL == "" {
		return "", ""
	}
	return digest, indexURL
}

// IsCrewshipCookie reports whether a cookie name is one Crewship issues on
// the Studio origin: the auth session, refresh and CSRF cookies (with or
// without the __Secure-/__Host- prefix, current and legacy NextAuth names)
// and the UI's own cookie. Names are compared case-insensitively.
func IsCrewshipCookie(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if rest, ok := strings.CutPrefix(n, "__secure-"); ok {
		n = rest
	} else if rest, ok := strings.CutPrefix(n, "__host-"); ok {
		n = rest
	}
	return strings.HasPrefix(n, "authjs.") || strings.HasPrefix(n, "next-auth.") || n == "sidebar_state"
}

// carriesCrewshipCookie reports whether any Cookie header of r names a
// Crewship cookie. Values are never looked at.
func carriesCrewshipCookie(r *http.Request) bool {
	for _, line := range r.Header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(pair, "=")
			if IsCrewshipCookie(name) {
				return true
			}
		}
	}
	return false
}

// sameHostRequestAllowed reports whether r looks like a request from a
// credentialless sandboxed editor frame. Only such a frame may receive
// bundle content on the Studio origin:
//   - no Crewship cookie: a credentialless iframe starts with an empty
//     ephemeral cookie jar, an iframe without the attribute sends Studio's
//     session and CSRF cookies. The ephemeral jar is shared by the whole
//     Studio document, so cookies that a proxy, load balancer or CDN sets
//     on editor responses (stickiness, bot management) come back when the
//     editor is reopened in the same tab; they prove nothing and are
//     ignored;
//   - the document only as a frame (Studio frames it same-origin, a
//     self-navigation from the opaque origin is cross-site);
//   - assets only cross-site, which is what an opaque origin sends, and never
//     as a document. A Studio document loading the foreign code into the
//     Studio origin sends same-origin and is refused.
func sameHostRequestAllowed(r *http.Request, index bool) bool {
	if carriesCrewshipCookie(r) {
		return false
	}
	dest := r.Header.Get("Sec-Fetch-Dest")
	if index {
		return dest == "iframe"
	}
	switch dest {
	case "", "document", "iframe", "frame", "embed", "object":
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") == "cross-site"
}

func (h *Handler) setHeaders(w http.ResponseWriter, csp string) {
	hdr := w.Header()
	hdr.Del("X-Frame-Options")
	hdr.Set("Content-Security-Policy", csp)
	// Fail closed for documents: one framed under Studio's COEP is
	// embeddable only through a credentialless iframe. Do not relax this on
	// index.html or error responses; only verified assets are cross-origin.
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	hdr.Set("Cross-Origin-Opener-Policy", "same-origin")
	hdr.Set("Origin-Agent-Cluster", "?1")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	if h.sameHost {
		hdr.Set("Strict-Transport-Security", StudioHSTS)
		hdr.Set(RouteHeader, "editor")
	} else {
		hdr.Set("Strict-Transport-Security", dedicatedHSTS)
	}
	hdr.Set("Permissions-Policy", permissionsPolicy)
	hdr.Set("Cache-Control", "no-store")
}

// policy is the editor policy for one response. In same-host mode it names
// the request's Studio origin; ok is false when the request has none.
func (h *Handler) policy(r *http.Request, inline string) (csp, origin string, err error) {
	if !h.sameHost {
		if h.all {
			return denyCSP, "", nil
		}
		return basePolicy(h.base, h.ancestor, inline), "", nil
	}
	origin, err = h.StudioOriginFor(r)
	if err != nil {
		return sameHostDenyCSP, "", err
	}
	// Sources carry the bundle path: the Studio origin itself is never a
	// source the editor may fetch from or load code from.
	return basePolicy(origin+SameHostPrefix+BundlePrefix, origin, inline), origin, nil
}

// ServeHTTP serves one verified bundle file. It never looks at credentials
// (same-host mode reads only cookie names); the bundle is public, constant
// code with no user data.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	csp, origin, originErr := h.policy(r, "")
	h.setHeaders(w, csp)
	if h.sameHost {
		w.Header().Set("Vary", sameHostVary)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch {
	case errors.Is(originErr, errBadHost):
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	case originErr != nil && h.sameHost && !h.broken:
		// The configured Studio origin, or the Page runtime host: the editor
		// is not served here. Fail closed rather than build a policy for a
		// host Studio does not run on.
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	p := r.URL.EscapedPath()
	inPrefix := true
	if h.sameHost {
		p, inPrefix = strings.CutPrefix(p, SameHostPrefix)
	}
	rest, ok := strings.CutPrefix(p, BundlePrefix)
	digest, name, ok2 := strings.Cut(rest, "/")
	b, known := h.bundles[digest]
	f, found := b.files[name]
	if !inPrefix || !ok || !ok2 || !known || !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if h.sameHost && h.check.disabledFor(origin) {
		http.Error(w, "the embedded editor is disabled: a proxy in front of Crewship redirects or rewrites "+SameHostPrefix+"/; an administrator can see the details", http.StatusServiceUnavailable)
		return
	}
	if h.sameHost && !sameHostRequestAllowed(r, f.index) {
		http.Error(w, "the editor loads only in a credentialless sandboxed frame", http.StatusForbidden)
		return
	}
	if f.index {
		csp, _, _ = h.policy(r, b.inline)
		w.Header().Set("Content-Security-Policy", csp)
	} else {
		// Same host: never in a shared cache. One that ignores Vary would
		// replay a frame's 200 to a Studio-context request refused above.
		cache := "public, max-age=31536000, immutable"
		if h.sameHost {
			cache = "private, max-age=31536000, immutable"
		}
		w.Header().Set("Cache-Control", cache)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(f.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(f.body)
	}
}

// Gate routes every request for the editor (its host, or in same-host mode
// its path prefix) to h and everything else to next. With a nil h it returns
// next unchanged. In same-host mode every other response is marked
// RouteHeader: app.
func Gate(h *Handler, next http.Handler) http.Handler {
	if h == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Matches(r) {
			h.ServeHTTP(w, r)
			return
		}
		if h.sameHost {
			w.Header().Set(RouteHeader, "app")
		}
		next.ServeHTTP(w, r)
	})
}
