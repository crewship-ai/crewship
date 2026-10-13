package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestExecutionOriginsUpgradeRecoversPositiveHistoricalEvidence(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "recovery.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = applyMigrationsUpTo(t.Context(), db, 20261013174957, logger); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO users(id,email,full_name) VALUES('recover-u','recover@example.test','User')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('recover-w','Recover','recover')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('recover-member','recover-w','recover-u','OWNER')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('recover-c','recover-w','Crew','crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('recover-a','recover-w','recover-c','Agent','agent')`)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('recover-chat','recover-w','recover-a','recover-u')`)
	receipt, e := chataudience.CaptureTrusted(t.Context(), db, "recover-chat", "recover-u", nil)
	if e != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, e)
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": "recover-chat", "trigger_message_id": "recover-message", "human_authority": receipt, "trigger_type": "USER"})
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-human-start','recover-w','recover-a','run.started','orchestrator','Historical human request','recover-human',?)`, string(payload))
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-human-end','recover-w','recover-a','run.completed','orchestrator','Parent ended','recover-human','{}')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-marked-human-start','recover-w','recover-a','run.started','orchestrator','Contradictory human provenance','recover-marked-human',json_set(?,'$.requester_authority_missing',json('true')))`, string(payload))
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-machine-start','recover-w','recover-a','run.started','orchestrator','Historical cron','recover-machine','{"host_admitted":true,"trigger_type":"CRON"}')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-bad-start','recover-w','recover-a','run.started','orchestrator','Unproved autonomy','recover-bad','{"trigger_type":"CRON"}')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-duplicate-one','recover-w','recover-a','run.started','orchestrator','Ambiguous machine root','recover-duplicate','{"host_admitted":true,"trigger_type":"CRON"}'),('recover-duplicate-two','recover-w','recover-a','run.started','orchestrator','Ambiguous machine root','recover-duplicate','{"host_admitted":true,"trigger_type":"CRON"}')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-requester-missing-start','recover-w','recover-a','run.started','orchestrator','Human request without receipt','recover-requester-missing','{"host_admitted":true,"trigger_type":"ASSIGNMENT","requester_authority_missing":true}'),('recover-null-start','recover-w','recover-a','run.started','orchestrator','Unknown receipt','recover-null','{"host_admitted":true,"trigger_type":"CRON","human_authority":null}')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('recover-foreign-w','Foreign','recover-foreign')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('recover-foreign-c','recover-foreign-w','Foreign','foreign')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('recover-foreign-a','recover-foreign-w','recover-foreign-c','Foreign','foreign')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('recover-foreign-start','recover-foreign-w','recover-foreign-a','run.started','orchestrator','Foreign proof cannot authorize this workspace','recover-missing','{"host_admitted":true,"trigger_type":"CRON"}')`)
	for _, kind := range []string{"human", "machine", "bad", "missing", "duplicate", "requester-missing", "null", "marked-human"} {
		exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,chain_origin) VALUES(?,'recover-w','recover-chat','recover-a','recover-a','Preserve me','QUEUED',?)`, "recover-work-"+kind, "recover-"+kind)
	}
	exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('recover-p','recover-w','pipeline','Pipeline','{}','hash')`)
	exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,chain_origin) VALUES('recover-intermediate','recover-w','recover-p','pipeline','completed','2026-10-13T00:00:00Z','recover-human')`)
	exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,chain_origin) VALUES('recover-work-indirect','recover-w','recover-chat','recover-a','recover-a','Preserve indirect','QUEUED','recover-intermediate')`)
	exec(`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,status,fire_at,chain_origin) VALUES('recover-unknown-pending','recover-w','recover-p','pipeline','pending','2026-10-13T00:00:00Z','missing-parent')`)
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"recover-work-human", "recover-work-indirect"} {
		var origin string
		if err = db.QueryRow(`SELECT chat_origin_id FROM assignments WHERE id=?`, id).Scan(&origin); err != nil {
			t.Fatal(err)
		}
		a, err := runauthority.ValidateOrigin(t.Context(), db, origin, "recover-a", "recover-w")
		if err != nil || a.HumanAuthority == nil || *a.HumanAuthority != *receipt {
			t.Fatalf("historical human receipt not recovered without renewal: %+v %v", a, err)
		}
		var raw string
		if err = db.QueryRow(`SELECT human_authority FROM execution_origins WHERE id=?`, origin).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var stored chataudience.Receipt
		if json.Unmarshal([]byte(raw), &stored) != nil || stored != *receipt {
			t.Fatal("recovery changed historical epochs")
		}
	}
	exec(`UPDATE workspace_members SET role='MEMBER' WHERE id='recover-member'`)
	for _, id := range []string{"recover-work-human", "recover-work-indirect"} {
		var origin string
		if err = db.QueryRow(`SELECT chat_origin_id FROM assignments WHERE id=?`, id).Scan(&origin); err != nil {
			t.Fatal(err)
		}
		if _, err = runauthority.ValidateOrigin(t.Context(), db, origin, "recover-a", "recover-w"); !errors.Is(err, runauthority.ErrRequesterStale) {
			t.Fatalf("revoked historical authority remained valid: %s %v", id, err)
		}
	}
	var autonomous bool
	if err = db.QueryRow(`SELECT chat_origin_id IS NULL FROM assignments WHERE id='recover-work-machine'`).Scan(&autonomous); err != nil || !autonomous {
		t.Fatalf("proved cron assignment fenced: %v", err)
	}
	for _, id := range []string{"recover-work-bad", "recover-work-missing", "recover-work-duplicate", "recover-work-requester-missing", "recover-work-null", "recover-work-marked-human"} {
		var root string
		if err = db.QueryRow(`SELECT o.root_run_id FROM assignments a JOIN execution_origins o ON o.id=a.chat_origin_id WHERE a.id=?`, id).Scan(&root); err != nil || root != runauthority.LegacyUnverifiedDelegationRootRunID {
			t.Fatalf("unknown assignment failed open: %s %s %v", id, root, err)
		}
	}
	var unknown string
	if err = db.QueryRow(`SELECT o.root_run_id FROM pending_runs p JOIN execution_origins o ON o.id=p.chat_origin_id WHERE p.id='recover-unknown-pending'`).Scan(&unknown); err != nil || unknown != runauthority.LegacyUnverifiedDelegationRootRunID {
		t.Fatalf("unknown pipeline work failed open: %s %v", unknown, err)
	}
}
