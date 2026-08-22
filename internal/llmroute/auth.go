package llmroute

import (
	"net/http"
	"strings"
)

// ApplyAuth authenticates an outbound request: it writes every slot the
// winning AuthRule names, the spec's StaticHeaders, and extra — the custom
// headers a bring-your-own endpoint's credential supplies (nil for built-ins).
//
// Order is StaticHeaders, then extra, then the auth slots, so the token the
// sidecar injects is written LAST and a credential-supplied custom header can
// never shadow it. A custom header cannot shadow a StaticHeader either, even
// though it is written later — those keys are skipped explicitly (see below),
// because a spec's static headers are protocol and the operator does not get a
// vote on them. Everything is set (never added), because the agent env carries
// a dummy in most of these slots and a slot we merely left alone would reach
// the upstream still holding the dummy.
//
// An empty token suppresses the AUTH SLOTS only. Writing an empty Authorization
// header would swap one broken auth for another while destroying the diagnostic
// (the upstream's 401 names the key it saw). `extra` is written either way,
// because a bring-your-own endpoint may authenticate ENTIRELY through a custom
// header — `X-Api-Key: <secret>` — and gating those on a bearer token that such
// an endpoint never has meant the sidecar could not serve it. The credential was
// then left in the agent's own config instead, readable by the agent process,
// which is the exposure this package exists to close.
//
// This is not a byte-identity risk for the grandfathered providers: `extra` is
// non-nil only for a spec whose upstream comes from the credential, and no such
// spec existed before phase 2.
func ApplyAuth(r *http.Request, s Spec, token string, extra map[string]string) {
	// StaticHeaders are protocol, not credential: `anthropic-version` tells the
	// upstream which API shape the body is in, and it was set for ANY non-nil
	// credential before this refactor regardless of what the token contained.
	// Gating it on the token made an empty-token credential forward without it,
	// which turns a clean 401 ("your key is wrong") into a protocol error about
	// a missing version header — a worse diagnostic for the same failure.
	for k, v := range s.StaticHeaders {
		r.Header.Set(k, v)
	}

	// extra is operator-supplied credential data; StaticHeaders are protocol.
	// A credential whose custom headers happened to name `anthropic-version`
	// would otherwise rewrite the API shape the body was encoded for, from the
	// one surface an operator can type free-form text into.
	for k, v := range extra {
		if isStaticHeader(s, k) {
			continue
		}
		r.Header.Set(k, v)
	}

	if token == "" {
		return
	}

	for _, slot := range matchAuthRule(s, token).Slots {
		switch slot.Placement {
		case PlaceHeader:
			r.Header.Set(slot.Name, slot.Prefix+token)
		case PlaceQuery:
			// Query, sort and re-encode — byte-for-byte what the Gemini arm of
			// injectCredential did, including Encode()'s reordering of the
			// existing params, so the outbound query string is unchanged.
			q := r.URL.Query()
			q.Set(slot.Name, slot.Prefix+token)
			r.URL.RawQuery = q.Encode()
		}
	}
}

// isStaticHeader reports whether name is one of s's protocol headers. Spec keys
// are written lowercase and http.Header stores canonical form, so the
// comparison has to canonicalise both sides or every collision slips past.
func isStaticHeader(s Spec, name string) bool {
	want := http.CanonicalHeaderKey(name)
	for k := range s.StaticHeaders {
		if http.CanonicalHeaderKey(k) == want {
			return true
		}
	}
	return false
}

// matchAuthRule picks the first rule whose TokenPrefix the token carries,
// falling back to the default rule. Registration guarantees the default exists
// and is last, so this always returns a rule with at least one slot.
func matchAuthRule(s Spec, token string) AuthRule {
	for _, rule := range s.AuthRules {
		if rule.TokenPrefix == "" {
			return rule
		}
		if strings.HasPrefix(token, rule.TokenPrefix) {
			return rule
		}
	}
	// Unreachable: validateAuthRules refuses a spec with no default rule.
	return AuthRule{}
}
