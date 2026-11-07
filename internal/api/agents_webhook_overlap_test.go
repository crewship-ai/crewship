package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func rotateWithBody(t *testing.T, h *AgentHandler, userID, wsID, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/webhook-secret/rotate", strings.NewReader(body))
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RotateWebhookSecret(rr, r)
	return rr
}

func TestPreviousSecretOpen_Boundaries(t *testing.T) {
	exp := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		raw  string
		now  time.Time
		want bool
	}{
		{"one nanosecond before", exp.Format(time.RFC3339Nano), exp.Add(-time.Nanosecond), true},
		{"at the instant", exp.Format(time.RFC3339Nano), exp, false},
		{"after", exp.Format(time.RFC3339Nano), exp.Add(time.Second), false},
		{"empty", "", exp.Add(-time.Hour), false},
		{"garbage", "tomorrow", exp.Add(-time.Hour), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := previousSecretOpen(tc.raw, tc.now); got != tc.want {
				t.Fatalf("previousSecretOpen = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRotateWebhookSecret_TTLValidation(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"no body keeps today's behaviour", ``, http.StatusOK},
		{"zero", `{"previous_secret_ttl_seconds":0}`, http.StatusOK},
		{"below minimum", `{"previous_secret_ttl_seconds":59}`, http.StatusBadRequest},
		{"minimum", `{"previous_secret_ttl_seconds":60}`, http.StatusOK},
		{"seven days", `{"previous_secret_ttl_seconds":604800}`, http.StatusOK},
		{"over seven days", `{"previous_secret_ttl_seconds":604801}`, http.StatusBadRequest},
		{"negative", `{"previous_secret_ttl_seconds":-1}`, http.StatusBadRequest},
		{"malformed", `{`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedRotateAgent(t, h, wsID, "agent-v", "whsec_old")
			rr := rotateWithBody(t, h, userID, wsID, "agent-v", tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want == http.StatusBadRequest {
				var stored string
				_ = h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id='agent-v'`).Scan(&stored)
				if stored != "whsec_old" {
					t.Fatalf("a refused rotation must not change the secret, got %q", stored)
				}
			}
		})
	}
}

func TestRotateWebhookSecret_Overlap(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, userID, wsID := covAUHandler(t)
	wh := NewWebhookHandler(h.db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)
	seedRotateAgent(t, h, wsID, "agent-o", "whsec_first")
	ctx := context.Background()
	rotate := func(body string) map[string]string {
		t.Helper()
		rr := rotateWithBody(t, h, userID, wsID, "agent-o", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
		}
		var out map[string]string
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}

	// Default: no overlap, old secret is gone at once.
	r0 := rotate(``)
	if _, ok := r0["previous_secret_expires_at"]; ok {
		t.Fatal("no overlap requested, none should be reported")
	}
	if p, _ := wh.lookupPreviousSecret(ctx, "", "agent-o"); p != "" {
		t.Fatalf("default rotation must leave no previous secret, got %q", p)
	}

	// Overlap: the replaced secret is previous, encrypted at rest, never in the response.
	second := r0["webhook_secret"]
	r1 := rotate(`{"previous_secret_ttl_seconds":3600}`)
	if r1["previous_secret_expires_at"] == "" {
		t.Fatal("overlap rotation must report when the old secret stops")
	}
	if got, _ := wh.lookupPreviousSecret(ctx, "", "agent-o"); got != second {
		t.Fatalf("previous secret = %q, want the one just replaced", got)
	}
	for _, v := range r1 {
		if v == second {
			t.Fatal("the previous secret must never be returned")
		}
	}
	var raw string
	_ = h.db.QueryRow(`SELECT previous_webhook_secret FROM agents WHERE id='agent-o'`).Scan(&raw)
	if !encryption.IsEncrypted(raw) {
		t.Fatalf("previous secret must be encrypted at rest, got %q", raw)
	}

	// Double rotation inside the overlap: only the secret just replaced survives.
	third := r1["webhook_secret"]
	r2 := rotate(`{"previous_secret_ttl_seconds":3600}`)
	got, _ := wh.lookupPreviousSecret(ctx, "", "agent-o")
	if got != third || got == second {
		t.Fatalf("after a second rotation previous = %q; want %q and not the older %q", got, third, second)
	}
	_ = r2

	// A rotation without a window ends the overlap.
	rotate(``)
	if p, _ := wh.lookupPreviousSecret(ctx, "", "agent-o"); p != "" {
		t.Fatalf("rotation without a window must clear the previous secret, got %q", p)
	}

	// Expiry is strict by time: an elapsed window yields nothing.
	rotate(`{"previous_secret_ttl_seconds":60}`)
	if _, err := h.db.Exec(`UPDATE agents SET previous_webhook_secret_expires_at=? WHERE id='agent-o'`,
		time.Now().UTC().Add(-time.Nanosecond).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if p, _ := wh.lookupPreviousSecret(ctx, "", "agent-o"); p != "" {
		t.Fatalf("expired previous secret must not be returned, got %q", p)
	}
}

func TestRotateWebhookSecret_OverlapWithNoCurrentSecret(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, userID, wsID := covAUHandler(t)
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('agent-n', ?, 'N', 'agent-n', 'IDLE')`, wsID); err != nil {
		t.Fatal(err)
	}
	if rr := rotateWithBody(t, h, userID, wsID, "agent-n", `{"previous_secret_ttl_seconds":3600}`); rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	var prev *string
	_ = h.db.QueryRow(`SELECT previous_webhook_secret FROM agents WHERE id='agent-n'`).Scan(&prev)
	if prev != nil {
		t.Fatalf("there was no secret to keep, got %q", *prev)
	}
}

func TestRevokePreviousWebhookSecret(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, userID, wsID := covAUHandler(t)
	wh := NewWebhookHandler(h.db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)
	seedRotateAgent(t, h, wsID, "agent-r", "whsec_first")
	if rr := rotateWithBody(t, h, userID, wsID, "agent-r", `{"previous_secret_ttl_seconds":3600}`); rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d", rr.Code)
	}
	if p, _ := wh.lookupPreviousSecret(context.Background(), "", "agent-r"); p != "whsec_first" {
		t.Fatalf("setup: previous = %q", p)
	}
	revoke := func(id, role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "/api/v1/agents/"+id+"/webhook-secret/previous", nil)
		r.SetPathValue("agentId", id)
		r = withWorkspaceUser(r, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.RevokePreviousWebhookSecret(rr, r)
		return rr
	}
	if rr := revoke("agent-r", "VIEWER"); rr.Code != http.StatusForbidden {
		t.Fatalf("viewer revoke = %d, want 403", rr.Code)
	}
	if p, _ := wh.lookupPreviousSecret(context.Background(), "", "agent-r"); p == "" {
		t.Fatal("a refused revoke must leave the overlap open")
	}
	for i := 0; i < 2; i++ { // idempotent
		if rr := revoke("agent-r", "OWNER"); rr.Code != http.StatusNoContent {
			t.Fatalf("revoke = %d %s", rr.Code, rr.Body.String())
		}
	}
	if p, _ := wh.lookupPreviousSecret(context.Background(), "", "agent-r"); p != "" {
		t.Fatalf("revoked previous secret still returned: %q", p)
	}
	if rr := revoke("missing", "OWNER"); rr.Code != http.StatusNotFound && rr.Code != http.StatusForbidden {
		t.Fatalf("unknown agent = %d", rr.Code)
	}
}

func TestAuditPreviousSecretUse_NoSecretInAudit(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	h, _, wsID := covAUHandler(t)
	wh := NewWebhookHandler(h.db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)
	seedRotateAgent(t, h, wsID, "agent-a", "whsec_cur")
	wh.auditPreviousSecretUse(context.Background(), "", "agent-a", "hmac")
	var action, meta string
	if err := h.db.QueryRow(`SELECT action, metadata FROM audit_logs WHERE entity_id='agent-a'`).Scan(&action, &meta); err != nil {
		t.Fatal(err)
	}
	if action != "agent.webhook_previous_secret.used" || strings.Contains(meta, "whsec") {
		t.Fatalf("audit = %q %q", action, meta)
	}
}
