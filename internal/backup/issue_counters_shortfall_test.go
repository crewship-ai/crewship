package backup

// Regression coverage for #2255's cross-PR defect: #2251
// (migrateIssueCounterRows, issue_counters_restore.go) collapses pre-#1797
// issue_counters rows that share an effective (workspace_id, prefix) onto
// one row BEFORE insert, so stats.RowsInsertedByTable["issue_counters"] is
// the post-collapse count. #2009's rows_inserted_shortfalls
// (completeness.go) compared that post-collapse actual against the
// manifest's pre-collapse recorded count, so a completely successful
// restore of such a bundle reported a false shortfall for issue_counters —
// neither PR's own tests could see it, since each only exercises its own
// half.
//
// TestRestoreBackup_PreRekeyIssueCountersCollapseNotReportedAsShortfall
// pins the fix: two crews sharing an effective prefix must not produce an
// issue_counters entry in RowsInsertedShortfalls.
//
// TestRestoreBackup_PreRekeyIssueCountersGenuineShortfallStillReported
// pins the other side: the fix is a discount, not a blindfold — an
// issue_counters shortfall for a reason OTHER than the collapse (here, a
// row whose crew cannot be resolved anywhere) must still surface.

import (
	"context"
	"testing"
)

// TestRestoreBackup_PreRekeyIssueCountersCollapseNotReportedAsShortfall
// builds a pre-#1797 bundle where two crews (c_1, c_2) share the effective
// prefix ENG. migrateIssueCounterRows folds both rows into the ONE
// (workspace_id, prefix) row the post-#1797 schema allows, keeping the
// higher next_number — so IssueCountersMigrated is 2 but only 1 row
// actually lands. The manifest recorded 2 (the bundle's own pre-collapse
// row count), so naively comparing recorded against actual reports a
// shortfall of 1 on a restore that did exactly what the migration
// document says it should. That must not happen: RowsInsertedShortfalls
// must carry no "issue_counters" entry.
func TestRestoreBackup_PreRekeyIssueCountersCollapseNotReportedAsShortfall(t *testing.T) {
	ctx := context.Background()

	dumpJSON := []byte(`{
		"workspace_id": "ws_collapse",
		"tables": {
			"workspaces": [{"id": "ws_collapse", "name": "Collapse Co", "slug": "collapse-co"}],
			"crews": [
				{"id": "c_1", "workspace_id": "ws_collapse", "name": "Eng One", "slug": "eng-one", "issue_prefix": "ENG"},
				{"id": "c_2", "workspace_id": "ws_collapse", "name": "Eng Two", "slug": "eng-two", "issue_prefix": "ENG"}
			],
			"issue_counters": [
				{"crew_id": "c_1", "next_number": 10},
				{"crew_id": "c_2", "next_number": 42}
			]
		}
	}`)
	m := newValidManifest()
	m.Encryption = Encryption{}
	m.Contents.Workspace = &WorkspaceSummary{ID: "ws_collapse", Slug: "collapse-co", Name: "Collapse Co"}
	m.Contents.Crews = nil
	// Recorded at create time, against the PRE-collapse bundle: 2 crews,
	// 2 issue_counters rows. The payload matches this exactly — only the
	// TARGET-side row count comes in lower, because of the migration's
	// merge, not because anything is missing.
	m.Contents.TableRowCounts = map[string]int{"workspaces": 1, "crews": 2, "issue_counters": 2}
	payload := buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}})
	bundle := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")

	target := openMigratedDBCov(t)
	result, err := RestoreBackup(ctx, target, RestoreOptions{
		Path:  bundle,
		Actor: covAdminActor(),
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	if result.IssueCountersMigrated != 2 {
		t.Fatalf("IssueCountersMigrated = %d, want 2", result.IssueCountersMigrated)
	}
	if len(result.PayloadRowCountMismatches) != 0 {
		t.Errorf("PayloadRowCountMismatches = %+v, want none — the payload matches the manifest", result.PayloadRowCountMismatches)
	}
	if tableRowCountMismatch(result.RowsInsertedShortfalls, "issue_counters") != nil {
		t.Fatalf("RowsInsertedShortfalls reported a false shortfall for issue_counters: %+v\n"+
			"this restore collapsed 2 rows sharing an effective prefix into 1, exactly as the "+
			"pre-#1797 migration is supposed to — that is not a shortfall", result.RowsInsertedShortfalls)
	}

	var workspaceID, prefix string
	var next int64
	if err := target.QueryRowContext(ctx,
		`SELECT workspace_id, prefix, next_number FROM issue_counters WHERE workspace_id = 'ws_collapse'`).
		Scan(&workspaceID, &prefix, &next); err != nil {
		t.Fatalf("issue_counters after restore: %v", err)
	}
	if prefix != "ENG" || next != 42 {
		t.Errorf("issue_counters row = (prefix=%s, next_number=%d), want (ENG, 42) — the higher of the two merged rows", prefix, next)
	}
}

// TestRestoreBackup_PreRekeyIssueCountersGenuineShortfallStillReported is
// the inverse: the fix must be a discount computed from what the
// migration's own transform actually collapsed, not a blanket exemption
// for the table. c_partial resolves and migrates normally (no collapse:
// it is the only row in its group). c_gone's crew_id is not in this
// bundle's crews table and does not exist on the target either, so it
// cannot be migrated — it falls through to the ordinary dropped-column
// path and is not inserted at all. That is a real, unrelated shortfall,
// and must still show up.
func TestRestoreBackup_PreRekeyIssueCountersGenuineShortfallStillReported(t *testing.T) {
	ctx := context.Background()

	dumpJSON := []byte(`{
		"workspace_id": "ws_partial",
		"tables": {
			"workspaces": [{"id": "ws_partial", "name": "Partial Co", "slug": "partial-co"}],
			"crews": [{"id": "c_partial", "workspace_id": "ws_partial", "name": "Eng", "slug": "eng", "issue_prefix": "ENG"}],
			"issue_counters": [
				{"crew_id": "c_partial", "next_number": 10},
				{"crew_id": "c_gone", "next_number": 99}
			]
		}
	}`)
	m := newValidManifest()
	m.Encryption = Encryption{}
	m.Contents.Workspace = &WorkspaceSummary{ID: "ws_partial", Slug: "partial-co", Name: "Partial Co"}
	m.Contents.Crews = nil
	m.Contents.TableRowCounts = map[string]int{"workspaces": 1, "crews": 1, "issue_counters": 2}
	payload := buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}})
	bundle := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")

	target := openMigratedDBCov(t)
	result, err := RestoreBackup(ctx, target, RestoreOptions{
		Path:  bundle,
		Actor: covAdminActor(),
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	if result.IssueCountersMigrated != 1 {
		t.Fatalf("IssueCountersMigrated = %d, want 1 (only c_partial resolves)", result.IssueCountersMigrated)
	}
	want := TableRowCountMismatch{Table: "issue_counters", Recorded: 2, Actual: 1}
	got := tableRowCountMismatch(result.RowsInsertedShortfalls, "issue_counters")
	if got == nil {
		t.Fatalf("RowsInsertedShortfalls has no issue_counters entry, want %+v — c_gone's row genuinely never landed", want)
	}
	if *got != want {
		t.Errorf("RowsInsertedShortfalls[issue_counters] = %+v, want %+v", *got, want)
	}
}

// tableRowCountMismatch returns the entry for table in mismatches, or nil
// if none names it.
func tableRowCountMismatch(mismatches []TableRowCountMismatch, table string) *TableRowCountMismatch {
	for i := range mismatches {
		if mismatches[i].Table == table {
			return &mismatches[i]
		}
	}
	return nil
}
