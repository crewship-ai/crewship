package api

// Second coverage pass for message_feedback.go: ensureChatVisible's guard
// and DB-error returns, Create's visibility-error 500, the no-chat
// workspace-fallback 403/500, the post-insert id-lookup fallback, Delete's
// exec failure, and List's query failure.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covFB2Post(t *testing.T, h *MessageFeedbackHandler, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/feedback", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "t@x.com"}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestFB2_EnsureChatVisible_Guards(t *testing.T) {
	bed := setupFeedbackTestBed(t)

	t.Run("empty chat id", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: bed.userID}))
		ws, ok, err := bed.h.ensureChatVisible(req, "")
		if ws != "" || ok || err != nil {
			t.Errorf("got (%q,%v,%v), want empty miss", ws, ok, err)
		}
	})
	t.Run("nil user", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		ws, ok, err := bed.h.ensureChatVisible(req, bed.chatID)
		if ws != "" || ok || err != nil {
			t.Errorf("got (%q,%v,%v), want empty miss", ws, ok, err)
		}
	})
}

func TestFB2_EnsureChatVisible_DBErrors(t *testing.T) {
	t.Run("chats query error", func(t *testing.T) {
		bed := setupFeedbackTestBed(t)
		if _, err := bed.h.db.Exec(`ALTER TABLE chats RENAME TO chats_hidden_fb2`); err != nil {
			t.Fatalf("rename chats: %v", err)
		}
		req := httptest.NewRequest("GET", "/x", nil)
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: bed.userID}))
		_, _, err := bed.h.ensureChatVisible(req, bed.chatID)
		if err == nil {
			t.Error("want error from broken chats table")
		}
	})
	t.Run("membership query error surfaces 500 via Create", func(t *testing.T) {
		bed := setupFeedbackTestBed(t)
		if _, err := bed.h.db.Exec(`ALTER TABLE workspace_members RENAME TO wm_hidden_fb2`); err != nil {
			t.Fatalf("rename workspace_members: %v", err)
		}
		rr := covFB2Post(t, bed.h, bed.userID,
			`{"message_id":"m1","chat_id":"`+bed.chatID+`","signal":"helpful"}`)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestFB2_Create_NoChatFallback(t *testing.T) {
	t.Run("no membership 403", func(t *testing.T) {
		bed := setupFeedbackTestBed(t)
		// A user with zero workspace memberships.
		if _, err := bed.h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('fb2-loner', 'l@fb.com', 'L')`); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		rr := covFB2Post(t, bed.h, "fb2-loner", `{"message_id":"m1","signal":"helpful"}`)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "no workspace membership") {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("membership lookup error 500", func(t *testing.T) {
		bed := setupFeedbackTestBed(t)
		if _, err := bed.h.db.Exec(`ALTER TABLE workspace_members RENAME TO wm_hidden_fb2b`); err != nil {
			t.Fatalf("rename workspace_members: %v", err)
		}
		rr := covFB2Post(t, bed.h, bed.userID, `{"message_id":"m1","signal":"helpful"}`)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestFB2_Create_PersistedIDLookupFallback(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// Make only SELECTs against message_feedback fail after the insert:
	// SQLite can't do that via trigger, so instead drop the table between
	// insert and lookup using a trigger that renames? Not possible —
	// instead make the post-insert SELECT fail by deleting the row from
	// inside an AFTER INSERT trigger. The lookup then hits ErrNoRows and
	// the handler must fall back to the generated id.
	if _, err := bed.h.db.Exec(`
		CREATE TRIGGER fb2_eat_row AFTER INSERT ON message_feedback
		BEGIN DELETE FROM message_feedback WHERE id = NEW.id; END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := covFB2Post(t, bed.h, bed.userID,
		`{"message_id":"m-fb2","chat_id":"`+bed.chatID+`","signal":"helpful","reason":"good"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"id":`) {
		t.Errorf("body = %q, want generated id fallback", rr.Body.String())
	}
}

func TestFB2_Delete_ExecError500(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	if _, err := bed.h.db.Exec(`
		CREATE TRIGGER fb2_block_delete BEFORE DELETE ON message_feedback
		BEGIN SELECT RAISE(ABORT, 'fb2 no delete'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	// Seed a row so the DELETE actually fires the trigger.
	if _, err := bed.h.db.Exec(`
		INSERT INTO message_feedback (id, workspace_id, message_id, signal, user_id)
		VALUES ('fb2-del', ?, 'm-del', 'helpful', ?)`, bed.wsID, bed.userID); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/feedback?message_id=m-del&signal=helpful", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: bed.userID}))
	rr := httptest.NewRecorder()
	bed.h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestFB2_List_QueryError500(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	if _, err := bed.h.db.Exec(`ALTER TABLE message_feedback RENAME TO mf_hidden_fb2`); err != nil {
		t.Fatalf("rename message_feedback: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/feedback?message_id=m1", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: bed.userID}))
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

var _ = context.Background
