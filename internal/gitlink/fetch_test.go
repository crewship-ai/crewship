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

// githubPullJSON is the subset of a real GitHub pull-request object that this
// package reads.
//
// PROVENANCE — pinned from live responses on 2026-08-05, not hand-written:
//
//	GET https://api.github.com/repos/crewship-ai/crewship/pulls/1752   (merged)
//	GET https://api.github.com/repos/crewship-ai/crewship/pulls/1756   (closed, not merged)
//	GET https://api.github.com/repos/kubernetes/kubernetes/pulls/141127 (open draft)
//
// What those responses confirmed, field by field: `title`, `state`, `draft`,
// `number`, `user.login`, `head.ref`, `base.ref`, `created_at`, `updated_at`,
// `merged_at`, `closed_at`, `html_url` all exist with these names and types,
// and timestamps are second-precision RFC 3339 in UTC ("2026-08-04T14:30:48Z").
//
// The two that matter most:
//
//   - `merged_at` and `closed_at` are JSON **null** when unset — present, not
//     omitted. That decodes into a Go string field as a no-op (leaving ""),
//     which is what githubState relies on, so the fixture keeps the nulls
//     rather than dropping the keys.
//   - A merged pull request reports `"state": "closed"` with `"merged": true`
//     and a non-null `merged_at`. Reading `state` alone files every merge as a
//     rejection; see TestFetch_GitHubStates/merged.
//
// `merged` is carried here because the real payload has it and a reader will
// look for it — the code deliberately keys off merged_at instead, which is the
// field the list endpoints also return.
const githubPullJSON = `{
	"number": 7,
	"title": "Add the widget",
	"state": "open",
	"draft": false,
	"merged": false,
	"merged_at": null,
	"closed_at": null,
	"created_at": "2026-08-01T10:00:00Z",
	"updated_at": "2026-08-02T11:30:00Z",
	"html_url": "https://github.com/acme/thing/pull/7",
	"user": {"login": "octocat"},
	"head": {"ref": "feat/widget"},
	"base": {"ref": "main"}
}`

// fetchVetted calls Fetch the way production does: the host argument is the one
// the CALLER already matched against its own records (a credential's
// account_label, or a canonical SaaS host). In these tests that is the same
// host the ref was built for; TestFetch_RefusesAHostTheCallerDidNotVet is the
// case where the two differ.
func fetchVetted(c *Client, ref Ref, token string) (Details, error) {
	return c.Fetch(context.Background(), ref, ref.Host, token)
}

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
	_, err := fetchVetted(c, refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "ghp_supersecret")
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
	// atomic, not a plain string: the handler runs on the server's goroutine
	// and the assertion on the test's, and `go test -race ./internal/...` is a
	// CI job — a bare shared string here is exactly the shape that turns into
	// an intermittent red build on an unrelated PR.
	var sawAuth atomic.Value
	sawAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		sawAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(githubPullJSON))
	}))
	defer srv.Close()

	c := NewClient(true)
	got, err := fetchVetted(c, refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "ghp_supersecret")
	if err != nil {
		t.Fatalf("Fetch with allowPrivate = %v, want success", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("listener saw %d requests, want 1", n)
	}
	if got, _ := sawAuth.Load().(string); got != "Bearer ghp_supersecret" {
		t.Errorf("Authorization = %q, want the bearer token", got)
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
			_, err := fetchVetted(c, refFor(t, ProviderGitHub, host), "tok")
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
	_, err := fetchVetted(c, ref, "tok")
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("Fetch(http://public) = %v, want ErrBlockedHost", err)
	}
}

// ─── redirects must not carry the credential off-host ────────────────────

// GitLab's credential rides in PRIVATE-TOKEN, a CUSTOM header, and net/http
// only strips Authorization / Www-Authenticate / Cookie when a redirect
// crosses hosts. So without an explicit host pin, a forge answering 302 with
// somebody else's Location hands that somebody a live GitLab PAT — and an
// SSRF check waves it through, because the target is an ordinary public
// address. The assertion is that the second host receives NOTHING.
func TestFetch_RefusesCrossHostRedirect(t *testing.T) {
	var attackerHits int32
	var attackerToken atomic.Value
	attackerToken.Store("")
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		attackerToken.Store(r.Header.Get("PRIVATE-TOKEN"))
		_, _ = w.Write([]byte(`{"iid":7,"title":"pwned"}`))
	}))
	defer attacker.Close()

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/api/v4/projects/x/merge_requests/7", http.StatusFound)
	}))
	defer forge.Close()

	_, err := fetchVetted(NewClient(true),
		refFor(t, ProviderGitLab, forge.Listener.Addr().String()), "glpat_supersecret")
	if !errors.Is(err, ErrCrossHostRedirect) {
		t.Fatalf("Fetch across a redirect = %v, want ErrCrossHostRedirect", err)
	}
	if n := atomic.LoadInt32(&attackerHits); n != 0 {
		t.Fatalf("the redirect target was contacted %d time(s)", n)
	}
	if tok, _ := attackerToken.Load().(string); tok != "" {
		t.Fatalf("PRIVATE-TOKEN leaked to the redirect target: %q", tok)
	}
}

// The teeth: a redirect that STAYS on the origin host is still followed, so
// the test above is pinning the host rule and not merely "redirects break".
// GitHub answers 301 on the same API host for a renamed repository; refusing
// that would be a regression dressed up as hardening.
func TestFetch_FollowsSameHostRedirect(t *testing.T) {
	var hops int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/thing/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hops, 1)
		http.Redirect(w, r, "/api/v3/repos/acme/renamed/pulls/7", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/v3/repos/acme/renamed/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hops, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("token did not survive the same-host hop: %q", got)
		}
		_, _ = w.Write([]byte(githubPullJSON))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := fetchVetted(NewClient(true),
		refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "tok")
	if err != nil {
		t.Fatalf("same-host redirect = %v, want it followed", err)
	}
	if got.Title != "Add the widget" {
		t.Errorf("title = %q", got.Title)
	}
	if n := atomic.LoadInt32(&hops); n != 2 {
		t.Errorf("hops = %d, want 2 (the redirect was not actually exercised)", n)
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
			_, err := fetchVetted(c, refFor(t, tt.provider, srv.Listener.Addr().String()), "tok")
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

			got, err := fetchVetted(NewClient(true),
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

// gitlabMRTemplate is the subset of a real GitLab merge-request object that
// this package reads.
//
// PROVENANCE — pinned from live, UNAUTHENTICATED responses to gitlab.com on
// 2026-08-05 (public projects answer /api/v4 without a token):
//
//	GET .../projects/gitlab-org%2Fgitlab/merge_requests/200000        (merged)
//	GET .../projects/gitlab-org%2Fgitlab/merge_requests?state=opened&wip=yes
//	GET .../projects/gitlab-org%2Fgitlab/merge_requests?state=closed
//
// Confirmed: `iid`, `title`, `state`, `draft`, `work_in_progress`,
// `author.username`, `source_branch`, `target_branch`, `created_at`,
// `updated_at`, `merged_at`, `closed_at`, `web_url` — all present with these
// names, and `merged_at` / `closed_at` null rather than absent when unset.
//
// Two things the live payloads corrected in this fixture:
//
//   - GitLab timestamps carry MILLISECONDS and GitHub's do not
//     ("2025-08-01T15:07:25.663Z" vs "2026-08-04T14:30:48Z"). We store the
//     provider string verbatim, so the fixture now uses the real shape rather
//     than a tidied-up one — anything downstream that starts parsing these has
//     to cope with both.
//   - `draft` and `work_in_progress` are BOTH still sent by current gitlab.com
//     and they agree. The pre-14.0 name is not a legacy-only field, so it is
//     in the fixture rather than only in the parser.
const gitlabMRTemplate = `{
	"iid": 7,
	"title": "Add the widget",
	"state": "__STATE__",
	"draft": __DRAFT__,
	"work_in_progress": __DRAFT__,
	"author": {"username": "tanuki"},
	"source_branch": "feat/widget",
	"target_branch": "main",
	"created_at": "2026-08-01T10:00:00.784Z",
	"updated_at": "2026-08-02T11:30:00.906Z",
	"merged_at": null,
	"closed_at": null,
	"web_url": "https://gitlab.com/acme/thing/-/merge_requests/7"
}`

func gitlabMRBody(state, draft string) string {
	return strings.NewReplacer("__STATE__", state, "__DRAFT__", draft).Replace(gitlabMRTemplate)
}

func TestFetch_GitLabStates(t *testing.T) {
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
		// This combination is not hypothetical: gitlab.com MR
		// gitlab-org/gitlab!248672 was `"state": "closed"` with
		// `"draft": true` on 2026-08-05. A closed draft is CLOSED — the state
		// check has to come before the draft check, and this is the case that
		// fails if someone reorders them.
		{"a closed draft is closed, not draft", "closed", "true", StateClosed},
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
				_, _ = w.Write([]byte(gitlabMRBody(tt.state, tt.draft)))
			}))
			defer srv.Close()

			got, err := fetchVetted(NewClient(true),
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
			// Millisecond precision is what gitlab.com actually sends; it must
			// survive verbatim rather than being reformatted or dropped.
			if got.CreatedAt != "2026-08-01T10:00:00.784Z" || got.UpdatedAt != "2026-08-02T11:30:00.906Z" {
				t.Errorf("timestamps not carried through verbatim: %+v", got)
			}
		})
	}
}

// A self-managed GitLab from before 14.0 sends `work_in_progress` and no
// `draft` key at all. Current gitlab.com sends both, so the live payloads
// cannot distinguish the two paths — this one does.
func TestFetch_GitLabPre14DraftField(t *testing.T) {
	const body = `{
		"iid": 7, "title": "Add the widget", "state": "opened",
		"work_in_progress": true,
		"author": {"username": "tanuki"},
		"source_branch": "feat/widget", "target_branch": "main"
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := fetchVetted(NewClient(true),
		refFor(t, ProviderGitLab, srv.Listener.Addr().String()), "tok")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.State != StateDraft {
		t.Errorf("state = %q, want DRAFT (work_in_progress without draft)", got.State)
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

	_, err := fetchVetted(NewClient(true),
		refFor(t, ProviderGitHub, srv.Listener.Addr().String()), "tok")
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("oversized body = %v, want ErrUnexpectedStatus (truncated JSON)", err)
	}
}

// ─── the request goes where the CALLER said, not where the paste said ────

// Fetch dials the host its caller vetted. If that host is not the one the
// pasted URL named, the call is refused before any dial — the two can only
// disagree through a bug, and a bug in host selection is the one that sends a
// credential somewhere it was never meant to go.
//
// This is also what breaks the taint path CodeQL's go/request-forgery follows:
// the request URL is built from the argument (a value the server looked up),
// never from ref.Host (a value the user pasted).
func TestFetch_RefusesAHostTheCallerDidNotVet(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(githubPullJSON))
	}))
	defer srv.Close()

	// The ref names a host the caller never approved; the vetted host is the
	// listener. Production can never build this pair — resolveCodeLinkCredential
	// matches on the ref's own host — which is exactly why it is asserted here.
	ref := refFor(t, ProviderGitHub, "somewhere-else.example")
	_, err := NewClient(true).Fetch(context.Background(), ref, srv.Listener.Addr().String(), "tok")
	if !errors.Is(err, ErrHostMismatch) {
		t.Fatalf("Fetch with a mismatched host = %v, want ErrHostMismatch", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("the mismatch was noticed after %d request(s); it must precede the dial", n)
	}
}

// The teeth of the test above: the SAME listener, the SAME client, with the
// ref and the vetted host agreeing — so the refusal is pinned to the mismatch
// and not to "Fetch cannot reach an httptest server".
func TestFetch_ReachesTheVettedHost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(githubPullJSON))
	}))
	defer srv.Close()

	hostPort := srv.Listener.Addr().String()
	got, err := NewClient(true).Fetch(context.Background(), refFor(t, ProviderGitHub, hostPort), hostPort, "tok")
	if err != nil {
		t.Fatalf("Fetch to the vetted host = %v, want success", err)
	}
	if got.Title != "Add the widget" {
		t.Errorf("title = %q", got.Title)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("listener saw %d requests, want 1", n)
	}
}

// Host comparison is case-insensitive: a credential labelled "GHE.Acme.Example"
// resolves for a link pasted as ghe.acme.example. Refusing that would break a
// real deployment in the name of a defence that gains nothing — the label is
// still the thing that decides where the request goes.
func TestFetch_HostMatchIsCaseInsensitive(t *testing.T) {
	ref := refFor(t, ProviderGitHub, "ghe.acme.example")
	// Strict mode, so the call still fails — but on the SCHEME (an http paste
	// at a public host), which is only reachable if the host comparison let the
	// case difference through. No dial happens either way.
	_, err := NewClient(false).Fetch(context.Background(), ref, "GHE.Acme.Example", "tok")
	if errors.Is(err, ErrHostMismatch) {
		t.Fatalf("a case-differing host was treated as a mismatch: %v", err)
	}
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("error = %v, want ErrBlockedHost (the scheme check, reached past the host check)", err)
	}
}
