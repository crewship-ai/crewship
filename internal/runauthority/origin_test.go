package runauthority

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOriginSurvivesParentCompletionAndFencesDelegatedWork(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "root-start", "root", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureOrigin(t.Context(), tx, "root", "agent", "ws")
	if err != nil {
		t.Fatal(err)
	}
	again, err := CaptureOrigin(t.Context(), tx, "root", "agent", "ws")
	if err != nil || again.ID != origin.ID {
		t.Fatalf("unstable root origin: %+v %+v %v", origin, again, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO agents(id,workspace_id,name,slug) VALUES('child-agent','ws','Child','child')`); err != nil {
		t.Fatal(err)
	}
	putEntry(t, db, "root-end", "root", "run.completed", "orchestrator", nil)
	raw, _ := json.Marshal(map[string]any{"trigger_type": "ASSIGNMENT", "chat_id": "synthetic-child-chat", "chat_origin_id": origin.ID})
	if _, err := db.ExecContext(t.Context(), `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('child-start','ws','child-agent','run.started','orchestrator','delegated',?,'child-run')`, string(raw)); err != nil {
		t.Fatal(err)
	}
	a, err := Resolve(t.Context(), db, "child-run", "child-agent", "ws")
	if err != nil || a.AgentID != "child-agent" || a.ChatID != "chat" || a.ChatOriginID != origin.ID || a.HumanAuthority == nil || *a.HumanAuthority != *receipt {
		t.Fatalf("lost inherited identity: %+v %v", a, err)
	}
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transitive, err := CaptureOrigin(t.Context(), tx, "child-run", "child-agent", "ws")
	if err != nil || transitive.ID != origin.ID {
		t.Fatalf("laundered transitive origin: %+v %v", transitive, err)
	}
	_ = tx.Rollback()
	if _, err := db.ExecContext(t.Context(), `UPDATE execution_origins SET human_authority='{}' WHERE id=?`, origin.ID); err == nil {
		t.Fatal("origin could be rewritten")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE workspace_members SET role='VIEWER' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.Context(), db, "child-run", "child-agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("revoked root became autonomous: %v", err)
	}
}

func TestOriginMissingAndDeletedChatFailClosed(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "start", "root", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureOrigin(t.Context(), tx, "root", "agent", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateOrigin(t.Context(), db, "missing", "agent", "ws"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing origin: %v", err)
	}
	if _, err := ValidateOrigin(t.Context(), db, origin.ID, "missing-agent", "ws"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing executor: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM chats WHERE id='chat'`); err != nil {
		t.Fatalf("origin blocked chat deletion: %v", err)
	}
	if _, err := ValidateOrigin(t.Context(), db, origin.ID, "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("deleted root chat accepted: %v", err)
	}
}

func TestCaptureAutonomousOriginRemainsEmpty(t *testing.T) {
	db, _ := authorityFixture(t)
	putEntry(t, db, "scheduled", "scheduled", "run.started", "orchestrator", map[string]any{"trigger_type": "SCHEDULED", "host_admitted": true})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	origin, err := CaptureOrigin(t.Context(), tx, "scheduled", "agent", "ws")
	if err != nil || origin.ID != "" {
		t.Fatalf("invented human origin: %+v %v", origin, err)
	}
}
