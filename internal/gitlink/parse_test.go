package gitlink

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_RecognisedShapes(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		provider Provider
		host     string
		owner    string
		repo     string
		number   int
		canon    string
	}{
		{
			name:     "github.com pull request",
			raw:      "https://github.com/crewship-ai/crewship/pull/1752",
			provider: ProviderGitHub,
			host:     "github.com", owner: "crewship-ai", repo: "crewship", number: 1752,
			canon: "https://github.com/crewship-ai/crewship/pull/1752",
		},
		{
			name:     "github.com pull request with a files tab and a fragment",
			raw:      "https://github.com/crewship-ai/crewship/pull/1752/files#diff-abc",
			provider: ProviderGitHub,
			host:     "github.com", owner: "crewship-ai", repo: "crewship", number: 1752,
			canon: "https://github.com/crewship-ai/crewship/pull/1752",
		},
		{
			// The whole point of the grammar-based discriminator: this host
			// says nothing, the /pull/ segment says everything.
			name:     "self-hosted GitHub Enterprise",
			raw:      "https://ghe.acme.example/platform/api-gateway/pull/9",
			provider: ProviderGitHub,
			host:     "ghe.acme.example", owner: "platform", repo: "api-gateway", number: 9,
			canon: "https://ghe.acme.example/platform/api-gateway/pull/9",
		},
		{
			name:     "gitlab.com merge request",
			raw:      "https://gitlab.com/gitlab-org/gitlab/-/merge_requests/12345",
			provider: ProviderGitLab,
			host:     "gitlab.com", owner: "gitlab-org", repo: "gitlab", number: 12345,
			canon: "https://gitlab.com/gitlab-org/gitlab/-/merge_requests/12345",
		},
		{
			name:     "gitlab nested subgroups",
			raw:      "https://gitlab.com/acme/platform/backend/billing/-/merge_requests/42",
			provider: ProviderGitLab,
			host:     "gitlab.com", owner: "acme/platform/backend", repo: "billing", number: 42,
			canon: "https://gitlab.com/acme/platform/backend/billing/-/merge_requests/42",
		},
		{
			name:     "gitlab legacy path without the -/ separator",
			raw:      "https://gitlab.example.org/acme/billing/merge_requests/7",
			provider: ProviderGitLab,
			host:     "gitlab.example.org", owner: "acme", repo: "billing", number: 7,
			canon: "https://gitlab.example.org/acme/billing/-/merge_requests/7",
		},
		{
			// Same host serving both products is exactly the case a
			// host-based discriminator gets wrong.
			name:     "one host, GitLab grammar",
			raw:      "https://code.acme.example/acme/thing/-/merge_requests/3",
			provider: ProviderGitLab,
			host:     "code.acme.example", owner: "acme", repo: "thing", number: 3,
			canon: "https://code.acme.example/acme/thing/-/merge_requests/3",
		},
		{
			name:     "one host, GitHub grammar",
			raw:      "https://code.acme.example/acme/thing/pull/3",
			provider: ProviderGitHub,
			host:     "code.acme.example", owner: "acme", repo: "thing", number: 3,
			canon: "https://code.acme.example/acme/thing/pull/3",
		},
		{
			name:     "host case and trailing slash are normalised",
			raw:      "https://GitHub.COM/Crewship-AI/Crewship/pull/1/",
			provider: ProviderGitHub,
			host:     "github.com", owner: "Crewship-AI", repo: "Crewship", number: 1,
			canon: "https://github.com/Crewship-AI/Crewship/pull/1",
		},
		{
			name:     "explicit port is kept",
			raw:      "https://gitlab.acme.example:8443/acme/thing/-/merge_requests/11",
			provider: ProviderGitLab,
			host:     "gitlab.acme.example:8443", owner: "acme", repo: "thing", number: 11,
			canon: "https://gitlab.acme.example:8443/acme/thing/-/merge_requests/11",
		},
		{
			// :443 under https says nothing. Keeping it would make this a
			// DIFFERENT link from the plain one — a second row past the
			// duplicate check, a credential lookup for "github.com:443" that
			// finds nothing, and an API endpoint under /api/v3 instead of
			// api.github.com.
			name:     "default https port is dropped",
			raw:      "https://github.com:443/acme/thing/pull/7",
			provider: ProviderGitHub,
			host:     "github.com", owner: "acme", repo: "thing", number: 7,
			canon: "https://github.com/acme/thing/pull/7",
		},
		{
			name:     "default http port is dropped",
			raw:      "http://code.acme.example:80/acme/thing/pull/7",
			provider: ProviderGitHub,
			host:     "code.acme.example", owner: "acme", repo: "thing", number: 7,
			canon: "http://code.acme.example/acme/thing/pull/7",
		},
		{
			// …but a port that is only the OTHER scheme's default is real.
			name:     "a port that is another scheme's default survives",
			raw:      "https://code.acme.example:80/acme/thing/pull/7",
			provider: ProviderGitHub,
			host:     "code.acme.example:80", owner: "acme", repo: "thing", number: 7,
			canon: "https://code.acme.example:80/acme/thing/pull/7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q) = error %v, want a ref", tt.raw, err)
			}
			if ref.Provider != tt.provider {
				t.Errorf("provider = %q, want %q", ref.Provider, tt.provider)
			}
			if ref.Host != tt.host {
				t.Errorf("host = %q, want %q", ref.Host, tt.host)
			}
			if ref.Owner != tt.owner {
				t.Errorf("owner = %q, want %q", ref.Owner, tt.owner)
			}
			if ref.Repo != tt.repo {
				t.Errorf("repo = %q, want %q", ref.Repo, tt.repo)
			}
			if ref.Number != tt.number {
				t.Errorf("number = %d, want %d", ref.Number, tt.number)
			}
			if ref.URL != tt.canon {
				t.Errorf("canonical url = %q, want %q", ref.URL, tt.canon)
			}
			if ref.Kind != KindPullRequest {
				t.Errorf("kind = %q, want %q", ref.Kind, KindPullRequest)
			}
		})
	}
}

func TestParse_Rejects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not a url", "just some words"},
		{"issue, not a pull request", "https://github.com/crewship-ai/crewship/issues/1752"},
		{"repo root", "https://github.com/crewship-ai/crewship"},
		{"pull list without a number", "https://github.com/crewship-ai/crewship/pulls"},
		{"non numeric number", "https://github.com/crewship-ai/crewship/pull/abc"},
		{"zero number", "https://github.com/crewship-ai/crewship/pull/0"},
		{"negative number", "https://github.com/crewship-ai/crewship/pull/-3"},
		{"pull at the wrong depth", "https://github.com/a/b/c/pull/1"},
		{"gitlab mr without a project", "https://gitlab.com/-/merge_requests/1"},
		{"gitlab mr without a number", "https://gitlab.com/acme/thing/-/merge_requests"},
		{"ftp scheme", "ftp://github.com/a/b/pull/1"},
		{"no scheme", "github.com/a/b/pull/1"},
		{"no host", "https:///a/b/pull/1"},
		// Credentials in a URL are how a paste turns into a token the
		// server would then log. ValidateURL rejects them downstream too;
		// rejecting at parse means we never even build the API request.
		{"userinfo", "https://user:ghp_secret@github.com/a/b/pull/1"},
		// Path traversal in the owner/repo would be re-encoded into the
		// provider's API path.
		{"dot segment in owner", "https://github.com/../etc/pull/1"},
		{"traversal in repo", "https://github.com/acme/..%2f..%2fadmin/pull/1"},
		{"backslash in repo", "https://github.com/acme/th\\ing/pull/1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.raw)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want an error", tt.raw, ref)
			}
			if !errors.Is(err, ErrUnsupportedURL) {
				t.Errorf("Parse(%q) error = %v, want it to wrap ErrUnsupportedURL", tt.raw, err)
			}
		})
	}
}

// A pathological paste must not become a pathological API path.
func TestParse_RejectsAbsurdlyDeepOwnerPath(t *testing.T) {
	deep := "https://gitlab.com/" + strings.Repeat("g/", maxOwnerSegments+2) + "repo/-/merge_requests/1"
	if _, err := Parse(deep); !errors.Is(err, ErrUnsupportedURL) {
		t.Fatalf("Parse(deep) error = %v, want ErrUnsupportedURL", err)
	}
}

func TestRef_APIEndpointFor(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "github.com uses the api subdomain",
			raw:  "https://github.com/crewship-ai/crewship/pull/7",
			want: "https://api.github.com/repos/crewship-ai/crewship/pulls/7",
		},
		{
			// The normalisation above is what keeps this from becoming the
			// GitHub Enterprise layout on the SaaS host.
			name: "an explicit :443 still resolves to api.github.com",
			raw:  "https://github.com:443/crewship-ai/crewship/pull/7",
			want: "https://api.github.com/repos/crewship-ai/crewship/pulls/7",
		},
		{
			name: "self-hosted GitHub uses the /api/v3 prefix on its own host",
			raw:  "https://ghe.acme.example/platform/gw/pull/7",
			want: "https://ghe.acme.example/api/v3/repos/platform/gw/pulls/7",
		},
		{
			name: "gitlab projects are addressed by url-encoded path",
			raw:  "https://gitlab.com/acme/platform/billing/-/merge_requests/7",
			want: "https://gitlab.com/api/v4/projects/acme%2Fplatform%2Fbilling/merge_requests/7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			// Production passes the host the CALLER vetted. Every case here
			// passes ref.Host, which is what the resolver returns for a
			// credential labelled with that host — so these assertions pin the
			// endpoints unchanged by the switch to an explicit host argument.
			if got := ref.APIEndpointFor(ref.Host); got != tt.want {
				t.Errorf("APIEndpointFor(%q) = %q, want %q", ref.Host, got, tt.want)
			}
		})
	}
}

// The endpoint's HOST comes from the argument, never from the pasted URL.
//
// This is the property that makes the request target come from a value the
// server looked up (a credential's account_label, or a canonical SaaS host
// constant) rather than from user input. If APIEndpointFor ever read r.Host
// again, the taint path from the pasted URL to http.Client.Do reopens and
// go/request-forgery fires again — that alert is the regression test that runs
// in CI, and this is the one that runs in `go test`.
func TestRef_APIEndpointFor_UsesTheSuppliedHostNotTheParsedOne(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		vetted  string
		want    string
		wantErr bool
	}{
		{
			name:   "github enterprise",
			raw:    "https://ghe.acme.example/platform/gw/pull/7",
			vetted: "ghe.acme.example",
			want:   "https://ghe.acme.example/api/v3/repos/platform/gw/pulls/7",
		},
		{
			name:   "gitlab self-managed",
			raw:    "https://gitlab.acme.internal/acme/billing/-/merge_requests/7",
			vetted: "gitlab.acme.internal",
			want:   "https://gitlab.acme.internal/api/v4/projects/acme%2Fbilling/merge_requests/7",
		},
		{
			// The scheme is rebuilt from a constant, not carried through from
			// the paste, so an http:// link can only ever produce "http://" or
			// "https://" and never a third thing.
			name:   "http paste keeps http",
			raw:    "http://gitlab.acme.internal/acme/billing/-/merge_requests/7",
			vetted: "gitlab.acme.internal",
			want:   "http://gitlab.acme.internal/api/v4/projects/acme%2Fbilling/merge_requests/7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := ref.APIEndpointFor(tt.vetted); got != tt.want {
				t.Errorf("APIEndpointFor(%q) = %q, want %q", tt.vetted, got, tt.want)
			}
			// Now mutate the parsed host to something the caller never vetted.
			// The endpoint must not move.
			ref.Host = "attacker.example"
			if got := ref.APIEndpointFor(tt.vetted); got != tt.want {
				t.Errorf("the parsed host leaked into the endpoint: got %q, want %q", got, tt.want)
			}
		})
	}
}
