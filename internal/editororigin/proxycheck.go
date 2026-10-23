package editororigin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Outcomes of CheckProxy and of the server's self-check.
const (
	ProxyCheckPending     = "pending"
	ProxyCheckPassed      = "passed"
	ProxyCheckFailed      = "failed"
	ProxyCheckUnreachable = "unreachable"
)

// ProxyCheck is the result of requesting /_editor/ variants through the
// public Studio URL.
type ProxyCheck struct {
	Status   string
	Problems []string
	Warnings []string
	// Isolation is set when a problem defeats the path-scoped isolation of
	// the same-host editor (a redirect, or a path routed out of the prefix),
	// as opposed to the prefix merely not reaching the editor.
	Isolation bool
}

// Status is the editor state reported on /healthz.
type Status struct {
	Mode       string `json:"mode"` // "same-host" or "dedicated"
	URL        string `json:"url"`  // bundle path (same host) or editor origin
	ProxyCheck string `json:"proxy_check,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
}

type proxyState struct {
	mu       sync.Mutex
	status   string
	detail   string
	disabled string
}

func (p *proxyState) disabledReason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disabled
}

// Status reports the mode and, in same-host mode, the proxy check. A nil or
// denying handler reports nothing.
func (h *Handler) Status() Status {
	if h == nil || h.all {
		return Status{}
	}
	if !h.sameHost {
		return Status{Mode: "dedicated", URL: h.base}
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	s := Status{Mode: "same-host", URL: h.FrameSource(), ProxyCheck: h.check.status, Detail: h.check.detail, Disabled: h.check.disabled != ""}
	if s.ProxyCheck == "" {
		s.ProxyCheck = ProxyCheckPending
	}
	if s.Disabled {
		s.Detail = h.check.disabled
	}
	return s
}

// SetProxyCheck records a proxy check. A failure that defeats the
// path-scoped isolation disables the same-host editor until restart: the
// prefix stays with the editor handler, which then serves no bundle file.
func (h *Handler) SetProxyCheck(c ProxyCheck) {
	if h == nil || !h.sameHost {
		return
	}
	h.check.mu.Lock()
	defer h.check.mu.Unlock()
	h.check.status = c.Status
	h.check.detail = strings.Join(append(append([]string{}, c.Problems...), c.Warnings...), "; ")
	if c.Status == ProxyCheckFailed && c.Isolation && h.check.disabled == "" {
		h.check.disabled = "a proxy in front of Crewship redirects or rewrites " + SameHostPrefix + "/ (" + strings.Join(c.Problems, "; ") +
			"); pass " + SameHostPrefix + "/ to Crewship unchanged, without redirects or an authentication gate, then restart"
	}
}

// isEditorResponse reports whether a response carries the editor handler's
// sandboxed policy, i.e. the request reached Crewship's editor handler.
func isEditorResponse(resp *http.Response) bool {
	csp := resp.Header.Get("Content-Security-Policy")
	return strings.HasPrefix(csp, "default-src 'none'") && (strings.HasSuffix(csp, "; sandbox allow-scripts") || strings.HasSuffix(csp, "; sandbox"))
}

// rejectedInFront reports a refusal that did not come from Studio: the
// request stopped in front of Crewship and reached nothing.
func rejectedInFront(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}
	return resp.Header.Get("X-Frame-Options") == "" && !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors")
}

// CheckProxy requests /_editor/ variants at studioOrigin, the public URL a
// browser uses, without cookies and without following redirects. The
// same-host editor's isolation relies on path-scoped CSP sources and a
// path-scoped Studio frame-src; a browser drops the path of a source after a
// redirect, and a proxy that decodes and resolves dot segments can route a
// path under the prefix out of it. So the check fails when
//   - any variant is redirected (SSO gate, trailing-slash rule): Isolation;
//   - an encoded traversal out of the prefix is answered by anything other
//     than the editor handler or a plain refusal in front: Isolation;
//   - the prefix does not reach the editor handler at all (missing route,
//     authentication gate answering 401): the editor cannot load.
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
	probes := []struct {
		path      string
		traversal bool
	}{
		{SameHostPrefix + "/b/x", false},
		{SameHostPrefix + "/b/x/", false},
		{SameHostPrefix + "/b/" + strings.Repeat("0", 64) + "/index.html", false},
		{SameHostPrefix + "/b/..%2f..%2fapi/v1/auth/session", true},
		{SameHostPrefix + "/b/%2e%2e/%2e%2e/api/v1/auth/session", true},
		{SameHostPrefix + "/b/..%5c..%5capi/v1/auth/session", true},
	}
	var out ProxyCheck
	answered := 0
	var firstErr error
	cookieWarned := false
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
		editor := isEditorResponse(resp)
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			out.Isolation = true
			out.Problems = append(out.Problems, fmt.Sprintf("%s is redirected (HTTP %d to %q)", p.path, resp.StatusCode, resp.Header.Get("Location")))
		case editor:
			if !cookieWarned && len(resp.Header.Values("Set-Cookie")) > 0 {
				cookieWarned = true
				out.Warnings = append(out.Warnings, "the proxy sets cookies on "+SameHostPrefix+"/ responses; harmless, but not needed")
			}
		case p.traversal && rejectedInFront(resp):
		case p.traversal:
			out.Isolation = true
			out.Problems = append(out.Problems, fmt.Sprintf("%s left the editor prefix (HTTP %d from outside the editor): the proxy decodes or normalises the path", p.path, resp.StatusCode))
		default:
			out.Problems = append(out.Problems, fmt.Sprintf("%s does not reach the Crewship editor (HTTP %d without the editor policy): missing route or authentication gate", p.path, resp.StatusCode))
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
