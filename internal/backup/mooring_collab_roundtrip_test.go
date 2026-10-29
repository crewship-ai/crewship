package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMooringCollaborationBackupPreservesLedgerCheckpointTaskAudit(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "mooring-collab-backup")
	other, _ := seedCovWorkspace(t, source, "mooring-collab-foreign")
	seed := func(ws, actor, page, marker string) {
		t.Helper()
		queries := []struct {
			q    string
			args []any
		}{
			{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'OWNER')`, []any{"member-" + page, ws, actor}},
			{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, []any{page, ws, page, "Collaboration", actor}},
			{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'editor','18446744073709551615','AP9h')`, []any{page, ws}},
			{`INSERT INTO mooring_collab_tasks(page_id,workspace_id,task_id,participant_ref,allow_partial,status) VALUES(?,?,'task','original-author',1,'cancelled')`, []any{page, ws}},
			{`INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,task_id,result_json) VALUES(?,?,'change','sha256:immutable','task',?)`, []any{page, ws, marker}},
			{`INSERT INTO mooring_collab_operations(page_id,workspace_id,op_id,change_id,participant_ref,fingerprint,result_json) VALUES(?,?,'op','change','original-author','sha256:immutable',?)`, []any{page, ws, marker}},
			{`INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,'p','18446744073709551615','9223372036854775808',0)`, []any{page, ws}},
			{`INSERT INTO mooring_collab_target_effects(page_id,workspace_id,op_id,target_id,revision,before_revision,before_structure_revision,content_changed,structure_changed,created,deleted,record_json) VALUES(?,?,'op','p','18446744073709551615','9223372036854775808','9223372036854775808',1,0,0,0,?)`, []any{page, ws, func() string {
				b, _ := json.Marshal(map[string]any{"workspace_id": ws, "page_id": page, "op_id": "op", "target_id": "p", "revision": "18446744073709551615", "before_revision": "9223372036854775808", "before_structure_revision": "9223372036854775808", "content_changed": true, "structure_changed": false, "created": false, "deleted": false})
				return string(b)
			}()}},
			{`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES(?,?,'18446744073709551615','AP9h',3)`, []any{page, ws}},
			{`INSERT INTO mooring_collab_audit(id,page_id,workspace_id,kind,detail_json) VALUES(?,?,?,'change_saved',?)`, []any{"audit-" + page, page, ws, marker}},
			{`INSERT INTO mooring_collab_artifact_edits(page_id,workspace_id,application_id,change_id,membership_hash,first_revision,last_revision,host_session_id,actor_kind,human_user_id,agent_id,crew_id,chat_id,message_id,work_id,run_id,attempt_number,attempt_generation) VALUES(?,?,'editor','change','sha256:immutable','18446744073709551615','18446744073709551615','historical-session','agent',?,'historical-agent','historical-crew','historical-chat','historical-message','historical-work','historical-run',1,1)`, []any{page, ws, actor}},
			{`INSERT INTO mooring_session_owners(page_id,workspace_id,application_id,owner_id,generation,token_hash,boot_id,expires_ns) VALUES(?,?,'editor','live-owner',1,?,'source-boot',999999)`, []any{page, ws, bytes.Repeat([]byte{1}, 32)}},
		}
		for _, query := range queries {
			if _, err := source.Exec(query.q, query.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(workspace, "u_cov_mooring-collab-backup", "collab-portable", `{"status":"saved","marker":"durable český audit"}`)
	seed(other, "u_cov_mooring-collab-foreign", "collab-foreign", `{"marker":"foreign-secret"}`)
	dump, err := DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"mooring_collab_documents", "mooring_collab_tasks", "mooring_collab_changes", "mooring_collab_operations", "mooring_collab_target_versions", "mooring_collab_target_effects", "mooring_collab_checkpoints", "mooring_collab_audit", "mooring_collab_artifact_edits"}
	for _, table := range tables {
		if len(dump.Tables[table]) != 1 {
			t.Fatal("durable table missing", table)
		}
	}
	raw, err := json.Marshal(dump)
	if err != nil || strings.Contains(string(raw), "foreign-secret") || strings.Contains(string(raw), "live-owner") {
		t.Fatal("workspace/runtime authority leaked", err)
	}
	destination := openMigratedDBCov(t)
	if err = RestoreDump(t.Context(), destination, dump); err != nil {
		t.Fatal(err)
	}
	restored, err := DumpWorkspace(t.Context(), destination, workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		before, _ := json.Marshal(dump.Tables[table])
		after, _ := json.Marshal(restored.Tables[table])
		if !bytes.Equal(before, after) {
			t.Fatalf("immutable durable content changed: %s", table)
		}
	}
	var owners int
	if err = destination.QueryRow(`SELECT count(*) FROM mooring_session_owners`).Scan(&owners); err != nil || owners != 0 {
		t.Fatal("runtime owner survived portability", owners, err)
	}
	if err = destination.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id='historical-run'`).Scan(&owners); err != nil || owners != 0 {
		t.Fatal("historical edit attribution reconstructed a run principal", owners, err)
	}
}

func TestMooringCollaborationRestoreRefusesRemapAndCollisionBeforeWrites(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "collab-refusal")
	user := "u_cov_collab-refusal"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('collab-owner',?,?,'OWNER')`, []any{workspace, user}},
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('collab-page',?,'collab-page','Collab',?,'{}')`, []any{workspace, user}},
		{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('collab-page',?,'editor','0','MA==')`, []any{workspace}},
		{`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES('collab-page',?,'0','MA==',1)`, []any{workspace}},
	} {
		if _, err := source.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	const passphrase = "collab-refusal-synthetic-passphrase"
	actor := Actor{UserID: user, Email: "collab-refusal@cov.test", Role: "ADMIN"}
	created, err := CreateBackup(t.Context(), source, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() []byte {
		t.Helper()
		dump, err := DumpWorkspace(t.Context(), source, workspace)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(dump)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	for _, opts := range []RestoreOptions{
		{Path: created.Path, Passphrase: passphrase, Actor: actor, AsWorkspace: "unqualified-copy"},
		{Path: created.Path, Passphrase: passphrase, Actor: actor, Replace: true},
	} {
		result, err := RestoreBackup(t.Context(), source, opts)
		if !errors.Is(err, ErrMooringCollaborationRemap) || result != nil {
			t.Fatalf("transformed/colliding restore accepted: %+v %v", result, err)
		}
		if !bytes.Equal(before, snapshot()) {
			t.Fatal("refusal mutated existing destination/source")
		}
	}
	dump, err := DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	dumpBefore, _ := json.Marshal(dump)
	if err = RemapIDs(t.Context(), source, dump); !errors.Is(err, ErrMooringCollaborationRemap) {
		t.Fatal("direct remap bypass", err)
	}
	dumpAfter, _ := json.Marshal(dump)
	if !bytes.Equal(dumpBefore, dumpAfter) || !bytes.Equal(before, snapshot()) {
		t.Fatal("remap refusal mutated dump or source")
	}
	hookCalled := false
	if _, err = RestoreDumpTx(t.Context(), source, dump, func(context.Context) error { hookCalled = true; return nil }); !errors.Is(err, ErrMooringCollaborationRemap) || hookCalled {
		t.Fatal("direct collision bypass", err, hookCalled)
	}
	if !bytes.Equal(before, snapshot()) {
		t.Fatal("direct collision refusal wrote destination")
	}
	empty := openMigratedDBCov(t)
	if _, err = RestoreBackup(t.Context(), empty, RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}); err != nil {
		t.Fatal("qualified empty-target exact-ID restore refused", err)
	}
	var restored string
	if err = empty.QueryRow(`SELECT checkpoint_base64 FROM mooring_collab_documents WHERE page_id='collab-page'`).Scan(&restored); err != nil || restored != "MA==" {
		t.Fatal("exact-ID checkpoint lost", restored, err)
	}
}
