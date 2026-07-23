package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// #1364 R1: withholding a SECRET from file delivery under Keeper must not be
// SILENT. The env path already surfaces credential exposures
// (AgentEnvCredentialExposures -> WARN in orchestrator_run.go) and the MCP path
// warns on withhold (exec_env.go). The file path skipped the SECRET with a bare
// `continue`, leaving no audit trace that a documented isolation gate fired.
// This is the "no silent path" doctrine gap the issue names.

func TestWriteCredentialFiles_LogsSecretWithheldUnderKeeper(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	fake := &credExecFake{}
	creds := []Credential{
		{ID: "c1", EnvVarName: "WEBHOOK_SECRET", PlainValue: "shhh", Type: "SECRET"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", true, logger); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "WEBHOOK_SECRET") {
		t.Errorf("Keeper ON: withholding a SECRET file must emit a WARN naming the env var; got:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("the withhold must be observable at WARN, not silent; got:\n%s", out)
	}
	if strings.Contains(out, "shhh") {
		t.Errorf("the withhold log must NEVER contain the secret value; got:\n%s", out)
	}
}

// Keeper OFF: the SECRET is delivered as a file (legacy behaviour) and there is
// nothing withheld, so no withhold WARN should fire.
func TestWriteCredentialFiles_NoWithholdWarnWhenKeeperOff(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	fake := &credExecFake{}
	creds := []Credential{
		{ID: "c1", EnvVarName: "WEBHOOK_SECRET", PlainValue: "shhh", Type: "SECRET"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", false, logger); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}
	if strings.Contains(buf.String(), "withheld") {
		t.Errorf("Keeper OFF: nothing is withheld, so no withhold WARN should fire; got:\n%s", buf.String())
	}
}
