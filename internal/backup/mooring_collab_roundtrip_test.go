package backup

import (
	"bytes"
	"encoding/json"
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
			{`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES(?,?,'18446744073709551615','AP9h',3)`, []any{page, ws}},
			{`INSERT INTO mooring_collab_audit(id,page_id,workspace_id,kind,detail_json) VALUES(?,?,?,'change_saved',?)`, []any{"audit-" + page, page, ws, marker}},
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
	tables := []string{"mooring_collab_documents", "mooring_collab_tasks", "mooring_collab_changes", "mooring_collab_operations", "mooring_collab_checkpoints", "mooring_collab_audit"}
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
}
