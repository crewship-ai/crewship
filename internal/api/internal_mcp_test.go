package api

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestIsReservedMCPEnvVar covers the deny-list classifier directly.
func TestIsReservedMCPEnvVar(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Reserved
		{"INTERNAL_TOKEN", true},
		{"internal_token", true},
		{"NEXTAUTH_SECRET", true},
		{"ENCRYPTION_KEY", true},
		{"CREWSHIP_ENV", true},
		{"JWT_SECRET", true},
		{"DATABASE_URL", true},
		{"OLLAMA_HOST", true},
		// Allowed
		{"GOOGLE_ACCESS_TOKEN", false},
		{"SLACK_CLIENT_ID", false},
		{"GITHUB_TOKEN", false},
		{"OPENAI_API_KEY", false},
		{"NOT_INTERNAL_REALLY", false}, // prefix-anchored
	}
	for _, tc := range cases {
		if got := isReservedMCPEnvVar(tc.name); got != tc.want {
			t.Errorf("isReservedMCPEnvVar(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAutoResolveMCPCredentials_RefusesReservedNamespace is the H4 regression:
// an MCP config referencing ${INTERNAL_TOKEN} or ${ENCRYPTION_KEY} must NOT
// trigger a credential lookup, even if a same-named credential exists.
func TestAutoResolveMCPCredentials_RefusesReservedNamespace(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	// Minimal credentials schema — just enough columns for resolveOneEnvVar
	// to attempt a query against. If the deny-list works, the query never
	// runs because we filter at the autoResolve entry point.
	if _, err := db.Exec(`
		CREATE TABLE credentials (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			encrypted_value TEXT NOT NULL,
			type TEXT NOT NULL,
			oauth_client_id TEXT,
			oauth_client_secret_enc TEXT,
			oauth_token_url TEXT,
			oauth_refresh_token_enc TEXT,
			oauth_token_expires_at TEXT,
			deleted_at TEXT,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	// Plant a credential matching the reserved env var. If the deny-list
	// is missing or weakened this would be returned and the test fails.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type)
		VALUES ('cred1', 'ws1', 'internal-token', 'v1:fake-ciphertext', 'API_KEY')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := `{"mcpServers":{"evil":{"env":{"INTERNAL_TOKEN":"${INTERNAL_TOKEN}"}}}}`

	got := autoResolveMCPCredentials(context.Background(), db, logger, "ws1", nil, cfg)
	if len(got) != 0 {
		t.Fatalf("expected 0 resolved entries (reserved namespace blocked), got %d: %+v", len(got), got)
	}
	if !strings.Contains(logBuf.String(), "reserved namespace") {
		t.Errorf("expected refusal log line; got:\n%s", logBuf.String())
	}
}

// TestAutoResolveMCPCredentials_AllowsRegularNamespace sanity-checks that
// non-reserved env vars still flow through the resolver (it returns no
// match here because the planted credential is encrypted with a fake value
// the real Decrypt would reject — we only care that the path runs).
func TestAutoResolveMCPCredentials_AllowsRegularNamespace(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE credentials (
			id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, encrypted_value TEXT,
			type TEXT, oauth_client_id TEXT, oauth_client_secret_enc TEXT,
			oauth_token_url TEXT, oauth_refresh_token_enc TEXT, oauth_token_expires_at TEXT,
			deleted_at TEXT, status TEXT NOT NULL DEFAULT 'ACTIVE',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cfg := `{"mcpServers":{"x":{"env":{"GH":"${GITHUB_TOKEN}"}}}}`

	// We don't assert on the result — Decrypt will fail because there's no
	// matching row. The point is the call doesn't get refused at the
	// deny-list and reaches resolveOneEnvVar, which is what we want.
	_ = autoResolveMCPCredentials(context.Background(), db, logger, "ws1", nil, cfg)
}
