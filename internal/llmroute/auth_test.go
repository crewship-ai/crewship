package llmroute

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// dummy is the placeholder every proxy-isolated agent carries in its provider
// env vars (exec_env.go writes GOOGLE_API_KEY=dummy-crewship-sidecar and
// friends). Preloading every slot with it is what turns "did we write the
// right slot?" into "did we overwrite every slot that would otherwise reach
// the upstream holding a dummy?".
const dummy = "dummy-crewship-sidecar"

// authSlotNames is the union of every slot any spec writes, so a case can
// assert that a provider left the OTHER providers' slots alone — the
// cross-contamination guard. Derived from the table, so a new provider's slot
// joins the guard automatically.
func authSlotNames(t *testing.T) (headers []string, queries []string) {
	t.Helper()
	seenH, seenQ := map[string]bool{}, map[string]bool{}
	for _, s := range Specs() {
		for k := range s.StaticHeaders {
			if !seenH[k] {
				seenH[k] = true
				headers = append(headers, k)
			}
		}
		for _, r := range s.AuthRules {
			for _, slot := range r.Slots {
				switch slot.Placement {
				case PlaceHeader:
					if !seenH[slot.Name] {
						seenH[slot.Name] = true
						headers = append(headers, slot.Name)
					}
				case PlaceQuery:
					if !seenQ[slot.Name] {
						seenQ[slot.Name] = true
						queries = append(queries, slot.Name)
					}
				}
			}
		}
	}
	if len(headers) == 0 {
		t.Fatal("no auth slots found in the table; the guard below would prove nothing")
	}
	return headers, queries
}

// newPreloadedRequest builds a request with every known slot already holding a
// dummy, the way one actually arrives from an agent CLI.
func newPreloadedRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	headers, queries := authSlotNames(t)
	r := httptest.NewRequest(http.MethodPost, path, nil)
	for _, h := range headers {
		r.Header.Set(h, dummy)
	}
	q := r.URL.Query()
	for _, k := range queries {
		q.Set(k, dummy)
	}
	r.URL.RawQuery = q.Encode()
	return r
}

func TestApplyAuth(t *testing.T) {
	cases := []struct {
		name        string
		specID      string
		token       string
		extra       map[string]string
		path        string
		wantHeaders map[string]string
		wantQuery   map[string]string
	}{
		{
			name: "anthropic api key goes in x-api-key", specID: "ANTHROPIC",
			token: "sk-ant-api03-real", path: "/v1/messages",
			wantHeaders: map[string]string{
				"x-api-key":         "sk-ant-api03-real",
				"anthropic-version": "2023-06-01",
			},
		},
		{
			// The token's own shape selects the rule — the branch that used to
			// be an Anthropic-specific if inside injectCredential.
			name: "anthropic oauth token goes in Authorization", specID: "ANTHROPIC",
			token: "sk-ant-oat01-real", path: "/v1/messages",
			wantHeaders: map[string]string{
				"Authorization":     "Bearer sk-ant-oat01-real",
				"anthropic-version": "2023-06-01",
			},
		},
		{
			name: "openai", specID: "OPENAI",
			token: "sk-proj-real", path: "/openai/v1/chat/completions",
			wantHeaders: map[string]string{"Authorization": "Bearer sk-proj-real"},
		},
		{
			// Both slots, because the SDK reads the header and other clients
			// read the query param; the one we skipped would carry the dummy.
			name: "google writes header and query", specID: "GOOGLE",
			token: "AIzaSyReal", path: "/gemini/v1beta/models",
			wantHeaders: map[string]string{"x-goog-api-key": "AIzaSyReal"},
			wantQuery:   map[string]string{"key": "AIzaSyReal"},
		},
		{
			name: "openrouter", specID: "OPENROUTER",
			token: "sk-or-v1-real", path: "/llm/openrouter/chat/completions",
			wantHeaders: map[string]string{"Authorization": "Bearer sk-or-v1-real"},
		},
		{
			name: "openai-compat carries the credential's custom headers", specID: "OPENAI_COMPAT",
			token: "sk-my_proxy-real", path: "/llm/openai-compat/chat/completions",
			extra: map[string]string{"X-Org": "acme"},
			wantHeaders: map[string]string{
				"Authorization": "Bearer sk-my_proxy-real",
				"X-Org":         "acme",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := Lookup(tc.specID)
			if !ok {
				t.Fatalf("Lookup(%q) found nothing", tc.specID)
			}
			r := newPreloadedRequest(t, tc.path)
			ApplyAuth(r, s, tc.token, tc.extra)

			for k, want := range tc.wantHeaders {
				if got := r.Header.Get(k); got != want {
					t.Errorf("header %s = %q, want %q", k, got, want)
				}
			}
			for k, want := range tc.wantQuery {
				if got := r.URL.Query().Get(k); got != want {
					t.Errorf("query %s = %q, want %q", k, got, want)
				}
			}

			// Cross-contamination: a slot the winning rule does not name must
			// still hold exactly what it held before — neither another
			// provider's slot nor the other AuthRule's. (In the OAuth case
			// that means x-api-key is left alone; exec_env.go:360 sets no
			// dummy ANTHROPIC_API_KEY on the OAuth path precisely because a
			// second Anthropic credential slot would fight the first.)
			headers, queries := authSlotNames(t)
			for _, h := range headers {
				if _, claimed := tc.wantHeaders[h]; claimed {
					continue
				}
				if got := r.Header.Get(h); got != dummy {
					t.Errorf("%s wrote header %s = %q; the winning AuthRule does not name it", tc.specID, h, got)
				}
			}
			for _, q := range queries {
				if _, claimed := tc.wantQuery[q]; claimed {
					continue
				}
				if got := r.URL.Query().Get(q); got != dummy {
					t.Errorf("%s wrote query %s = %q; the winning AuthRule does not name it", tc.specID, q, got)
				}
			}
		})
	}
}

// TestApplyAuth_EmptyTokenIsANoOp — a credential that arrived with no TOKEN in
// it must not have an empty Authorization header written on its behalf. The
// upstream's 401 then names the key it actually saw, which is the only
// diagnostic anyone gets from inside the sidecar.
//
// "No-op" covers the auth slots, not the whole call: StaticHeaders are protocol
// and `extra` is a credential's own custom headers, which may be the only
// authentication a bring-your-own endpoint has.
func TestApplyAuth_EmptyTokenIsANoOp(t *testing.T) {
	for _, s := range Specs() {
		t.Run(s.ID, func(t *testing.T) {
			r := newPreloadedRequest(t, s.PathPrefix+"/x")
			before := r.Header.Clone()
			beforeQuery := r.URL.RawQuery

			ApplyAuth(r, s, "", map[string]string{"X-Org": "acme"})

			// extra IS written on the empty-token path, and this assertion is
			// the reason the test is not vacuous: the loop below only walks
			// headers that already existed, so X-Org was never checked either
			// way and this test passed unchanged across a deliberate behaviour
			// change. A bring-your-own endpoint can authenticate entirely
			// through a custom header, and withholding those from the sidecar
			// is what left them in the agent's own config.
			if got := r.Header.Get("X-Org"); got != "acme" {
				t.Errorf("X-Org = %q, want acme — custom headers are not gated on a bearer token", got)
			}

			// StaticHeaders are the one exception, and deliberately so: they are
			// protocol, not credential. Pre-refactor injectCredential wrote
			// `anthropic-version` for ANY non-nil credential whatever the token
			// held, so exempting them here is what keeps this path byte-identical
			// — the earlier version of this test asserted the opposite and was
			// pinning a divergence the refactor introduced.
			for k, want := range s.StaticHeaders {
				if got := r.Header.Get(k); got != want {
					t.Errorf("static header %s = %q, want %q", k, got, want)
				}
			}
			// Spec keys are written lowercase; http.Header stores canonical
			// form, so the exemption set has to be canonicalised or every
			// comparison misses.
			static := make(map[string]bool, len(s.StaticHeaders))
			for k := range s.StaticHeaders {
				static[http.CanonicalHeaderKey(k)] = true
			}
			for k, want := range before {
				if static[http.CanonicalHeaderKey(k)] {
					continue
				}
				if got := r.Header[k]; len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
					t.Errorf("header %s = %v, want %v", k, got, want)
				}
			}
			if r.URL.RawQuery != beforeQuery {
				t.Errorf("RawQuery = %q, want %q", r.URL.RawQuery, beforeQuery)
			}
		})
	}
}

// TestApplyAuth_TokenWinsOverCustomHeaders pins the write order. The custom
// headers come from operator-supplied credential data; the token comes from
// the CredStore. If a custom "Authorization" could land last, a credential
// could route its own traffic authenticated as something else and the slot the
// sidecar believes it filled would be a lie.
func TestApplyAuth_TokenWinsOverCustomHeaders(t *testing.T) {
	s, ok := Lookup("OPENAI_COMPAT")
	if !ok {
		t.Fatal("OPENAI_COMPAT is not registered")
	}
	r := newPreloadedRequest(t, "/llm/openai-compat/chat/completions")
	ApplyAuth(r, s, "sk-real", map[string]string{
		"Authorization": "Bearer not-the-real-token",
		"X-Org":         "acme",
	})
	if got := r.Header.Get("Authorization"); got != "Bearer sk-real" {
		t.Errorf("Authorization = %q, want the CredStore token to win", got)
	}
	if got := r.Header.Get("X-Org"); got != "acme" {
		t.Errorf("X-Org = %q, want %q", got, "acme")
	}
}

// A spec's StaticHeaders are protocol; a credential's custom headers are
// operator-typed free text. extra is written after StaticHeaders (it has to be:
// it lives past the empty-token return), so without an explicit skip the one
// surface an operator can type anything into could rewrite the API shape the
// request body was encoded for.
func TestApplyAuth_CustomHeadersCannotShadowStaticHeaders(t *testing.T) {
	s, ok := Lookup("ANTHROPIC")
	if !ok {
		t.Fatal("ANTHROPIC spec missing — this guard cannot run")
	}
	want, ok := s.StaticHeaders["anthropic-version"]
	if !ok {
		t.Fatal("ANTHROPIC declares no anthropic-version; repoint this test at a spec with static headers")
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// Both casings, because http.Header canonicalises on Set and a check that
	// compared raw keys would miss the one an operator is most likely to type.
	ApplyAuth(r, s, "sk-ant-api03-real", map[string]string{
		"anthropic-version": "1999-01-01",
		"Anthropic-Version": "1999-01-02",
		"X-Org":             "acme",
	})

	if got := r.Header.Get("anthropic-version"); got != want {
		t.Errorf("anthropic-version = %q, want %q — a credential must not choose the wire protocol", got, want)
	}
	if got := r.Header.Get("X-Org"); got != "acme" {
		t.Errorf("X-Org = %q, want acme — non-protocol custom headers must still be forwarded", got)
	}
}

// TestApplyAuth_GoogleQueryEncoding pins the exact outbound query string,
// including Encode()'s sort — the Gemini arm of injectCredential built it the
// same way, and the sidecar's golden byte-identity tests compare RawQuery
// verbatim.
func TestApplyAuth_GoogleQueryEncoding(t *testing.T) {
	s, ok := Lookup("GOOGLE")
	if !ok {
		t.Fatal("GOOGLE is not registered")
	}
	r := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models?b=2&a=1&key="+dummy, nil)
	ApplyAuth(r, s, "AIzaSyReal", nil)

	const want = "a=1&b=2&key=AIzaSyReal"
	if r.URL.RawQuery != want {
		t.Errorf("RawQuery = %q, want %q", r.URL.RawQuery, want)
	}
}

// TestApplyAuth_StaticHeadersOnlyOnTheirOwnSpec — anthropic-version is
// Anthropic's, and sending it to an OpenAI-compatible upstream would be a new
// header on the wire that no previous version sent.
func TestApplyAuth_StaticHeadersOnlyOnTheirOwnSpec(t *testing.T) {
	for _, s := range Specs() {
		if s.ID == "ANTHROPIC" {
			continue
		}
		r := httptest.NewRequest(http.MethodPost, s.PathPrefix+"/x", nil)
		ApplyAuth(r, s, "sk-real", nil)
		if got := r.Header.Get("anthropic-version"); got != "" {
			t.Errorf("%s set anthropic-version = %q", s.ID, got)
		}
	}
}

// An empty token must not cost the request its protocol headers. Before the
// descriptor refactor, injectCredential set `anthropic-version` for any non-nil
// credential whatever the token held; gating StaticHeaders on a non-empty token
// silently dropped it, turning a clean 401 into a protocol complaint about a
// missing version header for the same underlying failure.
func TestApplyAuth_EmptyTokenStillSetsStaticHeaders(t *testing.T) {
	s, ok := Lookup("ANTHROPIC")
	if !ok {
		t.Fatal("ANTHROPIC spec missing — this guard cannot run")
	}
	if len(s.StaticHeaders) == 0 {
		t.Fatal("ANTHROPIC declares no static headers; repoint this test at a spec that does")
	}

	req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	ApplyAuth(req, s, "", nil)

	for k, want := range s.StaticHeaders {
		if got := req.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q — protocol headers are not credential material", k, got, want)
		}
	}
	if got := req.Header.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q — an empty token must not populate an auth slot", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q — an empty token must not populate the OAuth slot either", got)
	}
}
