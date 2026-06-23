package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// keeper_cov_test.go covers keeper.go's remaining branches: SetJournal,
// GetRequest's DB-error arm, and loadConversationHistory end to end.
// Helpers prefixed covKP.

type covKPConvReader struct {
	msgs []ConversationMessage
	err  error
}

func (c covKPConvReader) Read(_ context.Context, _ string, _, _ int) ([]ConversationMessage, error) {
	return c.msgs, c.err
}

func TestCovKP_SetJournal(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db)
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("SetJournal(nil) = %T", h.journal)
	}
	h.SetJournal(covCPFailingEmitter{})
	if _, ok := h.journal.(covCPFailingEmitter); !ok {
		t.Fatalf("SetJournal kept %T", h.journal)
	}
}

func TestCovKP_GetRequest_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db)
	db.Close()
	req := httptest.NewRequest("GET", "/api/v1/internal/keeper/request/r1", nil)
	req.SetPathValue("requestId", "r1")
	rr := httptest.NewRecorder()
	h.GetRequest(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovKP_LoadConversationHistory(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, agentID, _ := seedKeeperFixture(t, db)

	// No conversations reader wired -> "".
	h := newKeeperHandler(t, db)
	if got := h.loadConversationHistory(context.Background(), agentID); got != "" {
		t.Errorf("no reader: %q, want empty", got)
	}

	// Reader wired but agent has no active chat -> "".
	h = newKeeperHandler(t, db).WithConversations(covKPConvReader{msgs: []ConversationMessage{{Role: "user", Content: "hi"}}})
	if got := h.loadConversationHistory(context.Background(), agentID); got != "" {
		t.Errorf("no chat: %q, want empty", got)
	}

	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('covkp-chat', ?, ?, 'CHAT', 'ACTIVE')`, agentID, wsID)

	// Read error -> "".
	h = newKeeperHandler(t, db).WithConversations(covKPConvReader{err: errors.New("covkp boom")})
	if got := h.loadConversationHistory(context.Background(), agentID); got != "" {
		t.Errorf("read error: %q, want empty", got)
	}

	// Empty messages -> "".
	h = newKeeperHandler(t, db).WithConversations(covKPConvReader{})
	if got := h.loadConversationHistory(context.Background(), agentID); got != "" {
		t.Errorf("empty msgs: %q, want empty", got)
	}

	// Tool messages skipped, long content truncated, history capped.
	long := strings.Repeat("x", 400)
	msgs := []ConversationMessage{{Role: "tool", Content: "noise"}}
	for i := 0; i < 25; i++ { // more than the history limit
		msgs = append(msgs, ConversationMessage{Role: "user", Content: "msg"})
	}
	msgs = append(msgs, ConversationMessage{Role: "assistant", Content: long})
	h = newKeeperHandler(t, db).WithConversations(covKPConvReader{msgs: msgs})
	got := h.loadConversationHistory(context.Background(), agentID)
	if got == "" {
		t.Fatalf("expected history text")
	}
	if strings.Contains(got, "noise") {
		t.Errorf("tool message leaked into history: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("long content not truncated: %q", got)
	}
	if !strings.Contains(got, "[assistant]:") {
		t.Errorf("role prefix missing: %q", got)
	}
}
