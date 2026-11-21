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
	opts := RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor, AsWorkspace: "dry-run-fork"}

	dry := opts
	dry.DryRun = true
	_, dryErr := RestoreBackup(t.Context(), source, dry)

	_, realErr := RestoreBackup(t.Context(), source, opts)
	if !errors.Is(realErr, ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("fixture: the real fork must hit the Mooring delivery guard, got %v", realErr)
	}
	if !errors.Is(dryErr, ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("dry run did not refuse the target the real restore refuses: dry=%v real=%v", dryErr, realErr)
	}
}
