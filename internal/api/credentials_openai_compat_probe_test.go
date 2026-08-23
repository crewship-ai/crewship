package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// compatValue builds the stored shape of an OPENAI_COMPAT credential: base URL,
// bearer token and custom headers as one object (see parseEndpointValue).
func compatValue(t *testing.T, baseURL, apiKey string, headers map[string]string) string {
	t.Helper()
	obj := map[string]any{"baseURL": baseURL}
	if apiKey != "" {
		obj["apiKey"] = apiKey
	}
	if len(headers) > 0 {
		obj["headers"] = headers
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal credential value: %v", err)
	}
	return string(b)
}

// recordingEndpoint stands in for an operator's gateway and remembers what
// arrived, so a test can assert on what we did NOT send as well as on the hit
// count.
type recordingEndpoint struct {
	mu      sync.Mutex
	hits    int
	headers []http.Header
	paths   []string
}

func (e *recordingEndpoint) snapshot() (int, []http.Header, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hits, append([]http.Header(nil), e.headers...), append([]string(nil), e.paths...)
}

func newRecordingEndpoint(t *testing.T) (*recordingEndpoint, string) {
	t.Helper()
	rec := &recordingEndpoint{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.hits++
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"llama-3"},{"id":"mixtral"}]}`))
	}))
	t.Cleanup(srv.Close)
	return rec, srv.URL
}

// An OPENAI_COMPAT credential is the one most worth testing — its endpoint is
// operator-supplied, so it can be wrong in every way a fixed vendor host cannot
// — and #2043 is that it was the only LLM provider that could not be tested at
// all. It fell to the default branch and answered "No validation available"
// wrapped in Valid:true, which reads as a green tick over a non-check.
func TestProbeProvider_OpenAICompatIsProbeable(t *testing.T) {
	t.Parallel()

	if !probeSupported("OPENAI_COMPAT", "API_KEY") {
		t.Error("OPENAI_COMPAT reports unsupported, so the wizard hides its Test button")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res := probeProviderInner(ctx, "OPENAI_COMPAT", "API_KEY", compatValue(t, "https://gw.example/v1", "sk-x", nil), false)
	if res.Error == probeNoValidationMsg {
		t.Error("OPENAI_COMPAT still falls through to the default branch")
	}
}

// The probe checks reachability, not authentication, and that is a deliberate
// line: the stored value carries the operator's apiKey and custom headers, and
// whoever can edit the credential can also point baseURL at a host they own.
// Sending the secret on demand would turn Test into an exfiltration primitive,
// so the probe asks the endpoint what models it serves and offers no credential
// at all.
func TestProbeProvider_OpenAICompatNeverSendsTheSecret(t *testing.T) {
	t.Parallel()

	rec, url := newRecordingEndpoint(t)
	const apiKey = "sk-compat-must-never-leave"
	value := compatValue(t, url+"/v1", apiKey, map[string]string{"X-Org-Secret": "org-token-must-never-leave"})

	res := probeProvider(context.Background(), "OPENAI_COMPAT", "API_KEY", value, true)
	if !res.Valid {
		t.Fatalf("reachable endpoint should probe valid, got error=%q", res.Error)
	}

	hits, headers, paths := rec.snapshot()
	if hits == 0 {
		t.Fatal("stored path never dialled the endpoint")
	}
	for i, h := range headers {
		if got := h.Get("Authorization"); got != "" {
			t.Errorf("request %d carried an Authorization header (%q); the probe must not offer the stored key", i, got)
		}
		if got := h.Get("X-Org-Secret"); got != "" {
			t.Errorf("request %d carried the stored custom header (%q); those are secrets too", i, got)
		}
		for name, vals := range h {
			if strings.Contains(strings.Join(vals, " "), apiKey) {
				t.Errorf("request %d leaked the apiKey in header %s", i, name)
			}
		}
	}
	if len(paths) > 0 && !strings.HasSuffix(paths[0], "/models") {
		t.Errorf("first probe path = %q, want the OpenAI-compatible /models listing", paths[0])
	}
}

// Same SSRF line the ENDPOINT_URL probe holds: the body path is RequireAuth-only
// with no workspace or role floor, so it must syntax-check and stop. Dialling a
// caller-supplied host from there is the vector dialEndpoint exists to contain.
func TestProbeProvider_OpenAICompatBodyPathDoesNotDial(t *testing.T) {
	t.Parallel()

	rec, url := newRecordingEndpoint(t)
	value := compatValue(t, url+"/v1", "sk-x", nil)

	res := probeProvider(context.Background(), "OPENAI_COMPAT", "API_KEY", value, false)
	if !res.Valid {
		t.Errorf("a well-formed endpoint should pass syntax validation on the body path, got %q", res.Error)
	}
	if hits, _, _ := rec.snapshot(); hits != 0 {
		t.Fatalf("body path dialled the endpoint %d time(s) — that is the SSRF dialEndpoint exists to prevent", hits)
	}

	if r := probeProvider(context.Background(), "OPENAI_COMPAT", "API_KEY", `{"baseURL":"not-a-url"}`, false); r.Valid {
		t.Error("a malformed baseURL should be rejected on the body path")
	}
	if hits, _, _ := rec.snapshot(); hits != 0 {
		t.Fatalf("body path dialled on a malformed value (%d hits)", hits)
	}
}

// A stored credential whose value never parsed has to report that, not a
// transport error against a host built from a broken URL.
func TestProbeProvider_OpenAICompatMalformedValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"bare token, no endpoint", "sk-just-a-token"},
		{"object without baseURL", `{"apiKey":"sk-x"}`},
		{"baseURL is not http", `{"baseURL":"ftp://gw.example/v1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := probeProvider(context.Background(), "OPENAI_COMPAT", "API_KEY", tc.value, true)
			if res.Valid {
				t.Errorf("value %q probed valid; a credential that cannot yield a base URL is not testable", tc.value)
			}
			if res.Error == probeNoValidationMsg {
				t.Error("reported 'no validation available' rather than naming the malformed value")
			}
		})
	}
}

// An endpoint that answers but serves no OpenAI-compatible model list is a real
// operator error (wrong path prefix, a gateway that only proxies /chat) and has
// to be reported as a failure — this is exactly the case the old default branch
// dressed up as a green tick.
func TestProbeProvider_OpenAICompatEndpointWithoutModelList(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	res := probeProvider(context.Background(), "OPENAI_COMPAT", "API_KEY", compatValue(t, srv.URL+"/v1", "sk-x", nil), true)
	if res.Valid {
		t.Errorf("an endpoint serving no model list probed valid: %+v", res)
	}
}
