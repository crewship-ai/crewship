package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ---------------------------------------------------------------------------
// onboarding.go — validateOnboardingCredential + insertOnboardingCredential.
//
// These run on every onboarding submit; the shape gate explicitly
// rejects the most common foot-gun (raw Anthropic API key when an
// OAuth token is expected). Tests cover all shape branches with the
// live HTTP probe disabled via skipTokenProbe — see onboarding_probe_test.go
// for the gate contract itself.
// ---------------------------------------------------------------------------

func withTokenProbeSkipped(t *testing.T) {
	t.Helper()
	orig := skipTokenProbe
	t.Cleanup(func() { skipTokenProbe = orig })
	skipTokenProbe = true
}

// ---- validateOnboardingCredential ----

func TestValidateOnboardingCredential_EmptyValue_NoError(t *testing.T) {
	// Source: "Empty value is allowed — pair mode and users who skip
	// credential setup land here without a value to check." Pin that
	// the empty path bypasses both shape and probe checks.
	if err := validateOnboardingCredential(context.Background(), "ANTHROPIC", ""); err != nil {
		t.Errorf("err = %v, want nil for empty value", err)
	}
	// Whitespace-only should also count as empty after TrimSpace.
	if err := validateOnboardingCredential(context.Background(), "ANTHROPIC", "   \t  "); err != nil {
		t.Errorf("err = %v, want nil for whitespace-only value", err)
	}
}

func TestValidateOnboardingCredential_AnthropicAPIKey_RejectedWithFixItHint(t *testing.T) {
	// The whole point of this gate: a user pastes the wrong thing from
	// console.anthropic.com. Reject loudly with a pointer to the right
	// flow. The error string is rendered verbatim in the onboarding UI.
	withTokenProbeSkipped(t)
	err := validateOnboardingCredential(context.Background(), "ANTHROPIC", "sk-ant-api-XXXXXXXX")
	if err == nil {
		t.Fatal("expected error for raw API key shape")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Claude Code CLI token") {
		t.Errorf("err = %q, want mention of \"Claude Code CLI token\"", msg)
	}
	if !strings.Contains(msg, "claude setup-token") {
		t.Errorf("err = %q, want pointer to `claude setup-token`", msg)
	}
}

func TestValidateOnboardingCredential_WrongPrefix_RejectedWithFixItHint(t *testing.T) {
	// Any non-OAuth, non-API-key shape (e.g. a github_pat_..., a bare
	// uuid, or an OpenAI key) hits the "doesn't look like a Claude Code
	// CLI token" branch with the same fix-it pointer.
	withTokenProbeSkipped(t)
	cases := []string{
		"github_pat_abcdefghij",
		"sk-openai-XXXXXXXX",
		"random-string-no-prefix",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			err := validateOnboardingCredential(context.Background(), "ANTHROPIC", v)
			if err == nil {
				t.Fatalf("expected error for %q", v)
			}
			if !strings.Contains(err.Error(), "sk-ant-oat") {
				t.Errorf("err = %q, want mention of expected `sk-ant-oat` prefix", err)
			}
		})
	}
}

func TestValidateOnboardingCredential_ValidOAuthShape_PassesProbeGate(t *testing.T) {
	// sk-ant-oat-prefixed tokens pass the shape check; with the probe
	// gate disabled they should return nil (the probe would otherwise
	// hit api.anthropic.com).
	withTokenProbeSkipped(t)
	err := validateOnboardingCredential(context.Background(), "ANTHROPIC", "sk-ant-oat01-real-shape-fake-value")
	if err != nil {
		t.Errorf("err = %v, want nil for valid OAuth shape", err)
	}
}

func TestValidateOnboardingCredential_EmptyProviderDefaultsToAnthropic(t *testing.T) {
	// Source comment: "Empty/unset provider defaults to ANTHROPIC — same
	// default that resolveLLMProvider applies on the persistence side."
	// Pin that an empty-provider raw-API-key still trips the gate
	// (without the default a non-Anthropic provider would let it
	// through, leading to a silent broken-chat regression).
	withTokenProbeSkipped(t)
	err := validateOnboardingCredential(context.Background(), "", "sk-ant-api-XXX")
	if err == nil {
		t.Fatal("expected the API-key gate to fire even with empty provider (defaults to ANTHROPIC)")
	}
	if !strings.Contains(err.Error(), "Claude Code CLI token") {
		t.Errorf("err = %q, want Anthropic-specific message", err)
	}
}

func TestValidateOnboardingCredential_ProviderCaseInsensitive(t *testing.T) {
	withTokenProbeSkipped(t)
	for _, p := range []string{"anthropic", "Anthropic", "  ANTHROPIC  "} {
		t.Run(p, func(t *testing.T) {
			err := validateOnboardingCredential(context.Background(), p, "sk-ant-api-XXX")
			if err == nil {
				t.Errorf("provider=%q: expected API-key gate to fire (case + trim)", p)
			}
		})
	}
}

func TestValidateOnboardingCredential_UnknownProvider_FallsThrough(t *testing.T) {
	// Source: "Per-provider checks are intentionally narrow: we only
	// reject shapes we know will fail downstream, never anything
	// ambiguous. A future adapter whose token shape we don't yet
	// recognise falls through."
	withTokenProbeSkipped(t)
	err := validateOnboardingCredential(context.Background(), "FUTURE_PROVIDER", "anything-shaped")
	if err != nil {
		t.Errorf("err = %v, want nil for unknown provider (fall-through)", err)
	}
}

// ---- insertOnboardingCredential ----

func TestInsertOnboardingCredential_InsertsEncryptedRow(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	now := time.Now().UTC().Format(time.RFC3339)
	err := insertOnboardingCredential(
		context.Background(), db,
		userID, wsID,
		"ANTHROPIC_API_KEY", "ANTHROPIC", "ANTHROPIC_API_KEY",
		"sk-ant-oat01-actual-token-bytes", now,
	)
	if err != nil {
		t.Fatalf("insertOnboardingCredential: %v", err)
	}

	var encrypted, name, provider, credType, scope string
	if err := db.QueryRow(`SELECT encrypted_value, name, provider, type, scope FROM credentials WHERE workspace_id = ?`,
		wsID).Scan(&encrypted, &name, &provider, &credType, &scope); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if name != "ANTHROPIC_API_KEY" {
		t.Errorf("name = %q", name)
	}
	if provider != "ANTHROPIC" {
		t.Errorf("provider = %q", provider)
	}
	if credType != "AI_CLI_TOKEN" {
		t.Errorf("type = %q, want AI_CLI_TOKEN (onboarding always inserts as OAuth-shape token)", credType)
	}
	if scope != "WORKSPACE" {
		t.Errorf("scope = %q, want WORKSPACE (onboarding-issued creds are workspace-wide)", scope)
	}
	// Encrypted value must NOT be the plaintext.
	if encrypted == "sk-ant-oat01-actual-token-bytes" {
		t.Error("encrypted_value stored as plaintext; encryption.Encrypt was bypassed")
	}
	// And it must decrypt back to the original.
	plain, err := encryption.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "sk-ant-oat01-actual-token-bytes" {
		t.Errorf("decrypted = %q, want round-trip identity", plain)
	}
}

func TestInsertOnboardingCredential_NameUniqueConstraint(t *testing.T) {
	// credentials table has UNIQUE(workspace_id, name). Inserting twice
	// with the same name in the same workspace must surface the SQL
	// error rather than silently succeed (UI's "credential already
	// configured" prompt depends on the error).
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	now := time.Now().UTC().Format(time.RFC3339)

	if err := insertOnboardingCredential(context.Background(), db,
		userID, wsID, "DUP", "ANTHROPIC", "X", "v1", now); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := insertOnboardingCredential(context.Background(), db,
		userID, wsID, "DUP", "ANTHROPIC", "X", "v2", now)
	if err == nil {
		t.Error("second insert with duplicate name succeeded; UNIQUE constraint should have fired")
	}
	if err != nil && !strings.Contains(err.Error(), "insert credential") {
		t.Errorf("err = %v, want wrapped \"insert credential\" prefix", err)
	}
}

func TestInsertOnboardingCredential_AcceptsBlankEnvVarName(t *testing.T) {
	// Source signature has an `_ /*envVarName*/` parameter — currently
	// unused. Pin that passing "" still inserts successfully (a future
	// "must be non-empty" check would surface here AND break legacy
	// onboarding flows that don't pass it).
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	now := time.Now().UTC().Format(time.RFC3339)

	if err := insertOnboardingCredential(context.Background(), db,
		userID, wsID, "BLANK_ENV", "ANTHROPIC", "", "v", now); err != nil {
		t.Errorf("err = %v, want nil (empty env-var-name currently allowed)", err)
	}
}

// ---- stringPtr (already 100% covered but the empty branch is worth a
//      sanity check in this file so it sits next to its caller logic) ----

func TestStringPtr_EmptyReturnsNil(t *testing.T) {
	if stringPtr("") != nil {
		t.Error("stringPtr(\"\") should return nil (downstream JSON-omitempty path)")
	}
	got := stringPtr("hello")
	if got == nil || *got != "hello" {
		t.Errorf("stringPtr(\"hello\") = %v, want pointer to \"hello\"", got)
	}
}
