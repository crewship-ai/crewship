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

// escWaitAuthzRig seeds two workspaces, three crews and the escalations the
// binding table below waits on. Everything is inserted directly rather than
// through seedTestWorkspace because the cross-tenant cases need a SECOND
// workspace, and that helper hard-codes a single id.
//
// Layout:
//
//	ws-a ── crew-a1 ── esc-a1        RESOLVED, TEXT
//	     └─ crew-a2 ── esc-a2        RESOLVED, TEXT     (sibling crew)
//	     └─ crew-a1 ── esc-a1-pend   PENDING, TEXT
//	ws-b ── crew-b1 ── esc-b1        RESOLVED, CREDENTIAL (encrypted secret)
func escWaitAuthzRig(t *testing.T) (*QueryHandler, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)

	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES ('u-a', 'a@example.com', 'A')`)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-a', 'WS A', 'ws-a')`)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-b', 'WS B', 'ws-b')`)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-a', 'ws-a', 'u-a', 'OWNER')`)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a1', 'ws-a', 'A1', 'a1')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a2', 'ws-a', 'A2', 'a2')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b1', 'ws-b', 'B1', 'b1')`)

	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-a1', 'crew-a1', 'ws-a', 'Aa', 'aa')`)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-a2', 'crew-a2', 'ws-a', 'Ab', 'ab')`)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-b1', 'crew-b1', 'ws-b', 'Ba', 'ba')`)

	secret, err := encryption.Encrypt(escWaitAuthzSecret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, resolution, action, resolved_at, created_at)
		 VALUES ('esc-a1', 'ws-a', 'crew-a1', 'chat-a1', 'ag-a1', 'A1 needs help', 'TEXT', 'RESOLVED', 'a1-answer', 'approve', '2026-01-01T01:00:00Z', '2026-01-01T00:00:00Z')`)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, resolution, action, resolved_at, created_at)
		 VALUES ('esc-a2', 'ws-a', 'crew-a2', 'chat-a2', 'ag-a2', 'A2 needs help', 'TEXT', 'RESOLVED', 'a2-answer', 'approve', '2026-01-01T01:00:00Z', '2026-01-01T00:00:00Z')`)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, created_at)
		 VALUES ('esc-a1-pend', 'ws-a', 'crew-a1', 'chat-a1', 'ag-a1', 'A1 pending', 'TEXT', 'PENDING', '2026-01-01T00:00:00Z')`)
	execOrFatal(t, db,
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status, resolution, action, resolved_at, created_at)
		 VALUES ('esc-b1', 'ws-b', 'crew-b1', 'chat-b1', 'ag-b1', 'B1 needs a key', 'CREDENTIAL', 'RESOLVED', ?, 'approve', '2026-01-01T01:00:00Z', '2026-01-01T00:00:00Z')`,
		secret)

	return NewQueryHandler(db, nil, nil, "token", newTestLogger()), secret
}

// escWaitAuthzSecret is the plaintext behind the ws-b CREDENTIAL escalation.
// A cross-tenant caller must never see this string in a response body.
const escWaitAuthzSecret = "sk-live-ws-b-must-never-leak"

// escWaitAuthzCall drives WaitForEscalationResponse the way the internal
// router does, with the token bindings requireInternal would have placed in
// context. Empty tokenWS + empty tokenCrew is the master-token caller.
func escWaitAuthzCall(t *testing.T, h *QueryHandler, escID, tokenWS, tokenCrew string) (int, string) {
	t.Helper()
	// A deadline so a handler that (wrongly) blocks on a foreign PENDING
	// escalation fails the assertion instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if tokenWS != "" {
		ctx = context.WithValue(ctx, ctxInternalTokenWS, tokenWS)
	}
	if tokenCrew != "" {
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, tokenCrew)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/escalations/"+escID+"/wait", nil).WithContext(ctx)
	req.SetPathValue("escalationId", escID)
	rec := httptest.NewRecorder()
	h.WaitForEscalationResponse(rec, req)
	return rec.Code, rec.Body.String()
}

// TestWaitForEscalationResponse_TokenBindingScopesLookup is the authorization
// regression for the escalation long-poll. The handler used to load the
// escalation by id alone and hand back its (decrypted, for CREDENTIAL)
// resolution, so any crew sidecar could poll any escalation id in any
// workspace. It must now scope the lookup to the caller's token binding the
// same way CreateEscalation does, and refuse with 404 so the endpoint is not
// an existence oracle for other tenants' ids.
func TestWaitForEscalationResponse_TokenBindingScopesLookup(t *testing.T) {
	tests := []struct {
		name       string
		escID      string
		tokenWS    string
		tokenCrew  string
		wantStatus int
		wantBody   string // substring that must appear
	}{
		{
			name:       "crew-bound caller reads its own escalation",
			escID:      "esc-a1",
			tokenWS:    "ws-a",
			tokenCrew:  "crew-a1",
			wantStatus: http.StatusOK,
			wantBody:   "a1-answer",
		},
		{
			name:       "crew-bound caller cannot read a sibling crew's escalation",
			escID:      "esc-a2",
			tokenWS:    "ws-a",
			tokenCrew:  "crew-a1",
			wantStatus: http.StatusNotFound,
			wantBody:   "escalation not found",
		},
		{
			name:       "crew-bound caller cannot cross workspaces",
			escID:      "esc-b1",
			tokenWS:    "ws-a",
			tokenCrew:  "crew-a1",
			wantStatus: http.StatusNotFound,
			wantBody:   "escalation not found",
		},
		{
			name:       "workspace-bound caller reads an escalation in its own workspace",
			escID:      "esc-a1",
			tokenWS:    "ws-a",
			wantStatus: http.StatusOK,
			wantBody:   "a1-answer",
		},
		{
			name:       "workspace-bound caller cannot cross workspaces",
			escID:      "esc-b1",
			tokenWS:    "ws-b-not-mine",
			wantStatus: http.StatusNotFound,
			wantBody:   "escalation not found",
		},
		{
			name:       "master token stays unrestricted",
			escID:      "esc-b1",
			wantStatus: http.StatusOK,
			// #2376: even the unrestricted caller is answered with the
			// decision, never the stored ciphertext's plaintext — the value
			// is not on this path for anyone.
			wantBody: `"status":"RESOLVED"`,
		},
		{
			name:       "unknown id looks the same as a foreign id",
			escID:      "esc-does-not-exist",
			tokenWS:    "ws-a",
			tokenCrew:  "crew-a1",
			wantStatus: http.StatusNotFound,
			wantBody:   "escalation not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := escWaitAuthzRig(t)
			code, body := escWaitAuthzCall(t, h, tc.escID, tc.tokenWS, tc.tokenCrew)
			if code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", code, tc.wantStatus, body)
			}
			if !strings.Contains(body, tc.wantBody) {
				t.Errorf("body = %s, want it to contain %q", body, tc.wantBody)
			}
			// No answer, refused or granted, carries the plaintext: a refusal
			// must not leak the other tenant's secret, and an answer (#2376)
			// is a decision plus a handle, never the value.
			if strings.Contains(body, escWaitAuthzSecret) {
				t.Errorf("CROSS-TENANT LEAK: refusal body carried ws-b's credential plaintext: %s", body)
			}
			// The waiter registered before the DB read must always be
			// unregistered on the way out, refusal path included.
			h.escalationMu.Lock()
			n := len(h.escalationWaiters)
			h.escalationMu.Unlock()
			if n != 0 {
				t.Errorf("waiter map has %d entries after the handler returned, want 0 (deferred removeEscalationWaiter did not run)", n)
			}
		})
	}
}

// TestWaitForEscalationResponse_ForeignPendingRefusedNotBlocked pins the
// second half of the leak: a foreign PENDING escalation must be refused
// immediately rather than long-polled, otherwise the sidecar sits on the
// channel and collects the resolution the moment a human answers it — the
// decryption happens on the resolve path too.
func TestWaitForEscalationResponse_ForeignPendingRefusedNotBlocked(t *testing.T) {
	h, _ := escWaitAuthzRig(t)

	start := time.Now()
	code, body := escWaitAuthzCall(t, h, "esc-a1-pend", "ws-a", "crew-a2")
	elapsed := time.Since(start)

	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", code, body)
	}
	if !strings.Contains(body, "escalation not found") {
		t.Errorf("body = %s, want the canonical not-found message", body)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("handler blocked for %s on a foreign PENDING escalation — it long-polled instead of refusing", elapsed)
	}

	// And a crew-bound caller must still be able to notify-and-receive on
	// its OWN pending escalation: scoping must not break the wakeup path.
	got := make(chan map[string]interface{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, ctxInternalTokenWS, "ws-a")
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, "crew-a1")
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/escalations/esc-a1-pend/wait", nil).WithContext(ctx)
		req.SetPathValue("escalationId", "esc-a1-pend")
		rec := httptest.NewRecorder()
		h.WaitForEscalationResponse(rec, req)
		var out map[string]interface{}
		json.Unmarshal(rec.Body.Bytes(), &out)
		out["__code"] = float64(rec.Code)
		got <- out
	}()

	time.Sleep(100 * time.Millisecond)
	h.notifyEscalationWaiter("esc-a1-pend", escalationResult{Resolution: "go ahead", Action: "approve"})

	select {
	case out := <-got:
		if out["__code"] != float64(http.StatusOK) {
			t.Fatalf("own pending wait returned %v, want 200: %v", out["__code"], out)
		}
		if out["resolution"] != "go ahead" {
			t.Errorf("resolution = %v, want 'go ahead'", out["resolution"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("own-crew waiter never woke up — the wakeup registration was broken by scoping")
	}
}
