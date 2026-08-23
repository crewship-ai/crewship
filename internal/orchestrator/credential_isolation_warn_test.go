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
