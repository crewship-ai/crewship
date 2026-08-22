package llmroute

import (
	"strings"
	"testing"
)

// TestResolveUpstream_FixedHost — a spec with its own host ignores whatever a
// credential says. An operator cannot redirect Anthropic traffic by putting a
// URL in a credential, and the redirect attempt is not even an error: the
// field simply has no meaning for that provider.
func TestResolveUpstream_FixedHost(t *testing.T) {
	cases := []struct {
		specID       string
		credBaseURL  string
		wantHost     string
		wantBasePath string
	}{
		{"ANTHROPIC", "", "api.anthropic.com", ""},
		{"ANTHROPIC", "https://evil.example/v1", "api.anthropic.com", ""},
		{"OPENAI", "", "api.openai.com", ""},
		{"GOOGLE", "", "generativelanguage.googleapis.com", ""},
		{"OPENROUTER", "", "openrouter.ai", "/api/v1"},
		{"OPENROUTER", "http://127.0.0.1:1/", "openrouter.ai", "/api/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.specID+" "+tc.credBaseURL, func(t *testing.T) {
			s, ok := Lookup(tc.specID)
			if !ok {
				t.Fatalf("Lookup(%q) found nothing", tc.specID)
			}
			u, err := ResolveUpstream(s, tc.credBaseURL)
			if err != nil {
				t.Fatalf("ResolveUpstream: %v", err)
			}
			if u.Scheme != "https" {
				t.Errorf("Scheme = %q, want https", u.Scheme)
			}
			if u.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", u.Host, tc.wantHost)
			}
			if u.BasePath != tc.wantBasePath {
				t.Errorf("BasePath = %q, want %q", u.BasePath, tc.wantBasePath)
			}
		})
	}
}

func TestResolveUpstream_FromCredential(t *testing.T) {
	compat, ok := Lookup("OPENAI_COMPAT")
	if !ok {
		t.Fatal("OPENAI_COMPAT is not registered")
	}

	t.Run("accepts", func(t *testing.T) {
		cases := []struct {
			base                       string
			wantScheme, wantHost, want string
		}{
			{"https://llm.internal.example/v1", "https", "llm.internal.example", "/v1"},
			{"https://llm.internal.example/v1/", "https", "llm.internal.example", "/v1"},
			{"https://llm.internal.example", "https", "llm.internal.example", ""},
			{"https://llm.internal.example/", "https", "llm.internal.example", ""},
			// A self-hosted runtime on a private network is plain http by
			// default; whether the crew may REACH it is the sidecar
			// allowlist's and the SSRF dialer's decision, not this one.
			{"http://ollama.lan:11434/v1", "http", "ollama.lan:11434", "/v1"},
			{"HTTPS://LLM.Internal.Example/v1", "https", "llm.internal.example", "/v1"},
			// Query and fragment are dropped rather than concatenated onto
			// every agent request.
			{"https://llm.internal.example/v1?tenant=a#x", "https", "llm.internal.example", "/v1"},
		}
		for _, tc := range cases {
			t.Run(tc.base, func(t *testing.T) {
				u, err := ResolveUpstream(compat, tc.base)
				if err != nil {
					t.Fatalf("ResolveUpstream(%q): %v", tc.base, err)
				}
				if u.Scheme != tc.wantScheme || u.Host != tc.wantHost || u.BasePath != tc.want {
					t.Errorf("= %+v, want %s://%s%s", u, tc.wantScheme, tc.wantHost, tc.want)
				}
			})
		}
	})

	t.Run("rejects", func(t *testing.T) {
		cases := []struct {
			name    string
			base    string
			wantMsg string
		}{
			{"empty", "", "carries no base URL"},
			{"file scheme", "file:///etc/passwd", "not http or https"},
			{"gopher scheme", "gopher://llm.internal.example/", "not http or https"},
			{"no scheme", "llm.internal.example/v1", "not http or https"},
			{"scheme but no host", "https:///v1", "no host"},
			{"userinfo", "http://user:pass@llm.internal.example/v1", "must not carry userinfo"},
			{"userinfo without password", "https://token@llm.internal.example/v1", "must not carry userinfo"},
			{"oversized", "https://llm.internal.example/" + strings.Repeat("a", maxCredBaseURLBytes), "over the 2048-byte cap"},
			{"unparseable", "https://llm.internal.example/\x7f\x00", "not a URL"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u, err := ResolveUpstream(compat, tc.base)
				if err == nil {
					t.Fatalf("ResolveUpstream(%q) = %+v, want an error", tc.base, u)
				}
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("error = %q, want it to mention %q", err, tc.wantMsg)
				}
				if u != (Upstream{}) {
					t.Errorf("a rejected base URL still returned %+v; a caller ignoring the error would dial it", u)
				}
			})
		}
	})
}

// TestOutboundPath covers the two halves of the path rewrite: the routing
// prefix that must not reach the upstream, and the base path that must.
func TestOutboundPath(t *testing.T) {
	cases := []struct {
		name     string
		specID   string
		credBase string
		reqPath  string
		want     string
	}{
		{
			// StripPrefix=false: /v1/messages IS the upstream path, so it
			// travels verbatim. Stripping here would 404 every Claude Code
			// request.
			name: "anthropic forwards the path verbatim", specID: "ANTHROPIC",
			reqPath: "/v1/messages", want: "/v1/messages",
		},
		{
			name: "openai strips its routing prefix", specID: "OPENAI",
			reqPath: "/openai/v1/chat/completions", want: "/v1/chat/completions",
		},
		{
			name: "gemini strips its routing prefix", specID: "GOOGLE",
			reqPath: "/gemini/v1beta/models/gemini-3-pro:generateContent",
			want:    "/v1beta/models/gemini-3-pro:generateContent",
		},
		{
			// The prefix comes off and OpenRouter's own /api/v1 goes on, so
			// the agent's base URL stays a plain sidecar path.
			name: "openrouter joins its base path", specID: "OPENROUTER",
			reqPath: "/llm/openrouter/chat/completions", want: "/api/v1/chat/completions",
		},
		{
			name: "openai-compat joins the credential's base path", specID: "OPENAI_COMPAT",
			credBase: "https://llm.internal.example/v1",
			reqPath:  "/llm/openai-compat/chat/completions", want: "/v1/chat/completions",
		},
		{
			name: "openai-compat with a root endpoint", specID: "OPENAI_COMPAT",
			credBase: "https://llm.internal.example",
			reqPath:  "/llm/openai-compat/chat/completions", want: "/chat/completions",
		},
		{
			name: "openai-compat with a nested base path", specID: "OPENAI_COMPAT",
			credBase: "https://gw.internal.example/tenant/acme/v1",
			reqPath:  "/llm/openai-compat/models", want: "/tenant/acme/v1/models",
		},
		{
			// Defence in depth: MatchPath never routes a bare prefix here, but
			// an empty path is not a request target we are willing to emit.
			name: "stripping everything leaves a root path", specID: "OPENAI",
			reqPath: "/openai", want: "/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := Lookup(tc.specID)
			if !ok {
				t.Fatalf("Lookup(%q) found nothing", tc.specID)
			}
			u, err := ResolveUpstream(s, tc.credBase)
			if err != nil {
				t.Fatalf("ResolveUpstream: %v", err)
			}
			if got := OutboundPath(s, u, tc.reqPath); got != tc.want {
				t.Errorf("OutboundPath(%s, %q) = %q, want %q", tc.specID, tc.reqPath, got, tc.want)
			}
		})
	}
}

// TestOutboundPath_PrefixIsStrippedOnceAndOnlyAtTheFront — a path that repeats
// the prefix later on ("/openai/v1/openai/…") must keep the second copy, and a
// path whose body merely starts with the same letters must not lose them.
func TestOutboundPath_PrefixIsStrippedOnceAndOnlyAtTheFront(t *testing.T) {
	s, ok := Lookup("OPENAI")
	if !ok {
		t.Fatal("OPENAI is not registered")
	}
	u, err := ResolveUpstream(s, "")
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	cases := map[string]string{
		"/openai/v1/openai/models": "/v1/openai/models",
		"/openai/openai/v1":        "/openai/v1",
	}
	for in, want := range cases {
		if got := OutboundPath(s, u, in); got != want {
			t.Errorf("OutboundPath(%q) = %q, want %q", in, got, want)
		}
	}
}
