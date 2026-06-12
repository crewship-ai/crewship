package api

// Coverage for message_feedback.go — the Delete handler's guard rails and
// the actual user-scoped row removal.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covFbDelete(t *testing.T, h *MessageFeedbackHandler, userID, wsID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/api/v1/feedback"+query, nil)
	if userID != "" {
		req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	}
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestFeedbackDelete_Guards(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	h, userID, wsID := bed.h, bed.userID, bed.wsID

	t.Run("anonymous 401", func(t *testing.T) {
		if rr := covFbDelete(t, h, "", "", "?message_id=m&signal=not_helpful"); rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
	t.Run("missing params 400", func(t *testing.T) {
		if rr := covFbDelete(t, h, userID, wsID, "?message_id=m"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("oversized message_id 400", func(t *testing.T) {
		long := strings.Repeat("x", maxFeedbackIDChars+1)
		if rr := covFbDelete(t, h, userID, wsID, "?message_id="+long+"&signal=not_helpful"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("oversized signal 400", func(t *testing.T) {
		long := strings.Repeat("s", maxFeedbackSignalChars+1)
		if rr := covFbDelete(t, h, userID, wsID, "?message_id=m&signal="+long); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown signal 400", func(t *testing.T) {
		if rr := covFbDelete(t, h, userID, wsID, "?message_id=m&signal=invented"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
}

func TestFeedbackDelete_RemovesOwnRowOnly(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	h, userID, otherID, wsID, messageID := bed.h, bed.userID, bed.otherID, bed.wsID, bed.messageID

	// Two rows on the same (message, signal) tuple by different users.
	for i, uid := range []string{userID, otherID} {
		if _, err := h.db.Exec(`
			INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, user_id, signal)
			VALUES (?, ?, ?, ?, ?, 'not_helpful')`,
			"fb-del-"+string(rune('a'+i)), wsID, bed.chatID, messageID, uid); err != nil {
			t.Fatalf("seed feedback for %s: %v", uid, err)
		}
	}

	rr := covFbDelete(t, h, userID, wsID, "?message_id="+messageID+"&signal=not_helpful")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}

	var mine, theirs int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE message_id = ? AND user_id = ?`, messageID, userID).Scan(&mine); err != nil {
		t.Fatalf("count mine: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE message_id = ? AND user_id = ?`, messageID, otherID).Scan(&theirs); err != nil {
		t.Fatalf("count theirs: %v", err)
	}
	if mine != 0 {
		t.Errorf("caller's row not deleted (count=%d)", mine)
	}
	if theirs != 1 {
		t.Errorf("other user's row deleted (count=%d)", theirs)
	}

	// Idempotent: deleting an already-gone row is still 204.
	rr2 := covFbDelete(t, h, userID, wsID, "?message_id="+messageID+"&signal=not_helpful")
	if rr2.Code != http.StatusNoContent {
		t.Errorf("repeat delete status = %d, want 204", rr2.Code)
	}
}
