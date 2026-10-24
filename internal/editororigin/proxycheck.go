package editororigin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Outcomes of CheckProxy and of the server's self-check of one origin.
const (
	ProxyCheckPending     = "pending"
	ProxyCheckPassed      = "passed"
	ProxyCheckFailed      = "failed"
	ProxyCheckUnreachable = "unreachable"
)

// Coarse editor states reported on the public /healthz.
const (
	HealthOK       = "ok"
	HealthPending  = "pending"
	HealthFailed   = "failed"
	HealthDisabled = "disabled"
)

// Causes of a failed check, short codes safe to show to administrators.
const (
	CauseRedirect      = "redirect"       // something in front redirects the prefix
	CausePathRewrite   = "path_rewrite"   // a path under the prefix reached Crewship outside the editor
	CauseNotRouted     = "not_routed"     // the prefix does not reach the editor (missing route, auth gate)
	CauseHostRewritten = "host_rewritten" // Crewship sees another Host than the browser uses
	CauseNoMarker      = "marker_missing" // responses lose RouteHeader; the check cannot tell who answered
)

// ReenableAfterPasses is how many consecutive passing checks bring back an
// editor that a confirmed isolation failure disabled.
const ReenableAfterPasses = 3

// maxLearnedOrigins bounds the origins learned from signed-in requests that
// the self-check probes; maxRewrittenReports bounds the origins only reported
// because the proxy rewrites Host (never probed). Each signed-in user teaches
// at most one of each.
const (
	maxLearnedOrigins   = 4
	maxRewrittenReports = 4
	// maxAdminChecks bounds how many users' administrator status is looked
	// up per learned origin.
	maxAdminChecks = 64
)

// LearnedOriginTTL is how long a learned origin is kept without a signed-in
// request from it. Expired origins free their slot; one seen again is
// learned anew and checked again.
var LearnedOriginTTL = 24 * time.Hour

// timeNow is the clock of learned origins; tests replace it.
var timeNow = time.Now

// ProxyCheck is the result of requesting /_editor/ variants through the
// public Studio URL.
type ProxyCheck struct {
	Status   string
	Problems []string
	Warnings []string
	// Isolation is set when a problem defeats the path-scoped isolation of
	// the same-host editor (a redirect, or a path routed out of the prefix
	// to Crewship), or when the check cannot tell (responses lose
	// RouteHeader), as opposed to the prefix merely not reaching the editor.
	Isolation bool
	// Cause is the first problem's cause code.
	Cause string
}

// OriginDetail is the self-check state of one Studio origin, for instance
// administrators (never on the public /healthz).
type OriginDetail struct {
	Origin string `json:"origin"`
	Source string `json:"source"` // "configured" or "learned"
	// LearnedFrom is the ID of the user whose signed-in request taught the
	// origin; AdminSession is set once an instance administrator's session
	// used it. Only the configured origin and origins an administrator used
	// count towards the public /healthz state.
	LearnedFrom       string    `json:"learned_from,omitempty"`
	AdminSession      bool      `json:"admin_session,omitempty"`
	ProxyCheck        string    `json:"proxy_check"`
	Disabled          bool      `json:"disabled"`
	Cause             string    `json:"cause,omitempty"`
	Problems          []string  `json:"problems,omitempty"`
	Warnings          []string  `json:"warnings,omitempty"`
	ConsecutivePasses int       `json:"consecutive_passes,omitempty"`
	CheckedAt         time.Time `json:"checked_at,omitzero"`
	LastSeen          time.Time `json:"last_seen,omitzero"`
}

// Detail is the editor state for instance administrators and doctor.
type Detail struct {
	Mode    string         `json:"mode"` // "same-host", "dedicated" or "off"
	Status  string         `json:"status,omitempty"`
	Origin  string         `json:"origin,omitempty"` // dedicated editor origin
	Origins []OriginDetail `json:"origins,omitempty"`
}

type originState struct {
	source      string
	learnedFrom string
	admin       bool
	adminChecks map[string]struct{}
	seen        time.Time
	status      string
	disabled    bool
	cause       string
	problems    []string
	warnings    []string
	passes      int
	checked     time.Time
}

// trusted reports whether the state counts towards the public health: the
// configured origin, or one an instance administrator's session used.
func (s *originState) trusted() bool { return s.source == "configured" || s.admin }

type proxyState struct {
	mu sync.Mutex
	// origins are the probe targets, keyed by exact origin.
	origins map[string]*originState
	// rewritten are browser origins whose Host the proxy rewrites; reported
	// only, never probed and never a probe slot.
	rewritten map[string]*originState
	wake      chan struct{}
}

func (p *proxyState) init() {
	p.origins = map[string]*originState{}
	p.rewritten = map[string]*originState{}
	p.wake = make(chan struct{}, 1)
}

// prune drops learned origins not seen within LearnedOriginTTL. Callers hold
// p.mu.
func (p *proxyState) prune(now time.Time) {
	for _, m := range []map[string]*originState{p.origins, p.rewritten} {
		for o, s := range m {
			if s.source == "learned" && now.Sub(s.seen) > LearnedOriginTTL {
				delete(m, o)
			}
		}
	}
}

// disabledFor reports whether the editor is disabled on origin. State is
// keyed by the exact origin the self-check probed, never by host alone, so
// a probe of another scheme or port cannot disable this one. The one
// exception is fail-closed: a request Crewship sees as http (TLS ended at a
// proxy it does not trust) is the https origin of the browser, so it also
// takes the state of https on the same host. Never the reverse.
func (p *proxyState) disabledFor(origin string) bool {
	if origin == "" || p.origins == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.origins[origin]; ok && s.disabled {
		return true
	}
	if rest, ok := strings.CutPrefix(origin, "http://"); ok {
		if s, ok := p.origins[CanonicalOrigin("https://"+rest)]; ok && s.disabled {
			return true
		}
	}
	return false
}

// Wake is signalled when a new origin is learned; the self-check then runs
// without waiting for its schedule. Nil for a handler without a self-check.
func (h *Handler) Wake() <-chan struct{} {
	if h == nil || !h.sameHost || h.check.wake == nil {
		return nil
	}
	return h.check.wake
}

// ProbeIPAllowed reports whether the self-check may send requests to ip:
// never to an unspecified, multicast, broadcast or link-local address
// (which covers the cloud metadata endpoints 169.254.169.254 and
// fd00:ec2::254), and to loopback only when allowLoopback is set (a server
// bound to loopback, or an operator-configured Studio origin).
func ProbeIPAllowed(ip net.IP, allowLoopback bool) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.Equal(net.IPv4bcast), ip.Equal(awsMetadataV6):
		return false
	case ip.IsLoopback():
		return allowLoopback
	}
	return true
}

var awsMetadataV6 = net.ParseIP("fd00:ec2::254")

// AllowsLoopbackProbes reports whether the self-check may reach loopback
// addresses: the server itself is bound to loopback (development), or the
// operator configured the Studio origin.
func (h *Handler) AllowsLoopbackProbes() bool {
	return h != nil && (h.loopback || h.pinned != "")
}

// learnableHost reports whether the self-check may learn a browser origin
// with scheme and host (a requestHost result). Plain http is learned only
// for a loopback host: elsewhere the host page refuses an http editor, so
// probing it shows nothing about the origin the editor runs on, while the
// usual port-80 redirect to https would read as an isolation failure. IP
// literals in a form a browser would not send (decimal, hex, short) and
// addresses ProbeIPAllowed refuses are never learned; loopback names and
// addresses only while the server is bound to loopback.
func (h *Handler) learnableHost(scheme, host string) bool {
	name := host
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSuffix(name, ".")
	loopbackName := name == "localhost" || strings.HasSuffix(name, ".localhost")
	labels := strings.Split(name, ".")
	last := labels[len(labels)-1]
	numeric := strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == ""
	loopback := loopbackName
	if numeric {
		ip := net.ParseIP(name)
		if ip == nil || ip.To4() == nil || len(labels) != 4 {
			return false
		}
		if !ProbeIPAllowed(ip, h.loopback) {
			return false
		}
		loopback = ip.IsLoopback()
	}
	if loopback && !h.loopback {
		return false
	}
	return scheme == "https" || loopback
}

// LearnOrigin records the Studio origin a signed-in browser request came
// from, so the self-check probes the public URL browsers actually use. The
// caller passes only requests that authenticated with a sign-in session,
// with the user's ID and a lookup of whether that user administers the
// instance (called only when it matters). This function further requires a
// browser same-origin request whose Origin or Referer names the request's
// own Host. These are plain headers outside a browser, so any signed-in
// user can make the server learn a host of their choosing. Hence:
//   - a configured Studio origin is never replaced, and nothing is learned
//     while one is configured;
//   - http is learned only for loopback, and metadata, link-local,
//     unspecified and multicast addresses never (learnableHost);
//   - each user teaches at most one origin; at most maxLearnedOrigins are
//     probed, and one not seen for LearnedOriginTTL expires. When all slots
//     are taken, only an instance administrator's origin is still learned,
//     replacing the least recently seen origin no administrator used, so
//     origins of other users can never push out the one administrators use;
//   - the state of an origin applies to that exact origin only, and only the
//     configured origin and origins an administrator used count towards
//     the public health.
//
// A browser origin whose host differs from the Host Crewship received is
// reported as failed (CauseHostRewritten) and never probed: the editor's
// policy follows the Host, so a proxy that rewrites it breaks the editor for
// that origin.
func (h *Handler) LearnOrigin(r *http.Request, user string, isAdmin func() bool) {
	if h == nil || !h.sameHost || h.broken || h.pinned != "" || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return
	}
	browser := r.Header.Get("Origin")
	if browser == "" || browser == "null" {
		ref, err := url.Parse(r.Header.Get("Referer"))
		if err != nil || ref.Host == "" {
			return
		}
		browser = ref.Scheme + "://" + ref.Host
	}
	browser = CanonicalOrigin(browser)
	if browser == "" {
		return
	}
	host, ok := requestHost(r.Host)
	if !ok {
		return
	}
	u, _ := url.Parse(browser)
	if _, ok := requestHost(u.Host); !ok || !h.learnableHost(u.Scheme, u.Host) {
		return
	}
	matches := hostKey(u.Scheme, host) == u.Host
	if matches && h.runtime != "" && browser == h.runtime {
		return
	}
	admin := func() bool { return isAdmin != nil && isAdmin() }
	if !matches {
		h.reportRewritten(browser, r.Host, user, admin)
		return
	}
	now := timeNow()
	h.check.mu.Lock()
	h.check.prune(now)
	if s, ok := h.check.origins[browser]; ok {
		delete(h.check.rewritten, browser)
		s.seen = now
		check := !s.admin && user != "" && len(s.adminChecks) < maxAdminChecks
		if check {
			if _, done := s.adminChecks[user]; done {
				check = false
			} else {
				s.adminChecks[user] = struct{}{}
			}
		}
		h.check.mu.Unlock()
		if check && admin() {
			h.check.mu.Lock()
			if s, ok := h.check.origins[browser]; ok {
				s.admin = true
			}
			h.check.mu.Unlock()
		}
		return
	}
	if user == "" || h.check.learnedBy(user) {
		h.check.mu.Unlock()
		return
	}
	h.check.mu.Unlock()
	// A new origin: look up the user's role once, outside the lock.
	isAdm := admin()
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	h.check.prune(now)
	if _, ok := h.check.origins[browser]; ok || h.check.learnedBy(user) {
		return
	}
	if len(h.check.origins) >= maxLearnedOrigins && (!isAdm || !evictUntrusted(h.check.origins)) {
		return
	}
	// The proxy forwards the Host after all: probe it instead.
	delete(h.check.rewritten, browser)
	h.check.origins[browser] = &originState{source: "learned", learnedFrom: user, admin: isAdm, seen: now, status: ProxyCheckPending,
		adminChecks: map[string]struct{}{user: {}}}
	h.signal()
}

// reportRewritten records a browser origin whose Host the proxy rewrites.
func (h *Handler) reportRewritten(browser, received, user string, admin func() bool) {
	now := timeNow()
	h.check.mu.Lock()
	h.check.prune(now)
	if s, ok := h.check.rewritten[browser]; ok {
		s.seen = now
		h.check.mu.Unlock()
		return
	}
	if user == "" || h.check.reportedBy(user) {
		h.check.mu.Unlock()
		return
	}
	h.check.mu.Unlock()
	isAdm := admin()
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	if _, ok := h.check.rewritten[browser]; ok || h.check.reportedBy(user) {
		return
	}
	if len(h.check.rewritten) >= maxRewrittenReports && (!isAdm || !evictUntrusted(h.check.rewritten)) {
		return
	}
	h.check.rewritten[browser] = &originState{source: "learned", learnedFrom: user, admin: isAdm, seen: now,
		status: ProxyCheckFailed, cause: CauseHostRewritten,
		problems: []string{fmt.Sprintf("browsers use %s but Crewship receives Host %q: the proxy must forward the original Host header (nginx: proxy_set_header Host $host; Apache: ProxyPreserveHost On)", browser, received)}}
}

// learnedBy reports whether user already taught a probed origin. Callers
// hold p.mu.
func (p *proxyState) learnedBy(user string) bool {
	for _, s := range p.origins {
		if s.source == "learned" && s.learnedFrom == user {
			return true
		}
	}
	return false
}

func (p *proxyState) reportedBy(user string) bool {
	for _, s := range p.rewritten {
		if s.learnedFrom == user {
			return true
		}
	}
	return false
}

// evictUntrusted removes the least recently seen learned origin that no
// instance administrator used, reporting whether one was removed.
func evictUntrusted(m map[string]*originState) bool {
	victim := ""
	for o, s := range m {
		if s.trusted() {
			continue
		}
		if victim == "" || s.seen.Before(m[victim].seen) {
			victim = o
		}
	}
	if victim == "" {
		return false
	}
	delete(m, victim)
	return true
}

// signal wakes the self-check. Callers hold h.check.mu.
func (h *Handler) signal() {
	select {
	case h.check.wake <- struct{}{}:
	default:
	}
}

// CheckTargets are the public Studio origins the self-check probes: the
// configured origin, or the origins learned from signed-in requests (none
// until a browser has used Studio).
func (h *Handler) CheckTargets() []string {
	if h == nil || !h.sameHost || h.broken {
		return nil
	}
	if h.pinned != "" {
		return []string{h.pinned}
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	h.check.prune(timeNow())
	out := make([]string, 0, len(h.check.origins))
	for o := range h.check.origins {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// RecordCheck records a check of origin. A failure that defeats the
// path-scoped isolation, or that cannot tell, disables the editor on that
// exact origin: the prefix stays with the editor handler, which serves no
// bundle file there. A disabled origin stays failed until
// ReenableAfterPasses consecutive checks pass; an unreachable check changes
// nothing.
func (h *Handler) RecordCheck(origin string, c ProxyCheck) {
	if h == nil || !h.sameHost || h.broken {
		return
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	s := h.check.origins[origin]
	if s == nil {
		if h.pinned == "" || origin != h.pinned {
			return
		}
		s = &originState{source: "configured", status: ProxyCheckPending}
		h.check.origins[origin] = s
	}
	switch c.Status {
	case ProxyCheckUnreachable:
		if s.status == ProxyCheckPending {
			s.status = ProxyCheckUnreachable
			s.warnings = c.Warnings
		}
		return
	case ProxyCheckFailed:
		s.status, s.cause, s.passes = ProxyCheckFailed, c.Cause, 0
		s.problems, s.warnings = c.Problems, c.Warnings
		if c.Isolation {
			s.disabled = true
		}
	case ProxyCheckPassed:
		s.warnings = c.Warnings
		if s.disabled {
			s.passes++
			if s.passes < ReenableAfterPasses {
				break
			}
			s.disabled = false
		}
		s.status, s.cause, s.problems, s.passes = ProxyCheckPassed, "", nil, 0
	}
	s.checked = time.Now()
}

// Health is the coarse editor state for the public /healthz: "" while the
// editor is off, HealthOK for a dedicated origin, and in same-host mode
// HealthDisabled when an origin is disabled, HealthFailed when a check
// failed without disabling it, HealthOK once a check passed and
// HealthPending until then. Only the configured origin and origins an
// instance administrator's session used count: any signed-in user can make
// the server learn an origin, and must not be able to flip the public
// state. The others are in Detail.
func (h *Handler) Health() string {
	if h == nil || h.all {
		return ""
	}
	if !h.sameHost {
		return HealthOK
	}
	if h.broken {
		return HealthDisabled
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	h.check.prune(timeNow())
	health := HealthPending
	for _, m := range []map[string]*originState{h.check.origins, h.check.rewritten} {
		for _, s := range m {
			if !s.trusted() {
				continue
			}
			switch {
			case s.disabled:
				return HealthDisabled
			case s.status == ProxyCheckFailed:
				health = HealthFailed
			case s.status == ProxyCheckPassed && health == HealthPending:
				health = HealthOK
			}
		}
	}
	return health
}

// Detail reports the editor state for instance administrators.
func (h *Handler) Detail() Detail {
	if h == nil || h.all {
		return Detail{Mode: "off"}
	}
	if !h.sameHost {
		return Detail{Mode: "dedicated", Status: HealthOK, Origin: h.base}
	}
	d := Detail{Mode: "same-host", Status: h.Health()}
	if h.broken {
		return d
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	h.check.prune(timeNow())
	if h.pinned != "" && h.check.origins[h.pinned] == nil {
		d.Origins = append(d.Origins, OriginDetail{Origin: h.pinned, Source: "configured", ProxyCheck: ProxyCheckPending})
	}
	for _, m := range []map[string]*originState{h.check.origins, h.check.rewritten} {
		for o, s := range m {
			d.Origins = append(d.Origins, OriginDetail{Origin: o, Source: s.source, LearnedFrom: s.learnedFrom, AdminSession: s.admin,
				ProxyCheck: s.status, Disabled: s.disabled, Cause: s.cause, Problems: s.problems, Warnings: s.warnings,
				ConsecutivePasses: s.passes, CheckedAt: s.checked, LastSeen: s.seen})
		}
	}
	sort.Slice(d.Origins, func(i, j int) bool { return d.Origins[i].Origin < d.Origins[j].Origin })
	return d
}

func originHost(origin string) string {
	_, host, _ := strings.Cut(origin, "://")
	if h, ok := strings.CutSuffix(host, ":443"); ok && strings.HasPrefix(origin, "https://") {
		return h
	}
	if h, ok := strings.CutSuffix(host, ":80"); ok && strings.HasPrefix(origin, "http://") {
		return h
	}
	return host
}

// ancestorMatches reports whether a frame-ancestors source list names
// origin, counting an http source as matching https (CSP3 scheme upgrade).
func ancestorMatches(policy, origin string) bool {
	for _, d := range strings.Split(policy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		if name != "frame-ancestors" {
			continue
		}
		for _, src := range strings.Fields(value) {
			if src == origin || strings.HasPrefix(origin, "https://") && src == "http://"+hostKey("http", originHost(origin)) {
				return true
			}
		}
	}
	return false
}

// CheckProxy requests /_editor/ variants at studioOrigin, the public URL a
// browser uses, without cookies and without following redirects. The
// same-host editor's isolation relies on path-scoped CSP sources and a
// path-scoped Studio frame-src; a browser drops the path of a source after a
// redirect, and a proxy that decodes and resolves the path can route a path
// under the prefix out of it. Only spellings a browser sends unchanged are
// probed: browsers resolve "..", "%2e%2e" and ".%2e" segments themselves, so
// a URL under the prefix can leave it only through an encoded slash or
// backslash (..%2f, ..%5c). Crewship marks every response with RouteHeader,
// so leaving the prefix is detected positively: a traversal answered by
// Crewship outside the editor. A refusal in front, whatever its headers,
// reached nothing. The check fails when
//   - any variant is redirected (SSO gate, trailing-slash rule): Isolation;
//   - a traversal is answered by Crewship outside the editor: Isolation;
//   - responses lose RouteHeader, so a traversal that reached Crewship
//     cannot be told from one refused in front: Isolation (fail closed);
//   - the prefix does not reach the editor at all (missing route, an
//     authentication gate answering 401), or the editor answers with a
//     policy for another host (the proxy rewrites Host).
//
// Status is ProxyCheckUnreachable when no variant got an answer.
func CheckProxy(ctx context.Context, client *http.Client, studioOrigin string) ProxyCheck {
	origin := CanonicalOrigin(studioOrigin)
	if origin == "" {
		return ProxyCheck{Status: ProxyCheckUnreachable, Warnings: []string{"invalid Studio origin"}}
	}
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	const (
		plain = iota
		index
		traversal
	)
	probes := []struct {
		path string
		kind int
	}{
		{SameHostPrefix + "/b/x", plain},
		{SameHostPrefix + "/b/x/", plain},
		{SameHostPrefix + "/b/" + strings.Repeat("0", 64) + "/index.html", index},
		{SameHostPrefix + "/b/..%2f..%2fhealthz", traversal},
		{SameHostPrefix + "/b/..%5c..%5chealthz", traversal},
	}
	var out ProxyCheck
	problem := func(cause string, isolation bool, format string, args ...any) {
		if out.Cause == "" {
			out.Cause = cause
		}
		out.Isolation = out.Isolation || isolation
		out.Problems = append(out.Problems, fmt.Sprintf(format, args...))
	}
	answered := 0
	var firstErr error
	cookieWarned, hostWarned := false, false
	for _, p := range probes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/", nil)
		if err != nil {
			return ProxyCheck{Status: ProxyCheckUnreachable, Warnings: []string{err.Error()}}
		}
		// Sent verbatim: the point is what the proxy does with this spelling.
		req.URL.Opaque = p.path
		req.Header.Set("Cache-Control", "no-cache")
		resp, err := c.Do(req)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		answered++
		route := resp.Header.Get(RouteHeader)
		csp := resp.Header.Get("Content-Security-Policy")
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			problem(CauseRedirect, true, "%s is redirected (HTTP %d to %q)", p.path, resp.StatusCode, resp.Header.Get("Location"))
		case route == "editor":
			if !cookieWarned && len(resp.Header.Values("Set-Cookie")) > 0 {
				cookieWarned = true
				out.Warnings = append(out.Warnings, "the proxy sets cookies on "+SameHostPrefix+"/ responses; harmless, but not needed")
			}
			if p.kind != traversal && !hostWarned && !ancestorMatches(csp, origin) {
				hostWarned = true
				problem(CauseHostRewritten, false, "the editor at %s answers for another host (HTTP %d): the proxy must forward the original Host header (nginx: proxy_set_header Host $host; Apache: ProxyPreserveHost On), or CREWSHIP_PAGE_STUDIO_ORIGIN names another origin", origin, resp.StatusCode)
			}
		case route != "":
			problem(CausePathRewrite, true, "%s was answered by Crewship outside the editor (HTTP %d): the proxy decodes or normalises the path", p.path, resp.StatusCode)
		case p.kind == traversal:
			// Refused or answered in front of Crewship: it reached nothing.
		case strings.HasSuffix(csp, "; sandbox allow-scripts") && strings.HasPrefix(csp, "default-src 'none'"):
			// Fail closed: without the marker a traversal that reached
			// Crewship outside the editor reads as refused in front.
			problem(CauseNoMarker, true, "%s reaches the editor but the %s response header is removed in front; the check cannot tell where other paths end up: pass the header through", p.path, RouteHeader)
		default:
			problem(CauseNotRouted, false, "%s does not reach the Crewship editor (HTTP %d): missing route or authentication gate", p.path, resp.StatusCode)
		}
	}
	switch {
	case answered == 0:
		out = ProxyCheck{Status: ProxyCheckUnreachable}
		if firstErr != nil {
			out.Warnings = []string{firstErr.Error()}
		}
	case len(out.Problems) > 0:
		out.Status = ProxyCheckFailed
	default:
		out.Status = ProxyCheckPassed
		if answered < len(probes) && firstErr != nil {
			out.Warnings = append(out.Warnings, "some variants got no answer: "+firstErr.Error())
		}
	}
	return out
}
