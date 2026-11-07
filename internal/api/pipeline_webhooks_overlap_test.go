package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func patchWebhook(t *testing.T, h *PipelineHandler, userID, wsID, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+id, strings.NewReader(body)), userID, wsID, "OWNER")
	req.SetPathValue("webhookId", id)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	return rr
}

func TestUpdateWebhook_OverlapWindowBounds(t *testing.T) {
	tests := []struct {
		body string
		want int
	}{
		{`{"rotate_secret":true,"rotate_grace_seconds":59}`, http.StatusBadRequest},
		{`{"rotate_secret":true,"rotate_grace_seconds":60}`, http.StatusOK},
		{`{"rotate_secret":true,"rotate_grace_seconds":129600}`, http.StatusOK}, // 36h, not a preset
		{`{"rotate_secret":true,"rotate_grace_seconds":604801}`, http.StatusBadRequest},
		{`{"rotate_secret":true,"rotate_grace_seconds":-5}`, http.StatusBadRequest},
		{`{"rotate_secret":true,"revoke_previous_secret":true}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.body, func(t *testing.T) {
			h, db, userID, wsID := webhookHandlerRig(t)
			seedWebhookPipeline(t, db, wsID, "pw-b", "b-pipe")
			wh := seedWebhookRow(t, db, wsID, "pw-b", "s1", true)
			if rr := patchWebhook(t, h, userID, wsID, wh.ID, tc.body); rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestUpdateWebhook_RevokePreviousSecret(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pw-rv", "rv-pipe")
	wh := seedWebhookRow(t, db, wsID, "pw-rv", "s1", true)
	if rr := patchWebhook(t, h, userID, wsID, wh.ID, `{"rotate_secret":true,"rotate_grace_seconds":3600}`); rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	store := pipeline.NewWebhookStore(db)
	cur, _ := store.GetByID(t.Context(), wh.ID)
	if ok, prev := cur.MatchSignature([]byte("x"), covPSWSign("s1", "x"), time.Now()); !ok || !prev {
		t.Fatal("setup: the old secret should verify inside the overlap")
	}
	rr := patchWebhook(t, h, userID, wsID, wh.ID, `{"revoke_previous_secret":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "s1") || strings.Contains(rr.Body.String(), "previous_secret_expires_at") {
		t.Fatalf("revoke response leaks or still reports an overlap: %s", rr.Body.String())
	}
	cur, _ = store.GetByID(t.Context(), wh.ID)
	if ok, _ := cur.MatchSignature([]byte("x"), covPSWSign("s1", "x"), time.Now()); ok {
		t.Fatal("revoked secret still verifies")
	}
	if !cur.ValidateSignature([]byte("x"), covPSWSign(cur.SigningSecret, "x")) {
		t.Fatal("the current secret must keep working after a revoke")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='webhook.previous_secret.revoked' AND entity_id=?`, wh.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("revoke audit rows = %d, want 1", n)
	}
}

func TestFireWebhook_PreviousSecretUseIsAudited(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pw-au", "au-pipe")
	wh := seedWebhookRow(t, db, wsID, "pw-au", "old-secret", true)
	if rr := patchWebhook(t, h, userID, wsID, wh.ID, `{"rotate_secret":true,"rotate_grace_seconds":3600}`); rr.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	cur, _ := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	fire := func(secret, body string) int {
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", covPSWSign(secret, body))
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		return rr.Code
	}
	count := func() int {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='webhook.previous_secret.used' AND entity_id=?`, wh.ID).Scan(&n)
		return n
	}
	if c := fire(cur.SigningSecret, `{"n":1}`); c != http.StatusAccepted || count() != 0 {
		t.Fatalf("current secret: status %d, audit rows %d (want 202, 0)", c, count())
	}
	if c := fire("old-secret", `{"n":2}`); c != http.StatusAccepted || count() != 1 {
		t.Fatalf("previous secret: status %d, audit rows %d (want 202, 1)", c, count())
	}
	var meta string
	_ = db.QueryRow(`SELECT metadata FROM audit_logs WHERE action='webhook.previous_secret.used'`).Scan(&meta)
	if strings.Contains(meta, "old-secret") {
		t.Fatalf("audit metadata leaks the secret: %s", meta)
	}
	waitPendingWebhookDispatches(t, h)
}
