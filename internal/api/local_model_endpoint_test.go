package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// #955 — the local-model endpoint is resolved from the vault as an
// ENDPOINT_URL credential with precedence: per-agent assigned override →
// workspace default → "" (orchestrator then applies the deprecated env
// fallback).

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestResolveLocalModelEndpoint_Precedence(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := discardLogger()

	const wsID = "ws-lm"
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u1', 'u1@ex.com')`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'LM', 'lm')`, wsID)

	wsURL := "http://workspace-default:11434/v1"
	enc, err := encryption.Encrypt(wsURL)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mustExec(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-ws-endpoint', ?, 'ollama-ws', ?, 'ENDPOINT_URL', 'OLLAMA', 'ACTIVE', 'u1')`,
		wsID, enc)

	agentOverride := []mcpCredEntry{
		{ID: "cr-agent", EnvVar: "OLLAMA_BASE_URL", Value: "http://agent-override:11434/v1", Type: CredTypeEndpointURL},
	}

	tests := []struct {
		name     string
		assigned []mcpCredEntry
		want     string
	}{
		{"per-agent override wins over workspace default", agentOverride, "http://agent-override:11434/v1"},
		{"workspace default used when no assigned endpoint", nil, wsURL},
		{"non-endpoint assigned creds ignored, falls to workspace", []mcpCredEntry{{Type: "SECRET", Value: "s"}}, wsURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLocalModelEndpoint(context.Background(), db, logger, wsID, tc.assigned)
			if got != tc.want {
				t.Errorf("resolveLocalModelEndpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveLocalModelEndpoint_NoneConfigured(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := discardLogger()

	const wsID = "ws-empty"
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'E', 'e')`, wsID)

	if got := resolveLocalModelEndpoint(context.Background(), db, logger, wsID, nil); got != "" {
		t.Errorf("expected empty (env fallback), got %q", got)
	}
}

func TestResolveLocalModelEndpoint_SkipsMalformedStored(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := discardLogger()

	const wsID = "ws-bad"
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u1', 'u1@ex.com')`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsID)

	// A row that somehow stored a non-URL must not reach OpenCode's config.
	enc, err := encryption.Encrypt("not a url")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mustExec(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-bad', ?, 'bad', ?, 'ENDPOINT_URL', 'OLLAMA', 'ACTIVE', 'u1')`,
		wsID, enc)

	if got := resolveLocalModelEndpoint(context.Background(), db, logger, wsID, nil); got != "" {
		t.Errorf("expected malformed stored URL skipped, got %q", got)
	}
}

// decryptEndpointURLForRead echoes an ENDPOINT_URL value on read but never a
// secret type's value.
func TestDecryptEndpointURLForRead(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	logger := discardLogger()

	url := "http://host:11434/v1"
	enc, err := encryption.Encrypt(url)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if got := decryptEndpointURLForRead(CredTypeEndpointURL, enc, logger); got == nil || *got != url {
		t.Errorf("ENDPOINT_URL should echo %q, got %v", url, got)
	}
	if got := decryptEndpointURLForRead("SECRET", enc, logger); got != nil {
		t.Errorf("SECRET value must never be echoed on read, got %v", *got)
	}
	if got := decryptEndpointURLForRead(CredTypeEndpointURL, "", logger); got != nil {
		t.Errorf("empty value → nil, got %v", *got)
	}
}
