package backup

// Tests for #2009: Verify checks integrity (the checksum) but, before this
// change, had nothing to compare row counts against and so could not tell
// "the bundle is empty because the source was empty" from "the bundle is
// empty because a scoping bug dropped every row". These tests build bundles
// by hand — manifest.Contents.TableRowCounts set independently from the
// payload's actual db/dump.json — so the divergence these tests exercise is
// exactly what a corrupted manifest, a hand edit, or a create-side bug that
// computed the two numbers from different sources would produce.
//
// None of this compiles against pre-#2009 code: Manifest.Contents had no
// TableRowCounts field, VerifyResult had no CompletenessChecked /
// TableRowCountMismatches fields, and Verify's only behaviour was the
// checksum check. The capability these tests exercise did not exist.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// dumpJSONWithCounts builds a db/dump.json payload carrying exactly the
// given number of (content-irrelevant) rows per table — enough to drive
// tableRowCounts and compareRowCounts without needing schema-valid content,
// since these tests exercise Verify's completeness comparison, not restore.
func dumpJSONWithCounts(t *testing.T, counts map[string]int) []byte {
	t.Helper()
	dump := &DBDump{WorkspaceID: "ws_1", Tables: map[string][]map[string]any{}}
	for table, n := range counts {
		rows := make([]map[string]any, n)
		for i := range rows {
			rows[i] = map[string]any{"id": fmt.Sprintf("%s_%d", table, i)}
		}
		dump.Tables[table] = rows
	}
	data, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal dump: %v", err)
	}
	return data
}

// completenessTestBundle writes a bundle whose manifest claims recorded row
// counts and whose payload actually carries actual row counts — the two
// parameters are independent on purpose, so a test can construct any
// divergence it wants. encrypted, when true, seals the payload with a
// passphrase instead of writing it plain.
func completenessTestBundle(t *testing.T, recorded, actual map[string]int, encrypted bool) string {
	t.Helper()
	m := newValidManifest()
	m.Contents.TableRowCounts = recorded
	var entries []payloadEntry
	if actual != nil {
		entries = []payloadEntry{{name: "db/dump.json", body: dumpJSONWithCounts(t, actual)}}
	}
	payload := buildPayloadTarZst(t, entries)
	opts := WriteBundleOptions{NoEncrypt: true}
	m.Encryption = Encryption{Enabled: false}
	if encrypted {
		opts = WriteBundleOptions{Passphrase: "hunter2"}
		// writeRawBundle seals per opts but does not itself set
		// manifest.Encryption (unlike CreateBackup) — set it here so the
		// manifest actually reflects the payload it wraps, exactly as a
		// real create would leave it.
		m.Encryption = Encryption{Enabled: true, Algorithm: EncryptionAlgorithm, KeyDerivation: "scrypt"}
	}
	return writeRawBundle(t, t.TempDir(), m, payload, opts, "")
}

func TestVerify_CompleteBundlePassesCompletenessCheck(t *testing.T) {
	counts := map[string]int{"missions": 3, "crews": 1}
	path := completenessTestBundle(t, counts, counts, false)

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Errorf("expected Valid=true, err=%v", res.Err)
	}
	if !res.CompletenessChecked {
		t.Errorf("expected CompletenessChecked=true for an unencrypted post-#2009 bundle")
	}
	if len(res.TableRowCountMismatches) != 0 {
		t.Errorf("expected no mismatches, got %+v", res.TableRowCountMismatches)
	}
	if res.CompletenessSkipReason != "" {
		t.Errorf("expected empty skip reason when checked, got %q", res.CompletenessSkipReason)
	}
}

// TestVerify_IncompleteBundleFailsCompletenessCheck is #2009's headline
// scenario: a bundle whose payload carries FEWER rows for a table than the
// manifest recorded — the #1973 shape ("dumped zero mission_tasks rows")
// reproduced directly at the manifest/payload boundary. Before this fix
// Verify had no way to notice; now it must report INVALID, not silently
// pass.
func TestVerify_IncompleteBundleFailsCompletenessCheck(t *testing.T) {
	recorded := map[string]int{"missions": 30, "crews": 1}
	actual := map[string]int{"missions": 0, "crews": 1} // scoping bug dropped every mission
	path := completenessTestBundle(t, recorded, actual, false)

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Valid {
		t.Error("expected Valid=false on a bundle whose payload is short of what the manifest recorded")
	}
	if !res.CompletenessChecked {
		t.Errorf("expected CompletenessChecked=true")
	}
	if !errors.Is(res.Err, ErrIncompleteBundle) {
		t.Errorf("expected Err to wrap ErrIncompleteBundle, got %v", res.Err)
	}
	want := []TableRowCountMismatch{{Table: "missions", Recorded: 30, Actual: 0}}
	if !sameTableRowCountMismatches(res.TableRowCountMismatches, want) {
		t.Errorf("TableRowCountMismatches = %+v, want %+v", res.TableRowCountMismatches, want)
	}
}

// TestVerify_EncryptedBundleSkipsCompletenessCheck pins the deliberate
// scope decision: Verify never decrypts, so an encrypted bundle's
// completeness is reported as unverifiable — never as confirmed complete,
// and never as a failure just because it could not be checked.
func TestVerify_EncryptedBundleSkipsCompletenessCheck(t *testing.T) {
	counts := map[string]int{"missions": 3}
	path := completenessTestBundle(t, counts, counts, true)

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Errorf("checksum should still pass on an encrypted bundle: err=%v", res.Err)
	}
	if res.CompletenessChecked {
		t.Error("expected CompletenessChecked=false for an encrypted bundle")
	}
	if !strings.Contains(res.CompletenessSkipReason, "encrypted") {
		t.Errorf("skip reason = %q, want it to mention encryption", res.CompletenessSkipReason)
	}
	if !strings.Contains(res.CompletenessSkipReason, "dry-run") {
		t.Errorf("skip reason = %q, want it to point at the restore --dry-run alternative", res.CompletenessSkipReason)
	}
}

// TestVerify_OldBundleWithoutRowCountsSkipsCompletenessCheck is the other
// deliberate scope decision from #2009: a bundle written before this
// feature existed carries no TableRowCounts at all. Verify must not start
// failing on it, and must not claim it confirmed completeness either.
func TestVerify_OldBundleWithoutRowCountsSkipsCompletenessCheck(t *testing.T) {
	// recorded is nil — exactly what an old manifest looks like.
	path := completenessTestBundle(t, nil, map[string]int{"missions": 3}, false)

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Error("a pre-#2009 bundle must not start failing verify")
	}
	if res.CompletenessChecked {
		t.Error("expected CompletenessChecked=false when the manifest carries no counts")
	}
	if !strings.Contains(res.CompletenessSkipReason, "predates") {
		t.Errorf("skip reason = %q, want it to mention the bundle predating row-count recording", res.CompletenessSkipReason)
	}
}

// TestVerify_BundleWithNoDBSectionSkipsCompletenessCheck covers a bundle
// whose payload legitimately carries no db/dump.json at all (instance-scope
// backups, or a files-only resume payload).
func TestVerify_BundleWithNoDBSectionSkipsCompletenessCheck(t *testing.T) {
	path := completenessTestBundle(t, map[string]int{"missions": 3}, nil, false)

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Errorf("expected Valid=true: err=%v", res.Err)
	}
	if res.CompletenessChecked {
		t.Error("expected CompletenessChecked=false when the payload has no db/dump.json")
	}
	if !strings.Contains(res.CompletenessSkipReason, "no db/dump.json") {
		t.Errorf("skip reason = %q, want it to mention the missing section", res.CompletenessSkipReason)
	}
}

// TestVerify_CompletenessScanDoesNotCorruptChecksum guards the fragile part
// of this implementation: the completeness scan and the checksum both read
// from the SAME underlying reader (see Verify's doc comment on hashed). A
// scanner that stops as soon as it finds db/dump.json — this one does, by
// design — must not leave trailing tar entries unhashed. db/dump.json here
// is deliberately NOT the last entry in the payload.
func TestVerify_CompletenessScanDoesNotCorruptChecksum(t *testing.T) {
	m := newValidManifest()
	m.Encryption = Encryption{}
	m.Contents.TableRowCounts = map[string]int{"missions": 2}
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "devcontainer/alpha/devcontainer.json", body: []byte(`{"a":1}`)},
		{name: "db/dump.json", body: dumpJSONWithCounts(t, map[string]int{"missions": 2})},
		{name: "mise/alpha/mise.toml", body: []byte("some = 1")},
	})
	path := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")

	res, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid {
		t.Fatalf("checksum must still match with entries following db/dump.json: err=%v", res.Err)
	}
	if !res.CompletenessChecked || len(res.TableRowCountMismatches) != 0 {
		t.Errorf("expected a clean completeness check, got checked=%v mismatches=%+v",
			res.CompletenessChecked, res.TableRowCountMismatches)
	}
}

func TestCompareRowCounts(t *testing.T) {
	tests := []struct {
		name             string
		recorded, actual map[string]int
		want             []TableRowCountMismatch
	}{
		{
			name:     "nil recorded produces no comparison",
			recorded: nil,
			actual:   map[string]int{"a": 1},
			want:     nil,
		},
		{
			name:     "matching counts",
			recorded: map[string]int{"a": 1, "b": 2},
			actual:   map[string]int{"a": 1, "b": 2},
			want:     nil,
		},
		{
			name:     "shortfall",
			recorded: map[string]int{"a": 5},
			actual:   map[string]int{"a": 3},
			want:     []TableRowCountMismatch{{Table: "a", Recorded: 5, Actual: 3}},
		},
		{
			name:     "table absent from actual counts as zero",
			recorded: map[string]int{"a": 5},
			actual:   map[string]int{},
			want:     []TableRowCountMismatch{{Table: "a", Recorded: 5, Actual: 0}},
		},
		{
			name:     "table present in actual but not recorded is not a gap",
			recorded: map[string]int{"a": 1},
			actual:   map[string]int{"a": 1, "b": 99},
			want:     nil,
		},
		{
			name:     "deterministic sorted order across multiple mismatches",
			recorded: map[string]int{"z": 1, "a": 1},
			actual:   map[string]int{},
			want: []TableRowCountMismatch{
				{Table: "a", Recorded: 1, Actual: 0},
				{Table: "z", Recorded: 1, Actual: 0},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compareRowCounts(tc.recorded, tc.actual)
			if !sameTableRowCountMismatches(got, tc.want) {
				t.Errorf("compareRowCounts(%v, %v) = %+v, want %+v", tc.recorded, tc.actual, got, tc.want)
			}
		})
	}
}

// TestCreateBackup_RecordsTableRowCounts is the create-side half: a real
// CreateBackup against a seeded workspace must populate
// Manifest.Contents.TableRowCounts from the actual dump, and Verify against
// that same bundle must then confirm it clean — the whole feature, wired
// together end to end with real data instead of hand-built fixtures.
func TestCreateBackup_RecordsTableRowCounts(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, source, "rowcounts")

	created, err := CreateBackup(ctx, source, CreateOptions{
		Scope:       ScopeWorkspace,
		WorkspaceID: wsID,
		OutputDir:   t.TempDir(),
		Actor:       covAdminActor(),
		NoEncrypt:   true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if len(created.Manifest.Contents.TableRowCounts) == 0 {
		t.Fatal("expected TableRowCounts to be populated by CreateBackup")
	}
	if n := created.Manifest.Contents.TableRowCounts["workspaces"]; n != 1 {
		t.Errorf(`TableRowCounts["workspaces"] = %d, want 1`, n)
	}
	if n := created.Manifest.Contents.TableRowCounts["crews"]; n != 1 {
		t.Errorf(`TableRowCounts["crews"] = %d, want 1`, n)
	}

	res, err := Verify(ctx, created.Path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Valid || !res.CompletenessChecked || len(res.TableRowCountMismatches) != 0 {
		t.Errorf("expected a clean completeness-checked VALID result, got valid=%v checked=%v mismatches=%+v err=%v",
			res.Valid, res.CompletenessChecked, res.TableRowCountMismatches, res.Err)
	}
}

// TestRestoreBackup_PayloadRowCountMismatchIsReported drives the restore
// side of the same comparison Verify makes, through the real runner. The
// manifest is hand-edited to claim more workspaces rows than the payload
// actually carries — RestoreBackup already has the decrypted dump for
// other reasons, so it can (and must) make the same comparison Verify does,
// without needing a passphrase-free bundle to do it.
func TestRestoreBackup_PayloadRowCountMismatchIsReported(t *testing.T) {
	ctx := context.Background()

	dumpJSON := []byte(`{
		"workspace_id": "ws_pmc",
		"tables": {
			"workspaces": [{"id": "ws_pmc", "name": "Payload Mismatch Co", "slug": "payload-mismatch-co"}]
		}
	}`)
	m := newValidManifest()
	m.Encryption = Encryption{}
	m.Contents.Workspace = &WorkspaceSummary{ID: "ws_pmc", Slug: "payload-mismatch-co", Name: "Payload Mismatch Co"}
	m.Contents.Crews = nil
	// Claims 5 workspaces rows; the payload actually carries 1.
	m.Contents.TableRowCounts = map[string]int{"workspaces": 5}
	payload := buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}})
	bundle := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")

	for _, tc := range []struct {
		name   string
		dryRun bool
	}{
		{name: "dry run", dryRun: true},
		{name: "committed", dryRun: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged []string
			result, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
				Path:   bundle,
				Actor:  covAdminActor(),
				DryRun: tc.dryRun,
				Logger: func(msg string) { logged = append(logged, msg) },
			})
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			want := []TableRowCountMismatch{{Table: "workspaces", Recorded: 5, Actual: 1}}
			if !sameTableRowCountMismatches(result.PayloadRowCountMismatches, want) {
				t.Fatalf("PayloadRowCountMismatches = %+v, want %+v", result.PayloadRowCountMismatches, want)
			}
			joined := strings.Join(logged, "\n")
			if !strings.Contains(joined, "payload row counts") || !strings.Contains(joined, "workspaces") {
				t.Errorf("operator warning missing from:\n%s", joined)
			}
		})
	}
}

// TestRestoreBackup_RowsInsertedShortfallIsReported covers the OTHER
// comparison: the payload matches what the manifest recorded (so
// PayloadRowCountMismatches is clean), but a PK collision on the target
// means fewer rows land than the bundle carries. RowsInsertedShortfalls is
// what catches that a specific table came up short, on top of the existing
// aggregate no-op detection.
func TestRestoreBackup_RowsInsertedShortfallIsReported(t *testing.T) {
	ctx := context.Background()
	target := openMigratedDBCov(t)

	// Pre-seed the target with a workspace under the SAME id the bundle
	// will carry, so the bundle's own workspaces row collides and
	// INSERT OR IGNORE drops it — while a NEW crew row referencing that
	// (already-present) workspace id lands cleanly, keeping this off the
	// aggregate no-op-restore path.
	const wsID = "ws_shortfall"
	if _, err := target.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "Shortfall Co", "shortfall-co"); err != nil {
		t.Fatalf("pre-seed workspace: %v", err)
	}

	dumpJSON := fmt.Sprintf(`{
		"workspace_id": %q,
		"tables": {
			"workspaces": [{"id": %q, "name": "Shortfall Co (bundle copy)", "slug": "shortfall-co"}],
			"crews": [{"id": "c_shortfall", "workspace_id": %q, "name": "New Crew", "slug": "new-crew"}]
		}
	}`, wsID, wsID, wsID)
	m := newValidManifest()
	m.Encryption = Encryption{}
	m.Contents.Workspace = &WorkspaceSummary{ID: wsID, Slug: "shortfall-co", Name: "Shortfall Co"}
	m.Contents.Crews = nil
	// Matches the payload exactly — PayloadRowCountMismatches must be
	// clean; only the TARGET-side insert is short.
	m.Contents.TableRowCounts = map[string]int{"workspaces": 1, "crews": 1}
	payload := buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: []byte(dumpJSON)}})
	bundle := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")

	var logged []string
	result, err := RestoreBackup(ctx, target, RestoreOptions{
		Path:   bundle,
		Actor:  covAdminActor(),
		Logger: func(msg string) { logged = append(logged, msg) },
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if len(result.PayloadRowCountMismatches) != 0 {
		t.Errorf("PayloadRowCountMismatches = %+v, want none — the payload matches the manifest", result.PayloadRowCountMismatches)
	}
	want := []TableRowCountMismatch{{Table: "workspaces", Recorded: 1, Actual: 0}}
	if !sameTableRowCountMismatches(result.RowsInsertedShortfalls, want) {
		t.Fatalf("RowsInsertedShortfalls = %+v, want %+v", result.RowsInsertedShortfalls, want)
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "rows inserted") || !strings.Contains(joined, "workspaces") {
		t.Errorf("operator warning missing from:\n%s", joined)
	}
	// The crew row landed fine despite the workspace collision.
	var crewCount int
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM crews WHERE id = 'c_shortfall'`).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("expected the new crew row to land, got count=%d", crewCount)
	}
}

func sameTableRowCountMismatches(got, want []TableRowCountMismatch) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
