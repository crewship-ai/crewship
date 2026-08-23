package orchestrator

import (
	"context"
	"log/slog"
	"testing"
)

// countingHandler counts Warn-level records so the test can assert the
// fail-open notice fires at most once per process, matching the sync.Once
// guard other one-time deprecation warnings in this file use.
type countingHandler struct {
	warns *int
}

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.warns++
	}
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

func TestWarnCredentialIsolationFailOpenOnce_FiresOnce(t *testing.T) {
	var warns int
	o := &Orchestrator{logger: slog.New(countingHandler{warns: &warns})}

	o.warnCredentialIsolationFailOpenOnce()
	o.warnCredentialIsolationFailOpenOnce()
	o.warnCredentialIsolationFailOpenOnce()

	if warns != 1 {
		t.Fatalf("warnCredentialIsolationFailOpenOnce logged %d times across 3 calls, want exactly 1", warns)
	}
}

func TestConfigFingerprint_TriggersFailOpenWarningOnlyWhenCredentialsRouted(t *testing.T) {
	creds := []Credential{{ID: "cred-1", Provider: "OPENROUTER", PlainValue: "sk-test"}}

	// No internal token configured: sidecarConfigFingerprint must return "" so
	// the caller in orchestrator_run.go knows to warn (see startSidecar).
	if fp := sidecarConfigFingerprint("", creds); fp != "" {
		t.Fatalf("sidecarConfigFingerprint with empty master = %q, want empty", fp)
	}

	// With an internal token configured, isolation is intact and no warning
	// condition should be signaled.
	if fp := sidecarConfigFingerprint("internal-master", creds); fp == "" {
		t.Fatalf("sidecarConfigFingerprint with a configured master returned empty; fail-open warning would fire even though isolation is intact")
	}
}

// The isolation alarm must name a real exposure, and must not miss one.
//
// The unroutable cases are the ones a len(creds) gate gets wrong:
// buildSidecarCreds drops every credential credTypeToProvider does not route, so
// a run carrying only a GitHub PAT or an agent's own SECRET has nothing loaded in
// the CredStore for a concurrent agent to reach. Warning there sends an operator
// hunting a credential-isolation gap that does not exist, which is how a real
// alarm stops being read.
func TestCredentialIsolationFailedOpen(t *testing.T) {
	routed := Credential{ID: "or", EnvVarName: "OPENROUTER_API_KEY", Type: "API_KEY", Provider: "OPENROUTER", PlainValue: "sk-or-example"}
	ghPAT := Credential{ID: "gh", EnvVarName: "GITHUB_TOKEN", Type: "CLI_TOKEN", Provider: "GITHUB", PlainValue: "ghp_example"}
	secret := Credential{ID: "sec", EnvVarName: "MY_SECRET", Type: "SECRET", Provider: "NONE", PlainValue: "s3cret-value"}

	tests := []struct {
		name        string
		fingerprint string
		creds       []Credential
		want        bool
		why         string
	}{
		{
			name:  "no token and a routed credential is the real exposure",
			creds: []Credential{routed}, want: true,
			why: "an unfingerprinted sidecar holds a provider key every agent in the crew container can reach",
		},
		{
			name:  "unroutable credentials alone must stay quiet",
			creds: []Credential{ghPAT, secret}, want: false,
			why: "nothing reaches the CredStore, so there is no credential to confuse between agents",
		},
		{
			name:  "a routed credential among unroutable ones still warns",
			creds: []Credential{ghPAT, secret, routed}, want: true,
			why: "filtering the noise must not silence the case the warning exists for",
		},
		{
			name:  "no credentials at all",
			creds: nil, want: false,
			why: "an empty run has nothing to isolate",
		},
		{
			name:        "a configured fingerprint means isolation is intact",
			fingerprint: "abcdef123456", creds: []Credential{routed}, want: false,
			why: "authorizeLLMRoute enforces the route token whenever a fingerprint exists",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialIsolationFailedOpen(tc.fingerprint, tc.creds); got != tc.want {
				t.Errorf("credentialIsolationFailedOpen(%q, %d creds) = %v, want %v — %s",
					tc.fingerprint, len(tc.creds), got, tc.want, tc.why)
			}
		})
	}
}
