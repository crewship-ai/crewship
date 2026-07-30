package api

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// routeMux is a thin recording wrapper around http.ServeMux.
//
// It exists for two reasons, both of which need the one thing a bare
// *http.ServeMux will not give you: the list of patterns that were
// actually registered.
//
//  1. Method guards (#1489). Go 1.22 routing resolves METHOD + path
//     together, so a request whose method is not registered for a LITERAL
//     path silently falls through to a sibling WILDCARD pattern instead of
//     answering 405. `GET /api/v1/notifications/count` is registered, and
//     `DELETE /api/v1/notifications/{id}` is registered, so
//     `DELETE /api/v1/notifications/count` reaches the delete-notification
//     handler with id="count" — the client gets 401/404/200 for a method
//     the endpoint does not support, and RFC 9110's Allow header never
//     appears. sealMethodGuards() closes that by registering an explicit
//     405 handler wherever a real route would otherwise capture a method
//     the path does not declare.
//
//  2. Spec drift (#1489). openapi.gen.json is produced by a regex scan of
//     router_*.go (cmd/gen-openapi). Routes() hands the *runtime* route
//     table to TestOpenAPISpecMatchesRegisteredRoutes so the scan can be
//     checked against what the mux really serves, in both directions.
//
// Registration is single-threaded (all of it happens inside NewRouter
// before the server starts accepting), so no locking is needed. Serving is
// pure delegation to the embedded ServeMux.
type routeMux struct {
	mux *http.ServeMux

	// methods maps a registered path pattern, exactly as written, to the
	// HTTP methods registered for it. A method-less registration (e.g. the
	// /exposed/{token...} reverse-proxy mount) records the empty string,
	// which marks the path as "handles every method itself". This is the
	// view Routes() exposes, because it is the one the spec is written
	// against.
	methods map[string]map[string]bool

	// shapeMethods is the same table keyed by wildcard-name-normalized
	// path. Two registrars may spell one pattern differently ({id} vs
	// {agentId}); ServeMux treats those as the same pattern, so guards
	// must be synthesized per shape, not per spelling.
	shapeMethods map[string]map[string]bool
	// shapeSpelling remembers the first concrete spelling seen for a
	// shape — the pattern string the guard is registered under.
	shapeSpelling map[string]string
	// shapeOrder preserves first-registration order so guard registration
	// is deterministic run to run.
	shapeOrder []string
	sealed     bool
}

func newRouteMux() *routeMux {
	return &routeMux{
		mux:           http.NewServeMux(),
		methods:       map[string]map[string]bool{},
		shapeMethods:  map[string]map[string]bool{},
		shapeSpelling: map[string]string{},
	}
}

// splitPattern splits a Go 1.22 ServeMux pattern into its method and path
// halves. "POST /api/v1/agents" -> ("POST", "/api/v1/agents");
// "/exposed/{token...}" -> ("", "/exposed/{token...}").
func splitPattern(pattern string) (method, path string) {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		return pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	return "", pattern
}

// normalizeShape rewrites wildcard names to a fixed placeholder so
// "/api/v1/agents/{id}" and "/api/v1/agents/{agentId}" collapse to the same
// key — which is what ServeMux itself does when deciding whether two
// patterns conflict.
func normalizeShape(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			b.WriteByte(path[i])
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			b.WriteString(path[i:])
			break
		}
		seg := path[i : i+end+1]
		if strings.HasSuffix(seg, "...}") {
			b.WriteString("{*...}")
		} else {
			b.WriteString("{*}")
		}
		i += end
	}
	return b.String()
}

func (m *routeMux) record(pattern string) {
	method, path := splitPattern(pattern)
	if m.methods[path] == nil {
		m.methods[path] = map[string]bool{}
	}
	m.methods[path][method] = true

	shape := normalizeShape(path)
	if m.shapeMethods[shape] == nil {
		m.shapeMethods[shape] = map[string]bool{}
		m.shapeSpelling[shape] = path
		m.shapeOrder = append(m.shapeOrder, shape)
	}
	m.shapeMethods[shape][method] = true
}

func (m *routeMux) Handle(pattern string, handler http.Handler) {
	m.record(pattern)
	m.mux.Handle(pattern, handler)
}

func (m *routeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.record(pattern)
	m.mux.HandleFunc(pattern, handler)
}

func (m *routeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

// Handler forwards http.ServeMux.Handler: it reports which pattern a request
// WOULD reach, without running the handler.
//
// Forwarded rather than reached through m.mux because two callers depend on
// it and they depend on the same subtlety. ServeMux returns an EMPTY pattern
// for both of its own answers — not-found and method-not-allowed — and a
// non-empty one only when a registered handler would actually receive the
// request. sealMethodGuards uses that to find the holes; the internal-surface
// fence (#1501, router_internal_fence.go) uses it to collapse every rejection
// under /api/v1/internal/ into one indistinguishable 404. Replacing
// Router.mux with this wrapper would otherwise take the method away from the
// fence and break the build.
func (m *routeMux) Handler(r *http.Request) (http.Handler, string) {
	return m.mux.Handler(r)
}

// Routes returns the registered route table as path -> sorted methods.
// A path registered without a method reports the single entry "*".
func (m *routeMux) Routes() map[string][]string {
	out := make(map[string][]string, len(m.methods))
	for path, set := range m.methods {
		methods := make([]string, 0, len(set))
		for meth := range set {
			if meth == "" {
				meth = "*"
			}
			methods = append(methods, meth)
		}
		sort.Strings(methods)
		out[path] = methods
	}
	return out
}

// guardedMethods are the methods a guard may be synthesized for. It is the
// set a client (or a spec-driven fuzzer) can plausibly send AND that some
// other route in this API is registered under — a TRACE or QUERY request
// matches no pattern at all, so ServeMux already answers those with its own
// 405 + Allow and needs no help.
var guardedMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// guardProbeSegment is substituted for wildcards when asking the mux which
// pattern a hypothetical request would reach. Any value works — it is never
// sent anywhere, only matched.
const guardProbeSegment = "openapi-method-guard-probe"

// sealMethodGuards closes Go 1.22's method-routing hole (#1489).
//
// ServeMux resolves method and path together. When a literal path P is
// registered for GET only, a DELETE to P does NOT produce 405 if some
// sibling wildcard — say "DELETE /prefix/{id}" — also matches P: the
// request lands in the sibling's handler with id="count". The client gets
// 401/404/200 for a method the endpoint does not support, and the Allow
// header RFC 9110 requires never appears.
//
// The fix is one explicit "METHOD P" registration per hole, which beats the
// wildcard sibling on path specificity for that exact method. Note a
// method-LESS guard at P cannot be used: against "DELETE /prefix/{id}" it
// wins on path but loses on method, and ServeMux rejects that ambiguity at
// registration time with a conflict panic.
//
// Holes are found, not assumed: mux.Handler reports which pattern a request
// would reach without running the handler, so a guard is only added where a
// real route would otherwise capture the request. All probing happens before
// any guard is registered, so the guards cannot influence each other.
//
// Must be called exactly once, after every registrar has run and before the
// mux serves traffic; the sealed flag makes a second call a no-op rather
// than a duplicate-pattern panic.
//
// Known residue: a verb no route anywhere uses (TRACE, QUERY, an invented
// one) still gets ServeMux's OWN 405, whose Allow is derived from every
// pattern matching that path — so it lists the guarded methods too, e.g.
// "Allow: DELETE, GET, HEAD" on a path where DELETE is guarded. That was
// equally true before the guards existed (the wildcard sibling contributed
// the same DELETE), and no client or spec-driven fuzzer sends those verbs,
// so it is left alone rather than paid for with a per-request lookup on the
// hot path. Closing it exactly means intercepting in ServeHTTP against a
// shape-resolving mux; see the follow-up note on #1489.
func (m *routeMux) sealMethodGuards() {
	if m.sealed {
		return
	}
	m.sealed = true

	type guard struct {
		pattern string
		allow   string
	}
	var guards []guard

	for _, shape := range m.shapeOrder {
		set := m.shapeMethods[shape]
		if set[""] {
			// Registered without a method — it already owns every method
			// on that path.
			continue
		}
		spelling := m.shapeSpelling[shape]
		probePath := pathParamSub.ReplaceAllString(spelling, guardProbeSegment)
		allow := allowHeaderValue(set)
		for _, method := range guardedMethods {
			if set[method] {
				continue
			}
			// ServeMux answers HEAD from a GET registration, so a GET
			// route needs no HEAD guard (and adding one would break HEAD).
			if method == http.MethodHead && set[http.MethodGet] {
				continue
			}
			req, err := http.NewRequest(method, probePath, nil)
			if err != nil {
				continue
			}
			_, captured := m.Handler(req)
			if captured == "" {
				continue // ServeMux already answers 405 here.
			}
			if capturedMethod, _ := splitPattern(captured); capturedMethod == "" {
				// A method-LESS mount (e.g. /exposed/{token...}) owns every
				// method under it on purpose. Guarding a narrower shape
				// inside it would 405 traffic the mount is there to accept.
				continue
			}
			guards = append(guards, guard{pattern: method + " " + spelling, allow: allow})
		}
	}

	for _, g := range guards {
		m.mux.Handle(g.pattern, methodNotAllowedHandler(g.allow))
	}
}

// pathParamSub matches a ServeMux wildcard segment ({id}, {token...}) so a
// pattern can be turned into a concrete probe path.
var pathParamSub = regexp.MustCompile(`\{[^{}]*\}`)

// allowHeaderValue builds the RFC 9110 Allow header value for a path.
// HEAD rides along with GET because ServeMux serves HEAD from a GET
// registration; OPTIONS is not listed because nothing in this API
// implements it.
func allowHeaderValue(set map[string]bool) string {
	methods := make([]string, 0, len(set)+1)
	for meth := range set {
		if meth == "" {
			continue
		}
		methods = append(methods, meth)
	}
	if set[http.MethodGet] && !set[http.MethodHead] {
		methods = append(methods, http.MethodHead)
	}
	sort.Strings(methods)
	return strings.Join(methods, ", ")
}

// methodNotAllowedHandler answers 405 with an Allow header and the same
// JSON error envelope every other API error uses, so a client parsing
// error bodies does not have to special-case this one.
func methodNotAllowedHandler(allow string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		replyError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
}
