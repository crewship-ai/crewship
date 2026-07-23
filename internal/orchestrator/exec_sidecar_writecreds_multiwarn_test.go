package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// Adversarial follow-up for #1364 R1: with several credentials of mixed types
// under Keeper, EVERY withheld SECRET must be named at WARN, non-SECRET file
// creds must NOT appear in a withhold warn (they are delivered, not withheld),
// and no credential VALUE may ever be logged.
func TestWriteCredentialFiles_WarnsEachSecretOnlyUnderKeeper(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	fake := &credExecFake{}
	creds := []Credential{
		{ID: "1", EnvVarName: "SECRET_A", PlainValue: "aaa-val", Type: "SECRET"},
		{ID: "2", EnvVarName: "SECRET_B", PlainValue: "bbb-val", Type: "SECRET"},
		{ID: "3", EnvVarName: "GH_TOKEN", PlainValue: "ghp-val", Type: "CLI_TOKEN"},
		{ID: "4", EnvVarName: "STRIPE_HOOK", PlainValue: "whsec-val", Type: "GENERIC_SECRET"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", true, logger); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}

	out := buf.String()
	for _, name := range []string{"SECRET_A", "SECRET_B"} {
		if !strings.Contains(out, name) {
			t.Errorf("every withheld SECRET must be named at WARN; missing %q in:\n%s", name, out)
		}
	}
	// Non-SECRET file creds are delivered, not withheld — they must not appear
	// in a withhold WARN line.
	for _, name := range []string{"GH_TOKEN", "STRIPE_HOOK"} {
		if strings.Contains(out, name) {
			t.Errorf("non-SECRET %q must not be reported as withheld; got:\n%s", name, out)
		}
	}
	// No value, of any credential, may ever be logged.
	for _, val := range []string{"aaa-val", "bbb-val", "ghp-val", "whsec-val"} {
		if strings.Contains(out, val) {
			t.Errorf("credential value %q leaked into logs:\n%s", val, out)
		}
	}
}
