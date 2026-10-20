package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ODCH-INT-11: rotating a trigger's signing secret can leave the old one valid
// for a while, so a sender can be switched over without a window of 401s.
func TestUpdateWebhook_RotateWithGrace(t *testing.T) {
	const body = `{"event":"deploy"}`
	tests := []struct {
		name        string
		request     string
		wantCode    int
		oldWorks    bool // right after the call
		oldAfter    bool // after the grace window has passed
		wantExpires bool
	}{
		{"no grace: the old secret stops now", `{"rotate_secret":true}`, http.StatusOK, false, false, false},
		{"explicit zero grace is the same", `{"rotate_secret":true,"rotate_grace_seconds":0}`, http.StatusOK, false, false, false},
		{"one hour: old works, then stops", `{"rotate_secret":true,"rotate_grace_seconds":3600}`, http.StatusOK, true, false, true},
		{"seven days is allowed", `{"rotate_secret":true,"rotate_grace_seconds":604800}`, http.StatusOK, true, false, true},
		{"an odd window is refused", `{"rotate_secret":true,"rotate_grace_seconds":5}`, http.StatusBadRequest, true, true, false},
		{"grace without a rotation is refused", `{"rotate_grace_seconds":3600}`, http.StatusBadRequest, true, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := webhookHandlerRig(t)
			seedWebhookPipeline(t, db, wsID, "pw-grace", "grace-pipe")
			wh := seedWebhookRow(t, db, wsID, "pw-grace", "original-secret", true)
			req := withWorkspaceUser(httptest.NewRequest("PATCH",
				"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(tc.request)),
				userID, wsID, "OWNER")
			req.SetPathValue("webhookId", wh.ID)
			rr := httptest.NewRecorder()
			h.UpdateWebhook(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "original-secret") {
				t.Fatal("the response must never carry the previous secret")
			}
			if tc.wantExpires && !strings.Contains(rr.Body.String(), "previous_secret_expires_at") {
				t.Errorf("the response should say when the old secret stops: %s", rr.Body.String())
			}
			if !tc.wantExpires && strings.Contains(rr.Body.String(), "previous_secret_expires_at") {
				t.Errorf("no grace, so no expiry should be reported: %s", rr.Body.String())
			}

			store := pipeline.NewWebhookStore(db)
			cur, err := store.GetByID(t.Context(), wh.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := cur.ValidateSignature([]byte(body), covPSWSign("original-secret", body)); got != tc.oldWorks {
				t.Errorf("old secret valid = %v, want %v", got, tc.oldWorks)
			}
			if tc.wantCode == http.StatusOK && !cur.ValidateSignature([]byte(body), covPSWSign(cur.SigningSecret, body)) {
				t.Error("the new secret must always work")
			}

			// Let the window pass.
			if _, err := db.Exec(`UPDATE pipeline_webhooks SET previous_secret_expires_at = ? WHERE id = ? AND previous_secret_expires_at IS NOT NULL`,
				time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), wh.ID); err != nil {
				t.Fatal(err)
			}
			cur, _ = store.GetByID(t.Context(), wh.ID)
			if got := cur.ValidateSignature([]byte(body), covPSWSign("original-secret", body)); got != tc.oldAfter {
				t.Errorf("old secret valid after the window = %v, want %v", got, tc.oldAfter)
			}
		})
	}
}

// The timestamped scheme honours the window too, and a second rotation drops
// the secret before last.
func TestWebhookGrace_TimestampedAndSecondRotation(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pw-grace2", "grace-pipe-2")
	wh := seedWebhookRow(t, db, wsID, "pw-grace2", "first-secret", true)
	rotate := func() {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest("PATCH",
			"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"rotate_secret":true,"rotate_grace_seconds":3600}`)),
			userID, wsID, "OWNER")
		req.SetPathValue("webhookId", wh.ID)
		rr := httptest.NewRecorder()
		h.UpdateWebhook(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
		}
	}
	rotate()
	store := pipeline.NewWebhookStore(db)
	second, _ := store.GetByID(t.Context(), wh.ID)
	rotate()
	third, _ := store.GetByID(t.Context(), wh.ID)

	// The secret before last (first-secret) is gone; the one just replaced still works.
	if third.ValidateSignature([]byte("x"), covPSWSign("first-secret", "x")) {
		t.Error("the secret before last must not survive a second rotation")
	}
	if !third.ValidateSignature([]byte("x"), covPSWSign(second.SigningSecret, "x")) {
		t.Error("the secret just replaced should still work inside its window")
	}
	// The timestamped scheme signs "<ts>.<body>" and honours the window as well.
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	if !third.ValidateTimestampedSignature([]byte("x"), ts, covPSWSign(second.SigningSecret, ts+".x"), now, 0) {
		t.Error("timestamped signature with the previous secret should pass inside its window")
	}
	if third.ValidateTimestampedSignature([]byte("x"), ts, covPSWSign("first-secret", ts+".x"), now, 0) {
		t.Error("timestamped signature with the secret before last must fail")
	}
}
