package backup

import (
	"bytes"
	"errors"
	"testing"
)

// A dry run must refuse a target the real restore would refuse. Before this
// test, the dry-run branch returned before RestoreDumpTxHooks ran its target
// preflights (run admission, private artifact context, issued Mooring
// delivery), so on a dev instance a fork dry run reported "validation
// complete" and the real fork then failed with a 500 from the Mooring guard.
func TestRestoreDryRun_RunsTargetPreflights(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "dryrun-preflight")
	const owner = "u_cov_dryrun-preflight"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('member-dry',?,?,'OWNER')`, []any{workspace, owner}},
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('dry-page',?,'dry-page','Generic',?,1,'{}')`, []any{workspace, owner}},
		{`INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'dry-page','hello',1,1,'{"text":"x"}')`, []any{workspace}},
		{`INSERT INTO mooring_session_owners(page_id,workspace_id,application_id,owner_id,generation,token_hash,boot_id,expires_ns) VALUES('dry-page',?,'hello','dry-owner',7,?,'boot',9223372036854775807)`, []any{workspace, bytes.Repeat([]byte{1}, 32)}},
		{`INSERT INTO mooring_delivery_gates(id,page_id,workspace_id,application_id,participant_ref,owner_id,owner_generation,owner_token_hash,permission_version,boot_id,expires_ns,renewal_nonce) VALUES('dry-gate','dry-page',?,'hello','dry-participant','dry-owner',7,?,'1','boot',9223372036854775807,'nonce')`, []any{workspace, bytes.Repeat([]byte{2}, 32)}},
	} {
		if _, err := source.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}

	const passphrase = "dry-run-preflight-pass-123"
	actor := covAdminActor()
	created, err := CreateBackup(t.Context(), source, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	// An exact-ID restore lands in the source workspace, whose issued gate
	// the guard must protect. (A --as-workspace fork lands in a remapped
	// workspace and is admitted; see TestRestoreFork_AdmitsSourceDelivery.)
	opts := RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}

	dry := opts
	dry.DryRun = true
	_, dryErr := RestoreBackup(t.Context(), source, dry)

	_, realErr := RestoreBackup(t.Context(), source, opts)
	if !errors.Is(realErr, ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("fixture: the real restore must hit the Mooring delivery guard, got %v", realErr)
	}
	if !errors.Is(dryErr, ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("dry run did not refuse the target the real restore refuses: dry=%v real=%v", dryErr, realErr)
	}
}

// A --as-workspace fork on the source instance lands every row in a freshly
// remapped workspace. The guard used to also count the dump's source anchor
// (dump.WorkspaceID, which RemapIDs does not rewrite) and so refused the fork
// because of an issued gate in the untouched source workspace. The fork must
// be admitted, carry no delivery metadata, and leave the source gate intact.
func TestRestoreFork_AdmitsSourceDelivery(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "fork-delivery")
	const owner = "u_cov_fork-delivery"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('member-fork',?,?,'OWNER')`, []any{workspace, owner}},
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('fork-page',?,'fork-page','Generic',?,1,'{}')`, []any{workspace, owner}},
		{`INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'fork-page','hello',1,1,'{"text":"x"}')`, []any{workspace}},
		{`INSERT INTO mooring_session_owners(page_id,workspace_id,application_id,owner_id,generation,token_hash,boot_id,expires_ns) VALUES('fork-page',?,'hello','fork-owner',7,?,'boot',9223372036854775807)`, []any{workspace, bytes.Repeat([]byte{1}, 32)}},
		{`INSERT INTO mooring_delivery_gates(id,page_id,workspace_id,application_id,participant_ref,owner_id,owner_generation,owner_token_hash,permission_version,boot_id,expires_ns,renewal_nonce) VALUES('fork-gate','fork-page',?,'hello','fork-participant','fork-owner',7,?,'1','boot',9223372036854775807,'nonce')`, []any{workspace, bytes.Repeat([]byte{2}, 32)}},
	} {
		if _, err := source.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	const passphrase = "fork-delivery-pass-123"
	actor := covAdminActor()
	created, err := CreateBackup(t.Context(), source, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	opts := RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor, AsWorkspace: "fork-delivery-copy"}
	dry := opts
	dry.DryRun = true
	if _, err := RestoreBackup(t.Context(), source, dry); err != nil {
		t.Fatalf("dry-run fork refused: %v", err)
	}
	result, err := RestoreBackup(t.Context(), source, opts)
	if err != nil {
		t.Fatalf("fork refused because of the source workspace's delivery: %v", err)
	}
	if result.RestoredWorkspaceID == "" || result.RestoredWorkspaceID == workspace {
		t.Fatalf("fork did not land in a new workspace: %q", result.RestoredWorkspaceID)
	}
	var n int
	if err := source.QueryRow(`SELECT COUNT(*) FROM mooring_delivery_gates WHERE workspace_id=?`, result.RestoredWorkspaceID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fork carried delivery metadata: %d %v", n, err)
	}
	if err := source.QueryRow(`SELECT COUNT(*) FROM mooring_delivery_gates WHERE id='fork-gate' AND workspace_id=?`, workspace).Scan(&n); err != nil || n != 1 {
		t.Fatalf("source gate changed: %d %v", n, err)
	}
	if err := source.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=? AND slug='fork-page'`, result.RestoredWorkspaceID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("forked page missing: %d %v", n, err)
	}
}
