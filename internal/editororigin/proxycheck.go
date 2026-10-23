package editororigin

import (
	"context"
	"fmt"
	"io"
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

// maxLearnedOrigins bounds the origins learned from signed-in requests.
const maxLearnedOrigins = 4

// ProxyCheck is the result of requesting /_editor/ variants through the
// public Studio URL.
type ProxyCheck struct {
	Status   string
	Problems []string
	Warnings []string
	// Isolation is set when a problem defeats the path-scoped isolation of
	// the same-host editor (a redirect, or a path routed out of the prefix
	// to Crewship), as opposed to the prefix merely not reaching the editor.
	Isolation bool
	// Cause is the first problem's cause code.
	Cause string
}

// OriginDetail is the self-check state of one Studio origin, for instance
// administrators (never on the public /healthz).
type OriginDetail struct {
	Origin            string    `json:"origin"`
	Source            string    `json:"source"` // "configured" or "learned"
	ProxyCheck        string    `json:"proxy_check"`
	Disabled          bool      `json:"disabled"`
	Cause             string    `json:"cause,omitempty"`
	Problems          []string  `json:"problems,omitempty"`
	Warnings          []string  `json:"warnings,omitempty"`
	ConsecutivePasses int       `json:"consecutive_passes,omitempty"`
	CheckedAt         time.Time `json:"checked_at,omitzero"`
}

// Detail is the editor state for instance administrators and doctor.
type Detail struct {
	Mode    string         `json:"mode"` // "same-host", "dedicated" or "off"
	Status  string         `json:"status,omitempty"`
	Origin  string         `json:"origin,omitempty"` // dedicated editor origin
	Origins []OriginDetail `json:"origins,omitempty"`
}

type originState struct {
	source   string
	status   string
	disabled bool
	cause    string
	problems []string
	warnings []string
	passes   int
	checked  time.Time
}

type proxyState struct {
	mu      sync.Mutex
	origins map[string]*originState
	wake    chan struct{}
}

func (p *proxyState) init() {
	p.origins = map[string]*originState{}
	p.wake = make(chan struct{}, 1)
}

// stateFor returns the state of origin, matching by host so that the http
// and https spellings of one host share it. Callers hold p.mu.
func (p *proxyState) stateFor(origin string) *originState {
	if s, ok := p.origins[origin]; ok {
		return s
	}
	host := originHost(origin)
	for o, s := range p.origins {
		if originHost(o) == host {
			return s
		}
	}
	return nil
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

func (p *proxyState) disabledFor(origin string) bool {
	if origin == "" || p.origins == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stateFor(origin)
	return s != nil && s.disabled
}

// Wake is signalled when a new origin is learned; the self-check then runs
// without waiting for its schedule. Nil for a handler without a self-check.
func (h *Handler) Wake() <-chan struct{} {
	if h == nil || !h.sameHost || h.check.wake == nil {
		return nil
	}
	return h.check.wake
}

// LearnOrigin records the Studio origin a signed-in browser request came
// from, so the self-check probes the public URL browsers actually use. The
// caller passes only requests that authenticated with a session; this
// function further requires a browser same-origin request whose Origin or
// Referer names the request's own Host. A configured Studio origin is never
// replaced. At most maxLearnedOrigins are kept; requests on other origins
// are still served, just not probed.
//
// A browser origin whose host differs from the Host Crewship received is
// recorded as failed (CauseHostRewritten): the editor's policy follows the
// Host, so a proxy that rewrites it breaks the editor for that origin.
func (h *Handler) LearnOrigin(r *http.Request) {
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
	if _, ok := requestHost(u.Host); !ok {
		return
	}
	matches := hostKey(u.Scheme, host) == u.Host
	if matches && h.runtime != "" && CanonicalOrigin(browser) == h.runtime {
		return
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	if s, ok := h.check.origins[browser]; ok {
		if matches && s.cause == CauseHostRewritten {
			// The proxy forwards the Host after all: probe it.
			*s = originState{source: "learned", status: ProxyCheckPending}
			h.signal()
		}
		return
	}
	if len(h.check.origins) >= maxLearnedOrigins {
		return
	}
	s := &originState{source: "learned", status: ProxyCheckPending}
	if !matches {
		s.status, s.cause = ProxyCheckFailed, CauseHostRewritten
		s.problems = []string{fmt.Sprintf("browsers use %s but Crewship receives Host %q: the proxy must forward the original Host header (nginx: proxy_set_header Host $host; Apache: ProxyPreserveHost On)", browser, r.Host)}
	}
	h.check.origins[browser] = s
	h.signal()
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
	var out []string
	for o, s := range h.check.origins {
		if s.cause != CauseHostRewritten {
			out = append(out, o)
		}
	}
	sort.Strings(out)
	return out
}

// originDisabled reports whether the proxy self-check disabled the editor on
// origin.
func (h *Handler) originDisabled(origin string) bool {
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	s := h.check.origins[origin]
	return s != nil && s.disabled
}

// RecordCheck records a check of origin. A failure that defeats the
// path-scoped isolation disables the editor on that origin: the prefix stays
// with the editor handler, which serves no bundle file there. A disabled
// origin stays failed until ReenableAfterPasses consecutive checks pass; an
// unreachable check changes nothing.
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
// HealthDisabled when any origin is disabled, HealthFailed when a check
// failed without disabling it, HealthOK once a check passed and
// HealthPending until then.
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
	health := HealthPending
	for _, s := range h.check.origins {
		switch {
		case s.disabled:
			return HealthDisabled
		case s.status == ProxyCheckFailed:
			health = HealthFailed
		case s.status == ProxyCheckPassed && health == HealthPending:
			health = HealthOK
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
	if h.pinned != "" && h.check.origins[h.pinned] == nil {
		d.Origins = append(d.Origins, OriginDetail{Origin: h.pinned, Source: "configured", ProxyCheck: ProxyCheckPending})
	}
	for o, s := range h.check.origins {
		d.Origins = append(d.Origins, OriginDetail{Origin: o, Source: s.source, ProxyCheck: s.status, Disabled: s.disabled, Cause: s.cause,
			Problems: s.problems, Warnings: s.warnings, ConsecutivePasses: s.passes, CheckedAt: s.checked})
	}
	sort.Slice(d.Origins, func(i, j int) bool { return d.Origins[i].Origin < d.Origins[j].Origin })
	return d
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
//   - the prefix does not reach the editor at all (missing route, an
//     authentication gate answering 401), or the editor answers with a
//     policy for another host (the proxy rewrites Host), or responses lose
//     RouteHeader so nothing can be told apart.
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
			problem(CauseNoMarker, false, "%s reaches the editor but the %s response header is removed in front; the check cannot tell where other paths end up", p.path, RouteHeader)
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
