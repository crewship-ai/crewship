package gitlink

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// refFor builds a Ref aimed at a test server. Parse cannot produce this
// (it takes a web URL, and the test server has no web routes), so the
// struct is built directly — every other field goes through the same code
// path production uses.
func refFor(t *testing.T, p Provider, hostPort string) Ref {
	t.Helper()
	return Ref{
		Provider: p,
		Host:     hostPort,
		Owner:    "acme",
		Repo:     "thing",
		Number:   7,
		Kind:     KindPullRequest,
		Scheme:   "http",
		URL:      "http://" + hostPort + "/acme/thing/pull/7",
	}
}

const githubPullJSON = `{
	"number": 7,
	"title": "Add the widget",
	"state": "open",
	"draft": false,
	"merged_at": null,
	"closed_at": null,
	"created_at": "2026-08-01T10:00:00Z",
	"updated_at": "2026-08-02T11:30:00Z",
	"html_url": "https://github.com/acme/thing/pull/7",
	"user": {"login": "octocat"},
	"head": {"ref": "feat/widget"},
	"base": {"ref": "main"}
}`

// ─── SSRF ────────────────────────────────────────────────────────────────

// A pasted "PR link" that points at the loopback interface must not become a
// server-side request — and, critically, must not become a server-side
// request WITH A CREDENTIAL ATTACHED. The assertion is therefore twofold:
// the call fails with ErrBlockedHost, and the listener never saw a byte.
func TestFetch_RefusesLoopbackHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("credential leaked to a blocked host: Authorization=%q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(githubPullJSON))
	}))
	defer srv.Close()

	c := NewClient(false) // strict: the production default
	_, err := c.Fetch(context.Background(), refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "ghp_supersecret")
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("Fetch to loopback = %v, want ErrBlockedHost", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("blocked host still received %d request(s); the refusal happened after the dial", n)
	}
}

// The teeth of the test above: with the operator opt-in the SAME call reaches
// the SAME listener and carries the token. Without this, TestFetch_Refuses-
// LoopbackHost would keep passing if Fetch broke for any unrelated reason
// (wrong URL, no route, mis-built client) and prove nothing about SSRF.
func TestFetch_ReachesPrivateHostWhenOperatorAllowsIt(t *testing.T) {
	var hits int32
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(githubPullJSON))
	}))
	defer srv.Close()

	c := NewClient(true)
	got, err := c.Fetch(context.Background(), refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "ghp_supersecret")
	if err != nil {
		t.Fatalf("Fetch with allowPrivate = %v, want success", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("listener saw %d requests, want 1", n)
	}
	if sawAuth != "Bearer ghp_supersecret" {
		t.Errorf("Authorization = %q, want the bearer token", sawAuth)
	}
	if got.Title != "Add the widget" {
		t.Errorf("title = %q", got.Title)
	}
}

// The cloud-metadata range is refused even with the opt-in on. This is the
// httpsafe "hard tier": RFC1918 becomes reachable when an operator says so;
// 169.254.169.254 never does.
func TestFetch_RefusesLinkLocalEvenWithOptIn(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "[fd00:ec2::254]", "[::ffff:169.254.169.254]"} {
		t.Run(host, func(t *testing.T) {
			c := NewClient(true)
			_, err := c.Fetch(context.Background(), refFor(t, ProviderGitHub, host), "tok")
			if !errors.Is(err, ErrBlockedHost) {
				t.Fatalf("Fetch(%s) = %v, want ErrBlockedHost", host, err)
			}
		})
	}
}

// Strict mode is https-only: an http:// paste at a public host must not
// silently downgrade the transport the token rides on.
func TestFetch_StrictModeRejectsPlainHTTP(t *testing.T) {
	ref := refFor(t, ProviderGitHub, "code.example.com")
	c := NewClient(false)
	_, err := c.Fetch(context.Background(), ref, "tok")
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("Fetch(http://public) = %v, want ErrBlockedHost", err)
	}
}

// ─── error mapping ───────────────────────────────────────────────────────

func TestFetch_ErrorMapping(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		headers   map[string]string
		body      string
		want      error
		wantRetry time.Duration
		detailHas string
		provider  Provider
	}{
		{name: "401 revoked token", status: 401, want: ErrUnauthorized, provider: ProviderGitHub},
		{name: "404 private or missing", status: 404, want: ErrNotFound, provider: ProviderGitHub},
		{name: "403 no access", status: 403, want: ErrForbidden, provider: ProviderGitHub},
		{
			name:      "403 github rate limit",
			status:    403,
			headers:   map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1"},
			want:      ErrRateLimited,
			provider:  ProviderGitHub,
			detailHas: "rate limit",
		},
		{
			name:      "429 with Retry-After",
			status:    429,
			headers:   map[string]string{"Retry-After": "42"},
			want:      ErrRateLimited,
			wantRetry: 42 * time.Second,
			provider:  ProviderGitLab,
		},
		{name: "500 provider down", status: 500, want: ErrProviderUnavailable, provider: ProviderGitHub},
		{name: "503 provider down", status: 503, want: ErrProviderUnavailable, provider: ProviderGitLab},
		{name: "418 unexpected", status: 418, want: ErrUnexpectedStatus, provider: ProviderGitHub},
		{
			name:     "200 but not JSON",
			status:   200,
			body:     "<html>login</html>",
			want:     ErrUnexpectedStatus,
			provider: ProviderGitHub,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := NewClient(true)
			_, err := c.Fetch(context.Background(), refFor(t, tt.provider, srv.Listener.Addr().String()), "tok")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			var fe *FetchError
			if !errors.As(err, &fe) {
				t.Fatalf("error %v is not a *FetchError", err)
			}
			if tt.status != 0 && fe.Status != tt.status {
				t.Errorf("FetchError.Status = %d, want %d", fe.Status, tt.status)
			}
			if tt.wantRetry != 0 && fe.RetryAfter != tt.wantRetry {
				t.Errorf("RetryAfter = %v, want %v", fe.RetryAfter, tt.wantRetry)
			}
			if tt.detailHas != "" && !strings.Contains(strings.ToLower(fe.Error()), tt.detailHas) {
				t.Errorf("error %q does not mention %q", fe.Error(), tt.detailHas)
			}
			// Whatever went wrong, the token must never reach the message
			// — these strings land in an API response and the DB.
			if strings.Contains(fe.Error(), "tok") && !strings.Contains(fe.Error(), "token") {
				t.Errorf("error message may contain the credential: %q", fe.Error())
			}
		})
	}
}

// ─── payload decoding ────────────────────────────────────────────────────

func TestFetch_GitHubStates(t *testing.T) {
	tests := []struct {
		name string
		body string
		want State
	}{
		{"open", githubPullJSON, StateOpen},
		{"draft", strings.Replace(githubPullJSON, `"draft": false`, `"draft": true`, 1), StateDraft},
		{
			"merged",
			strings.NewReplacer(`"state": "open"`, `"state": "closed"`, `"merged_at": null`, `"merged_at": "2026-08-03T09:00:00Z"`).Replace(githubPullJSON),
			StateMerged,
		},
		{"closed", strings.Replace(githubPullJSON, `"state": "open"`, `"state": "closed"`, 1), StateClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if want := "/api/v3/repos/acme/thing/pulls/7"; r.URL.Path != want {
					t.Errorf("path = %q, want %q", r.URL.Path, want)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			got, err := NewClient(true).Fetch(context.Background(),
				refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "tok")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got.State != tt.want {
				t.Errorf("state = %q, want %q", got.State, tt.want)
			}
			if got.Author != "octocat" || got.SourceBranch != "feat/widget" || got.TargetBranch != "main" {
				t.Errorf("unexpected details: %+v", got)
			}
			if got.CreatedAt != "2026-08-01T10:00:00Z" || got.UpdatedAt != "2026-08-02T11:30:00Z" {
				t.Errorf("timestamps not carried through: %+v", got)
			}
		})
	}
}

func TestFetch_GitLabStates(t *testing.T) {
	const base = `{
		"iid": 7,
		"title": "Add the widget",
		"state": %q,
		"draft": %s,
		"author": {"username": "tanuki"},
		"source_branch": "feat/widget",
		"target_branch": "main",
		"created_at": "2026-08-01T10:00:00Z",
		"updated_at": "2026-08-02T11:30:00Z",
		"merged_at": null,
		"closed_at": null,
		"web_url": "https://gitlab.com/acme/thing/-/merge_requests/7"
	}`
	tests := []struct {
		name  string
		state string
		draft string
		want  State
	}{
		{"opened", "opened", "false", StateOpen},
		{"draft", "opened", "true", StateDraft},
		{"merged", "merged", "false", StateMerged},
		{"closed", "closed", "false", StateClosed},
		{"locked reads as open", "locked", "false", StateOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if want := "/api/v4/projects/acme%2Fthing/merge_requests/7"; r.URL.EscapedPath() != want {
					t.Errorf("path = %q, want %q", r.URL.EscapedPath(), want)
				}
				if got := r.Header.Get("PRIVATE-TOKEN"); got != "tok" {
					t.Errorf("PRIVATE-TOKEN = %q, want the token", got)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("GitLab must not receive an Authorization header: %q", got)
				}
				_, _ = w.Write([]byte(sprintfState(base, tt.state, tt.draft)))
			}))
			defer srv.Close()

			got, err := NewClient(true).Fetch(context.Background(),
				refFor(t, ProviderGitLab, srv.Listener.Addr().String()), "tok")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if got.State != tt.want {
				t.Errorf("state = %q, want %q", got.State, tt.want)
			}
			if got.Author != "tanuki" {
				t.Errorf("author = %q", got.Author)
			}
		})
	}
}

// A hostile provider (or a compromised one) must not be able to make us hold
// an unbounded response in memory.
func TestFetch_CapsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"` + strings.Repeat("A", maxResponseBytes+4096) + `"}`))
	}))
	defer srv.Close()

	_, err := NewClient(true).Fetch(context.Background(),
		refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "tok")
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("oversized body = %v, want ErrUnexpectedStatus (truncated JSON)", err)
	}
}

func sprintfState(tmpl, state, draft string) string {
	return strings.Replace(strings.Replace(tmpl, "%q", `"`+state+`"`, 1), "%s", draft, 1)
}
