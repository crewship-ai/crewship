package inbox

import "testing"

func TestSanitizeReason(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantInfra bool
		// wantBody is the exact expected friendly text; empty means
		// "don't assert exact, just assert it's not the raw leak".
		wantBody string
	}{
		{
			name:      "memory health LLM-down leak is flagged infra + scrubbed",
			in:        "MemoryHealth Curator unavailable or unparseable — operator review (underlying: Keeper LLM unavailable: paymaster: workspace_id required — deny by default)",
			wantInfra: true,
			wantBody:  infraFriendly,
		},
		{
			name:      "skill curator leak is flagged infra",
			in:        "Curator unavailable or returned unparseable response — operator review (underlying: Keeper LLM unavailable: paymaster: workspace_id required — deny by default)",
			wantInfra: true,
			wantBody:  infraFriendly,
		},
		{
			name:      "llm not configured is infra",
			in:        "Keeper LLM not configured — deny by default",
			wantInfra: true,
			wantBody:  infraFriendly,
		},
		{
			name:      "real finding passes through",
			in:        "Crew memory has 4 contradictions in deployment runbook entries.",
			wantInfra: false,
			wantBody:  "Crew memory has 4 contradictions in deployment runbook entries.",
		},
		{
			// Regression: a genuine finding that merely contains a loose
			// word like "unconfigured" must NOT be flagged as an infra
			// outage and silently suppressed by the caller.
			name:      "real finding containing 'unconfigured' is not infra",
			in:        "Crew memory references an unconfigured deployment target.",
			wantInfra: false,
			wantBody:  "Crew memory references an unconfigured deployment target.",
		},
		{
			name:      "empty stays empty, not infra",
			in:        "",
			wantInfra: false,
			wantBody:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, infra := SanitizeReason(tc.in)
			if infra != tc.wantInfra {
				t.Errorf("infra = %v, want %v", infra, tc.wantInfra)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			// The raw plumbing detail must never survive into a user body.
			if tc.wantInfra {
				for _, leak := range []string{"paymaster", "deny by default", "underlying:", "LLM"} {
					if contains(body, leak) {
						t.Errorf("sanitized body still leaks %q: %q", leak, body)
					}
				}
			}
		})
	}
}

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		absent string // substring that must NOT appear in output
	}{
		{
			name:   "redis connection string masks userinfo",
			in:     "Generated credential redis://:Ug#6Mu52Q~~v5tW^_RfCMsRN@127.0.0.1:6379/0 for testing",
			absent: "Ug#6Mu52Q",
		},
		{
			name:   "postgres uri masks user:pass",
			in:     "DSN: postgres://admin:s3cr3tP@db.internal:5432/app",
			absent: "s3cr3tP",
		},
		{
			name:   "key=value secret masked",
			in:     "use password=hunter2tokenvalue when connecting",
			absent: "hunter2tokenvalue",
		},
		{
			name:   "labelled token masked",
			in:     "api_key: ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ012345",
			absent: "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ012345",
		},
		{
			// Provider-aware via lookout: a bare (unlabelled, 20-char) AWS
			// access key id is below any generic blob bound but must still
			// be masked.
			name:   "bare AWS access key id masked via lookout",
			in:     "deploy used AKIAIOSFODNN7EXAMPLE for s3",
			absent: "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name: "clean text unchanged",
			in:   "ENG-6 ready for review",
			want: "ENG-6 ready for review",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.in)
			if tc.want != "" && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if tc.absent != "" && contains(got, tc.absent) {
				t.Errorf("secret leaked: %q still contains %q", got, tc.absent)
			}
		})
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		max      int
		fallback string
		want     string
	}{
		{
			name:     "strips markdown heading and newlines",
			body:     "Approve this production action?\n\n## Change Plan: Restart auth-svc Pods",
			max:      80,
			fallback: "Waitpoint",
			want:     "Approve this production action?",
		},
		{
			name:     "strips leading hashes on first line",
			body:     "## Change Plan: Restart auth-svc Pods in Production",
			max:      80,
			fallback: "Waitpoint",
			want:     "Change Plan: Restart auth-svc Pods in Production",
		},
		{
			name:     "truncates with ellipsis",
			body:     "This is a very long approval prompt that keeps going well past the limit boundary here",
			max:      20,
			fallback: "Waitpoint",
			want:     "This is a very long…",
		},
		{
			name:     "empty body falls back",
			body:     "   \n\n  ",
			max:      80,
			fallback: "Waitpoint pending approval",
			want:     "Waitpoint pending approval",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanTitle(tc.body, tc.max, tc.fallback)
			if got != tc.want {
				t.Errorf("CleanTitle = %q, want %q", got, tc.want)
			}
			if r := []rune(got); tc.max > 0 && len(r) > tc.max {
				t.Errorf("CleanTitle len %d exceeds max %d: %q", len(r), tc.max, got)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
