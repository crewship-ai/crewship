package api

import (
	"context"
	"errors"
	"testing"
)

// spyConvReader records how loadConversationHistory reads, so we can prove the
// keeper hot path uses the bounded ReadTail (#1041), not the whole-file Read.
type spyConvReader struct {
	msgs        []ConversationMessage
	readCalled  bool
	tailCalled  bool
	tailMaxSeen int
}

func (s *spyConvReader) Read(_ context.Context, _ string, _, _ int) ([]ConversationMessage, error) {
	s.readCalled = true
	return s.msgs, nil
}
func (s *spyConvReader) ReadTail(_ context.Context, _ string, maxMessages int) ([]ConversationMessage, error) {
	s.tailCalled = true
	s.tailMaxSeen = maxMessages
	if maxMessages > 0 && len(s.msgs) > maxMessages {
		return s.msgs[len(s.msgs)-maxMessages:], nil
	}
	return s.msgs, nil
}

// TestKeeperPerf_LoadConversationHistory_ReadsTailOnly pins #1041: keeper must
// call the bounded ReadTail(keeperConvHistoryLimit), NOT Read(0,0) which loaded
// the entire .jsonl on every request/execute.
func TestKeeperPerf_LoadConversationHistory_ReadsTailOnly(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, agentID, _ := seedKeeperFixture(t, db)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('perf-chat', ?, ?, 'CHAT', 'ACTIVE')`, agentID, wsID)

	spy := &spyConvReader{msgs: []ConversationMessage{{Role: "user", Content: "hello"}}}
	h := newKeeperHandler(t, db).WithConversations(spy)

	got := h.loadConversationHistory(context.Background(), agentID)
	if got == "" {
		t.Fatal("expected non-empty history")
	}
	if spy.readCalled {
		t.Error("loadConversationHistory called the whole-file Read; must use ReadTail (#1041)")
	}
	if !spy.tailCalled {
		t.Fatal("loadConversationHistory did not call ReadTail")
	}
	if spy.tailMaxSeen != keeperConvHistoryLimit {
		t.Errorf("ReadTail maxMessages = %d, want keeperConvHistoryLimit=%d", spy.tailMaxSeen, keeperConvHistoryLimit)
	}
}

// errConvReader returns an error from ReadTail to pin the error path.
type errConvReader struct{}

func (errConvReader) Read(_ context.Context, _ string, _, _ int) ([]ConversationMessage, error) {
	return nil, errors.New("read boom")
}
func (errConvReader) ReadTail(_ context.Context, _ string, _ int) ([]ConversationMessage, error) {
	return nil, errors.New("tail boom")
}

func TestKeeperPerf_LoadConversationHistory_ReadTailError(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, agentID, _ := seedKeeperFixture(t, db)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('perf-chat2', ?, ?, 'CHAT', 'ACTIVE')`, agentID, wsID)

	h := newKeeperHandler(t, db).WithConversations(errConvReader{})
	if got := h.loadConversationHistory(context.Background(), agentID); got != "" {
		t.Errorf("ReadTail error should yield empty history, got %q", got)
	}
}
