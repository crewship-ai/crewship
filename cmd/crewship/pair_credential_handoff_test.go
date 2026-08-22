package main

// The wizard used to tell a paired user: "Run `crewship setup` after
// launching and it will ask for [the token] there — nothing else to do here."
// Every clause of that is wrong once Launch has run:
//
//   - `crewship setup` answers 409 "Onboarding already completed".
//   - A credential created afterwards is not delivered to the agents that
//     already exist: autoAssignCredentials links workspace credentials to
//     agents at DEPLOY time, and the read-time delivery query has three arms
//     (explicit agent_credentials rows, slot bindings, crew links) — none of
//     which a bare workspace-scoped credential satisfies.
//
// So the only cheap moment to land the token is BEFORE the crew is deployed,
// which is exactly when the CLI finishes pairing. These tests pin the decision
// core of that handoff; the prompt/IO around it stays thin on purpose.

import "testing"

func TestNeedsModelTokenHandoff(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		creds    []pairedCredential
		want     bool
		why      string
	}{
		{
			name:     "no credentials at all",
			provider: "ANTHROPIC",
			creds:    nil,
			want:     true,
			why:      "the empty workspace right after pairing — this is the case that shipped broken",
		},
		{
			name:     "matching provider already present",
			provider: "ANTHROPIC",
			creds:    []pairedCredential{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Provider: "ANTHROPIC"}},
			want:     false,
			why:      "re-pairing must not nag for a token the workspace already holds",
		},
		{
			name:     "only another provider's credential",
			provider: "ANTHROPIC",
			creds:    []pairedCredential{{Name: "GEMINI_API_KEY", Provider: "GOOGLE"}},
			want:     true,
			why:      "a Google key does not let a Claude Code agent call anything",
		},
		{
			name:     "provider match is case-insensitive",
			provider: "anthropic",
			creds:    []pairedCredential{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Provider: "ANTHROPIC"}},
			want:     false,
			why:      "the API echoes upper-case; the adapter table spells it lower — neither should cause a duplicate",
		},
		{
			name:     "a soft-deleted credential does not count",
			provider: "ANTHROPIC",
			creds:    []pairedCredential{{Name: "CLAUDE_CODE_OAUTH_TOKEN", Provider: "ANTHROPIC", Status: "REVOKED"}},
			want:     true,
			why:      "delivery filters on status ACTIVE, so a revoked row is the same as no row",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsModelTokenHandoff(tc.provider, tc.creds); got != tc.want {
				t.Errorf("needsModelTokenHandoff(%q, %v) = %v, want %v — %s",
					tc.provider, tc.creds, got, tc.want, tc.why)
			}
		})
	}
}

func TestModelTokenCredentialPayload(t *testing.T) {
	got := modelTokenCredentialPayload("CLAUDE_CODE", "sk-ant-oat01-abc")

	// ANTHROPIC_API_KEY, however odd it reads for a value that must be an
	// OAuth token: that is the name `crewship setup` (supportedAdapters) and
	// the wizard (lib/cli-adapters.ts) both use for CLAUDE_CODE, and this
	// handoff has to produce a credential indistinguishable from theirs.
	// seeddata/credentials.go spells it CLAUDE_CODE_OAUTH_TOKEN instead — two
	// conventions live in the tree; matching the onboarding one is what makes
	// autoAssignCredentials link this at deploy time.
	if got["name"] != "ANTHROPIC_API_KEY" {
		t.Errorf("name = %v, want ANTHROPIC_API_KEY (the name onboarding uses for CLAUDE_CODE)", got["name"])
	}
	if got["env_var_name"] != "ANTHROPIC_API_KEY" {
		t.Errorf("env_var_name = %v, want ANTHROPIC_API_KEY", got["env_var_name"])
	}
	// AI_CLI_TOKEN, not API_KEY: onboarding rejects raw API keys, and the
	// two types are delivered differently.
	if got["type"] != "AI_CLI_TOKEN" {
		t.Errorf("type = %v, want AI_CLI_TOKEN", got["type"])
	}
	if got["provider"] != "ANTHROPIC" {
		t.Errorf("provider = %v, want ANTHROPIC", got["provider"])
	}
	if got["value"] != "sk-ant-oat01-abc" {
		t.Errorf("value not carried through")
	}
}

// An adapter the CLI does not know must not be guessed at — silently
// inventing an env var name would store a token under a name no container
// reads, which looks like success and behaves like the bug this fixes.
func TestModelTokenCredentialPayloadUnknownAdapter(t *testing.T) {
	if got := modelTokenCredentialPayload("NOT_AN_ADAPTER", "tok"); got != nil {
		t.Errorf("payload for unknown adapter = %v, want nil", got)
	}
}

// The token the user pastes is the one thing here that must never be
// mistaken for a raw API key: onboarding refuses those server-side, so
// catching it locally saves a round trip and explains the difference.
func TestLooksLikeRawAnthropicAPIKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"sk-ant-api03-xxxx", true},
		{"sk-ant-oat01-xxxx", false},
		{"", false},
		{"  sk-ant-api03-padded  ", true},
	}
	for _, tc := range cases {
		if got := looksLikeRawAnthropicAPIKey(tc.in); got != tc.want {
			t.Errorf("looksLikeRawAnthropicAPIKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
