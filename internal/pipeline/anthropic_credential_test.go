package pipeline

import (
	"context"
	"database/sql"
	"testing"
)

func TestIsAnthropicLLMCredentialType(t *testing.T) {
	cases := map[string]bool{
		"api_key":         true,
		"API_KEY":         true,
		"ai_cli_token":    true,
		"  Ai_Cli_Token ": true,
		"stripe":          false,
		"cli_token":       false,
		"":                false,
	}
	for in, want := range cases {
		if got := IsAnthropicLLMCredentialType(in); got != want {
			t.Errorf("IsAnthropicLLMCredentialType(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestAnthropicLLMCredentialProbe locks the probe to the SAME rows
// providerForWorkspace resolves: an ACTIVE, non-deleted Anthropic credential of
// either accepted type, in the queried workspace. Anything else → not found.
func TestAnthropicLLMCredentialProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("finds API_KEY", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "ANTHROPIC", "API_KEY", "ACTIVE", "sk-ant-x", false)
		assertProbe(t, db, "ws1", true)
	})
	t.Run("finds AI_CLI_TOKEN", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "ANTHROPIC", "AI_CLI_TOKEN", "ACTIVE", "sk-ant-oat-x", false)
		assertProbe(t, db, "ws1", true)
	})
	t.Run("ignores other provider", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "OPENAI", "API_KEY", "ACTIVE", "sk-x", false)
		assertProbe(t, db, "ws1", false)
	})
	t.Run("ignores non-active", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "ANTHROPIC", "API_KEY", "PENDING", "placeholder", false)
		assertProbe(t, db, "ws1", false)
	})
	t.Run("ignores deleted", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "ANTHROPIC", "API_KEY", "ACTIVE", "sk-ant-x", true)
		assertProbe(t, db, "ws1", false)
	})
	t.Run("scoped to workspace", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		insertCredential(t, db, "c1", "ws1", "ANTHROPIC", "API_KEY", "ACTIVE", "sk-ant-x", false)
		assertProbe(t, db, "ws-other", false)
	})
	t.Run("empty workspace errors", func(t *testing.T) {
		db := setupLLMRunnerDB(t)
		if _, err := NewAnthropicLLMCredentialProbe(db)(ctx, ""); err == nil {
			t.Fatal("want error for empty workspace, got nil")
		}
	})
}

func assertProbe(t *testing.T, db *sql.DB, wsID string, want bool) {
	t.Helper()
	got, err := NewAnthropicLLMCredentialProbe(db)(context.Background(), wsID)
	if err != nil {
		t.Fatalf("probe(%q): %v", wsID, err)
	}
	if got != want {
		t.Errorf("probe(%q) = %v, want %v", wsID, got, want)
	}
}
