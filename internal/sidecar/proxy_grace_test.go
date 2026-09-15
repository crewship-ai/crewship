package sidecar

// Rotation grace fallback on the proxy path (#1882).
//
// A credential rotation keeps the previous value for a grace window. A sidecar
// that booted AFTER the rotation carries the new value as its token and the
// previous one as its grace value; when the upstream answers 401 to the new
// value, the proxy replays the request ONCE with the grace value. Nothing else
// triggers it: not a 403, not a 500, not a second 401, not another
// credential's rotation, not a rotation that has expired or been cancelled.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// graceUpstream scripts the upstream: one status per call, in order, and
// records what each attempt carried.
type graceUpstream struct {
	mu       sync.Mutex
	statuses []int
	calls    []graceAttempt
}

type graceAttempt struct {
	auth  string
	query string
	body  string
	host  string
	path  string
}

func (u *graceUpstream) roundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	body := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	auth := r.Header.Get("x-api-key")
	if auth == "" {
		auth = r.Header.Get("Authorization")
	}
	u.calls = append(u.calls, graceAttempt{auth: auth, query: r.URL.RawQuery, body: body, host: r.URL.Host, path: r.URL.Path})
	status := http.StatusInternalServerError
	if i := len(u.calls) - 1; i < len(u.statuses) {
		status = u.statuses[i]
	}
	return jsonUpstreamResponse(status, "application/json", fmt.Sprintf(`{"status":%d}`, status), nil), nil
}

func newGraceProxy(t *testing.T, creds []Credential, statuses []int, observe GraceFallbackObserver, egress EgressObserver) (*Proxy, *CredStore, *graceUpstream) {
	t.Helper()
	cs := NewCredStore()
	cs.Load(creds)
	up := &graceUpstream{statuses: statuses}
	p := NewProxy(ProxyConfig{
		CredStore:       cs,
		Allowlist:       NewDomainAllowlist(nil),
		Logger:          covLogger(),
		FreeMode:        true,
		OnGraceFallback: observe,
		OnEgress:        egress,
	})
	p.transport = roundTripperFunc(up.roundTrip)
	return p, cs, up
}

func anthropicCred(id, token, grace string, graceUntil time.Time) Credential {
	c := Credential{ID: id, Provider: ProviderAnthropic, Token: token, Priority: 0}
	if grace != "" {
		c.GraceToken = grace
		c.GraceExpiresAt = rfc3339(graceUntil)
		c.GraceRotationID = "rot-" + id
	}
	return c
}

func TestProxy_GraceFallback(t *testing.T) {
	const reqBody = `{"model":"claude","messages":[{"role":"user","content":"hi"}]}`
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Minute)

	tests := []struct {
		name      string
		creds     []Credential
		scrub     map[string]struct{} // nil = no scrub; non-nil = ScrubGrace(keep) before the request
		statuses  []int
		wantCode  int
		wantCalls []string // auth value each upstream attempt must carry
		wantEvent *GraceFallback
		// wantEgress is the status of each network.egress the REGULAR observer
		// sees. A replayed response is reported by the fallback observer
		// instead, so it never appears here — one journal entry per attempt.
		wantEgress []int
	}{
		{
			name:       "401 with an active rotation retries once with the grace value and succeeds",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", future)},
			statuses:   []int{401, 200},
			wantCode:   200,
			wantCalls:  []string{"sk-new", "sk-old"},
			wantEvent:  &GraceFallback{CredentialID: "c1", RotationID: "rot-c1", Provider: "ANTHROPIC", Host: "api.anthropic.com", Status: 200},
			wantEgress: []int{401},
		},
		{
			name:       "a second 401 surfaces: exactly one retry",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", future)},
			statuses:   []int{401, 401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-new", "sk-old"},
			wantEvent:  &GraceFallback{CredentialID: "c1", RotationID: "rot-c1", Provider: "ANTHROPIC", Host: "api.anthropic.com", Status: 401},
			wantEgress: []int{401},
		},
		{
			name:       "expired rotation: the 401 surfaces, no retry",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", past)},
			statuses:   []int{401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{401},
		},
		{
			name:       "cancelled rotation (scrubbed by the reaper): no retry",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", future)},
			scrub:      map[string]struct{}{},
			statuses:   []int{401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{401},
		},
		{
			name: "another credential's rotation is never used",
			creds: []Credential{
				anthropicCred("c1", "sk-new", "", time.Time{}),
				{ID: "c2", Provider: ProviderOpenAI, Token: "sk-oa", GraceToken: "sk-oa-old", GraceExpiresAt: rfc3339(future), GraceRotationID: "rot-c2"},
			},
			statuses:   []int{401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{401},
		},
		{
			name:       "403 is not an authentication failure eligible for retry",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", future)},
			statuses:   []int{403, 200},
			wantCode:   403,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{403},
		},
		{
			name:       "500 is not retried",
			creds:      []Credential{anthropicCred("c1", "sk-new", "sk-old", future)},
			statuses:   []int{500, 200},
			wantCode:   500,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{500},
		},
		{
			name:       "a grace value identical to the current one is not re-sent",
			creds:      []Credential{anthropicCred("c1", "sk-same", "sk-same", future)},
			statuses:   []int{401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-same"},
			wantEgress: []int{401},
		},
		{
			name:       "no rotation at all: the 401 surfaces untouched",
			creds:      []Credential{anthropicCred("c1", "sk-new", "", time.Time{})},
			statuses:   []int{401, 200},
			wantCode:   401,
			wantCalls:  []string{"sk-new"},
			wantEgress: []int{401},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu     sync.Mutex
				events []GraceFallback
				egress []int
			)
			p, cs, up := newGraceProxy(t, tc.creds, tc.statuses, func(e GraceFallback) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, e)
			}, func(host, method, provider string, status int, denied bool) {
				mu.Lock()
				defer mu.Unlock()
				egress = append(egress, status)
			})
			if tc.scrub != nil {
				cs.ScrubGrace(tc.scrub)
			}

			req := httptest.NewRequest("POST", "http://127.0.0.1:9119/v1/messages", strings.NewReader(reqBody))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			req.Header.Set("x-api-key", "sk-dummy-crewship-sidecar")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body.String())
			}
			if len(up.calls) != len(tc.wantCalls) {
				t.Fatalf("upstream called %d times, want %d: %+v", len(up.calls), len(tc.wantCalls), up.calls)
			}
			for i, want := range tc.wantCalls {
				if up.calls[i].auth != want {
					t.Errorf("attempt %d carried %q, want %q", i+1, up.calls[i].auth, want)
				}
				// The replay is byte-identical: the agent's body is sent again,
				// not an empty or partially consumed one.
				if up.calls[i].body != reqBody {
					t.Errorf("attempt %d body = %q, want the original request body", i+1, up.calls[i].body)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if fmt.Sprint(egress) != fmt.Sprint(tc.wantEgress) {
				t.Errorf("regular egress statuses = %v, want %v (a replay is reported by the fallback observer, not twice)", egress, tc.wantEgress)
			}
			if tc.wantEvent == nil {
				if len(events) != 0 {
					t.Fatalf("grace fallback observed without a retry: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("observed %d grace fallbacks, want 1", len(events))
			}
			got := events[0]
			if got.CredentialID != tc.wantEvent.CredentialID || got.RotationID != tc.wantEvent.RotationID ||
				got.Provider != tc.wantEvent.Provider || got.Host != tc.wantEvent.Host || got.Status != tc.wantEvent.Status {
				t.Errorf("event = %+v, want %+v", got, *tc.wantEvent)
			}
		})
	}
}

// The forward-proxy path (HTTP_PROXY, host-matched) injects credentials too and
// must retry the same way; and a query-placed token (Gemini) must be REPLACED
// on the replay, not doubled.
func TestProxy_GraceFallback_ForwardPathAndQueryToken(t *testing.T) {
	future := time.Now().Add(time.Hour)
	p, _, up := newGraceProxy(t, []Credential{{
		ID: "g1", Provider: ProviderGoogle, Token: "AIza-new",
		GraceToken: "AIza-old", GraceExpiresAt: rfc3339(future), GraceRotationID: "rot-g1",
	}}, []int{401, 200}, nil, nil)

	req := httptest.NewRequest("POST",
		"http://generativelanguage.googleapis.com/v1beta/models/gemini:generateContent?alt=sse",
		strings.NewReader(`{"contents":[]}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if len(up.calls) != 2 {
		t.Fatalf("upstream called %d times, want 2", len(up.calls))
	}
	if !strings.Contains(up.calls[0].query, "key=AIza-new") {
		t.Errorf("first attempt query = %q, want the current key", up.calls[0].query)
	}
	if !strings.Contains(up.calls[1].query, "key=AIza-old") || strings.Contains(up.calls[1].query, "AIza-new") {
		t.Errorf("replay query = %q, want ONLY the grace key", up.calls[1].query)
	}
	if !strings.Contains(up.calls[1].query, "alt=sse") {
		t.Errorf("replay query = %q, lost the agent's own parameters", up.calls[1].query)
	}
	for i, c := range up.calls {
		if c.body != `{"contents":[]}` {
			t.Errorf("attempt %d body = %q", i+1, c.body)
		}
	}
}

// A prefix-stripped, host-rewritten reverse-proxy route (OpenAI at /openai)
// must replay to the same upstream path — built again from the inbound
// request, not stripped a second time from the first outbound one.
func TestProxy_GraceFallback_ReplayRebuildsOutboundPath(t *testing.T) {
	future := time.Now().Add(time.Hour)
	p, _, up := newGraceProxy(t, []Credential{{
		ID: "o1", Provider: ProviderOpenAI, Token: "sk-new",
		GraceToken: "sk-old", GraceExpiresAt: rfc3339(future), GraceRotationID: "rot-o1",
	}}, []int{401, 200}, nil, nil)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/openai/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer sk-dummy-crewship-sidecar")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if len(up.calls) != 2 {
		t.Fatalf("upstream called %d times, want 2", len(up.calls))
	}
	for i, c := range up.calls {
		if c.host != "api.openai.com" || c.path != "/v1/responses" {
			t.Errorf("attempt %d went to %s%s, want api.openai.com/v1/responses", i+1, c.host, c.path)
		}
	}
	if up.calls[0].auth != "Bearer sk-new" || up.calls[1].auth != "Bearer sk-old" {
		t.Errorf("auth = %q then %q, want the current key then the grace key", up.calls[0].auth, up.calls[1].auth)
	}
}
