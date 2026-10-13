package runauthority

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func authorityFixture(t *testing.T) (*sql.DB, *chataudience.Receipt) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('human','human@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('member','ws','human','MEMBER')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent','ws','Agent','agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('chat','ws','agent','human','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "chat", "human", nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	return db, receipt
}

func putEntry(t *testing.T, db *sql.DB, id, run, kind, actor string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES(?,'ws','agent',?,?, 'run authority test',?,?)`, id, kind, actor, string(raw), run)
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveDurableAuthorityInsideTransaction(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "start", "run", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := Resolve(t.Context(), tx, "run", "agent", "ws")
	if err != nil || got.ChatID != "chat" || got.TriggerMessageID != "trigger" || got.HumanAuthority == nil || *got.HumanAuthority != *receipt {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE workspace_members SET role='VIEWER' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.Context(), tx, "run", "agent", "ws"); !errors.Is(err, ErrRequesterStale) {
		t.Fatalf("stale member accepted: %v", err)
	}
}

func TestResolveFailsClosedOnMissingStaleAndEndedAuthority(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "start", "run", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	for _, tc := range []struct {
		run, agent, workspace string
		want                  error
	}{
		{"missing", "agent", "ws", ErrUnavailable},
		{"run", "other", "ws", ErrIdentityMismatch},
		{"run", "agent", "other", ErrUnavailable},
	} {
		if _, err := Resolve(t.Context(), db, tc.run, tc.agent, tc.workspace); !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	putEntry(t, db, "end", "run", "run.cancelled", "orchestrator", nil)
	if _, err := Resolve(t.Context(), db, "run", "agent", "ws"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("ended run accepted: %v", err)
	}
	putEntry(t, db, "legacy", "legacy", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER"})
	if _, err := Resolve(t.Context(), db, "legacy", "agent", "ws"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing receipt downgraded: %v", err)
	}
}

func TestResolveAutonomousRunRequiresHostAdmission(t *testing.T) {
	db, _ := authorityFixture(t)
	for _, tc := range []struct {
		id, actor, trigger string
		host, allow        bool
	}{
		{"scheduled", "sidecar", "SCHEDULED", true, true},
		{"assignment", "orchestrator", "ASSIGNMENT", true, true},
		{"legacy-assignment", "orchestrator", "ASSIGNMENT", false, false},
		{"forged", "sidecar", "SCHEDULED", false, false},
		{"user", "sidecar", "USER", true, false},
		{"unknown", "sidecar", "invented", true, false},
	} {
		putEntry(t, db, tc.id, tc.id, "run.started", tc.actor, map[string]any{"trigger_type": tc.trigger, "host_admitted": tc.host})
		got, err := Resolve(t.Context(), db, tc.id, "agent", "ws")
		if tc.allow {
			if err != nil || got.HumanAuthority != nil || got.TriggerType != tc.trigger {
				t.Fatalf("%s: %+v %v", tc.id, got, err)
			}
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s accepted: %+v %v", tc.id, got, err)
		}
	}
}

func TestResolveForeignWorkspaceTraceCannotRevokeOriginalRun(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "start", "shared-run", "run.started", "sidecar", map[string]any{"chat_id": "chat", "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": "trigger"})
	if _, e := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('fork','Fork','fork');INSERT INTO agents(id,workspace_id,name,slug) VALUES('fork-agent','fork','Fork','fork')`); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"run.started", "run.completed"} {
		if _, e := db.Exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES(?,'fork','fork-agent',?,'orchestrator','forked history','{}','shared-run')`, "fork-"+kind, kind); e != nil {
			t.Fatal(e)
		}
		got, e := Resolve(t.Context(), db, "shared-run", "agent", "ws")
		if e != nil || got.HumanAuthority == nil || got.ChatID != "chat" {
			t.Fatalf("foreign %s changed authority: %+v %v", kind, got, e)
		}
	}
	putEntry(t, db, "end", "shared-run", "run.completed", "orchestrator", nil)
	if _, e := Resolve(t.Context(), db, "shared-run", "agent", "ws"); !errors.Is(e, ErrIdentityMismatch) {
		t.Fatalf("own terminal ignored: %v", e)
	}
}

func TestMissingRequesterMarkerCannotBecomeAutonomousOrDelegate(t *testing.T) {
	db, _ := authorityFixture(t)
	putEntry(t, db, "human-channel-start", "human-channel", "run.started", "orchestrator", map[string]any{"chat_id": "chat", "trigger_type": "ASSIGNMENT", "host_admitted": true, "created_by_user_id": "human", "requester_authority_missing": true})
	if _, err := Resolve(t.Context(), db, "human-channel", "agent", "ws"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing human authority treated as autonomy: %v", err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := CaptureOrigin(t.Context(), tx, "human-channel", "agent", "ws"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing requester laundered via delegation: %v", err)
	}
	tx.Rollback()
	putEntry(t, db, "genuine-auto-start", "genuine-auto", "run.started", "orchestrator", map[string]any{"chat_id": "chat", "trigger_type": "ASSIGNMENT", "host_admitted": true})
	if a, err := Resolve(t.Context(), db, "genuine-auto", "agent", "ws"); err != nil || a.HumanAuthority != nil {
		t.Fatalf("genuine autonomous workflow refused: %+v %v", a, err)
	}
}
