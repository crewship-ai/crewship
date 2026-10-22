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
//     never in the app, and nothing under it redirects. Policy sources are
//     scoped to the bundle path, so the opaque-origin document cannot fetch
//     the Studio API or load Studio scripts, and Studio's frame-src names the
//     bundle path only, so a frame that navigates itself cannot reach a
//     Studio document. Both hold only while nothing in front of Crewship
//     redirects or normalises the prefix; CheckProxy verifies that through
//     the public URL and a failed check disables the editor. Requests a
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
// origins; BundleDir holds one directory per allowlisted digest. SameHost
// serves the editor under SameHostPrefix on StudioOrigin instead of a
// dedicated Origin, which must then be empty.
type Options struct {
	Origin       string
	SameHost     bool
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
	host     string
	all      bool // Deny: match every host, serve nothing.
	sameHost bool // Match SameHostPrefix on any host instead of a host name.
	base     string
	csp      string
	bundles  map[string]bundle
	check    proxyState
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
// under SameHostPrefix on the Studio origin. Studio must use HTTPS (plain HTTP
// only on loopback, where browsers still treat it as a secure context). The
// Page runtime must not share the Studio origin: Studio would then list its
// own whole origin in frame-src, and a self-navigated editor frame could land
// on a framable Studio document. The opaque origin "null" must not be a
// trusted API caller.
func ValidateSameHost(studio, pageRuntime string, allowedOrigins []string) error {
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

func basePolicy(self, ancestor, extraScript string) string {
	return "default-src 'none'; script-src " + self + " 'wasm-unsafe-eval'" + extraScript +
		"; style-src " + self + " 'unsafe-inline'; img-src " + self + " data: blob:; font-src " + self +
		"; connect-src " + self + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " +
		ancestor + "; webrtc 'block'; sandbox allow-scripts"
}

const denyCSP = "default-src 'none'; frame-ancestors 'none'; sandbox"

// Deny returns a handler that matches every host and serves nothing. It is
// the fail-closed answer when an editor origin is configured but unusable.
func Deny() *Handler {
	return &Handler{all: true, csp: denyCSP, bundles: map[string]bundle{}}
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
	ancestor := CanonicalOrigin(o.StudioOrigin)
	if ancestor == "" {
		ancestor = "'none'"
		errs = append(errs, errors.New("editor origin requires a Studio origin for frame-ancestors"))
	}
	var h *Handler
	var self string
	if o.SameHost {
		h = &Handler{sameHost: true, csp: denyCSP, bundles: map[string]bundle{}}
		if strings.TrimSpace(o.Origin) != "" {
			return h, errors.New("same-host editor must not also name a dedicated editor origin")
		}
		if ancestor == "'none'" {
			return h, errors.Join(errs...)
		}
		// Sources carry the bundle path: the Studio origin itself is never a
		// source the editor may fetch from or load code from.
		h.base = ancestor + SameHostPrefix
		self = h.base + BundlePrefix
	} else {
		u, err := parseOrigin(o.Origin)
		if err != nil {
			return nil, fmt.Errorf("editor origin %w", err)
		}
		self = u.Scheme + "://" + hostKey(u.Scheme, u.Host)
		h = &Handler{host: hostname(u.Host), base: self, bundles: map[string]bundle{}}
	}
	h.csp = basePolicy(self, ancestor, "")
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
	raw, err := readRegular(bundleRoot, "manifest.json", maxManifestBytes)
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
		body, err := readRegular(bundleRoot, name, maxBundleBytes-total)
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

// FrameSource is the source Studio adds to frame-src for this editor: the
// dedicated origin, or in same-host mode the bundle path on the Studio origin
// (never the bare Studio origin). It is "" when nothing may be framed.
func (h *Handler) FrameSource() string {
	if h == nil || h.base == "" {
		return ""
	}
	if h.sameHost {
		return h.base + BundlePrefix
	}
	return h.base
}

// IndexURL is the editor document of the bundle pinned by digest, for the
// host page's iframe src; "" when nothing may be framed.
func (h *Handler) IndexURL(digest string) string {
	if h.FrameSource() == "" {
		return ""
	}
	return h.base + BundlePrefix + digest + "/index.html"
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
	} else {
		hdr.Set("Strict-Transport-Security", dedicatedHSTS)
	}
	hdr.Set("Permissions-Policy", permissionsPolicy)
	hdr.Set("Cache-Control", "no-store")
}

// ServeHTTP serves one verified bundle file. It never looks at credentials
// (same-host mode reads only cookie names); the bundle is public, constant
// code with no user data.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setHeaders(w, h.csp)
	if h.sameHost {
		w.Header().Set("Vary", sameHostVary)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if reason := h.check.disabledReason(); reason != "" {
		http.Error(w, "the embedded editor is disabled: "+reason, http.StatusServiceUnavailable)
		return
	}
	if h.sameHost && !sameHostRequestAllowed(r, f.index) {
		http.Error(w, "the editor loads only in a credentialless sandboxed frame", http.StatusForbidden)
		return
	}
	if f.index {
		w.Header().Set("Content-Security-Policy", b.indexCSP)
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
// next unchanged.
func Gate(h *Handler, next http.Handler) http.Handler {
	if h == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Matches(r) {
			h.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
