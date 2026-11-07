package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Crew AI suggestions spend a workspace's Anthropic key server-side (#5045).
// Two boundaries hold: a member of workspace A cannot make the server spend
// workspace B's key, and a key a person bound to other hosts is never sent to
// api.anthropic.com.

// Only the victim's workspace holds a key. Naming the victim's workspace is
// refused by the membership fence, and the attacker's own workspace answers
// "no key", so the victim's key was never selected.
func TestCrewAISuggest_CannotSpendAnotherWorkspacesKey(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "a")
	victim := fenceSeedTenant(t, db, "b")
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-anthropic-victim', ?, 'victim anthropic', 'enc', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		victim.wsID, victim.userID)
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	post := func(target, header string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, strings.NewReader(`{"description":"a crew that reviews pull requests"}`))
		req.Header.Set("Authorization", "Bearer "+attacker.token)
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("X-Workspace-ID", header)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		name   string
		target string
		header string
	}{
		{"victim workspace in header", "/api/v1/crew-ai-suggest", victim.wsID},
		{"victim workspace in query", "/api/v1/crew-ai-suggest?workspace_id=" + victim.wsID, ""},
		{"victim in query, own in header", "/api/v1/crew-ai-suggest?workspace_id=" + victim.wsID, attacker.wsID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := post(tc.target, tc.header)
			if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 403 or 404 before any key is read: %s", rr.Code, fenceTrim(rr.Body.String()))
			}
		})
	}
	rr := post("/api/v1/crew-ai-suggest", attacker.wsID)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("own workspace status = %d, want 422 (no key here, so the victim's was not used): %s", rr.Code, fenceTrim(rr.Body.String()))
	}
}

func TestCrewAISuggest_HostBoundKeyIsNotSentToTheProvider(t *testing.T) {
	ensureEncryptionKey(t)
	sealed, err := encryption.Encrypt("sk-ant-api-test")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		hosts   *string
		wantKey bool
	}{
		{"empty list", strPtr("[]"), true},
		{"approves the provider host", strPtr(`["api.anthropic.com"]`), true},
		{"approves a wildcard covering it", strPtr(`["*.anthropic.com"]`), true},
		{"bound to another host", strPtr(`["llm-proxy.example.com"]`), false},
		{"unreadable list", strPtr(`not json`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			execOrFatal(t, db, `INSERT INTO credentials
				(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, allowed_hosts, created_at, updated_at)
				VALUES ('cred-a', ?, 'anthropic', ?, 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, ?, datetime('now'), datetime('now'))`,
				wsID, sealed, userID, tc.hosts)
			p, err := NewCrewAIHandler(db, newTestLogger()).getLLMProvider(context.Background(), wsID)
			if tc.wantKey {
				if err != nil || p == nil {
					t.Fatalf("getLLMProvider = %v, %v; want a provider", p, err)
				}
				return
			}
			if !errors.Is(err, errNoActiveAnthropicCredential) {
				t.Fatalf("getLLMProvider err = %v, want errNoActiveAnthropicCredential", err)
			}
		})
	}
}
