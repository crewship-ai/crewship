package api

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Capture entire canonical rows, including original IDs/issuers/times and
// pre-existing audit/selection records, rather than assuming empty tables.
func presetApplyAtomicRows(t *testing.T, db *sql.DB, query string, arg any) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, arg)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := [][]any{}
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		row := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Record/writer/journal one-row guards are already prerequisites. These API
// cases prove their composition and rollback; they are not new unit RED claims.
func TestPresetApplyIgnoredInsertIntegrationRollback(t *testing.T) {
	for _, target := range []string{"selection", "managed-add", "managed-reissue", "journal"} {
		t.Run(target, func(t *testing.T) {
			f := newPresetApplyFixture(t)
			execOrFatal(t, f.db, `INSERT INTO users(id,email) VALUES('ignore-issuer','issuer@ignore.invalid')`)
			execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ignore-issuer-member',?,'ignore-issuer','ADMIN')`, f.workspace)
			m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			rights := []access.Right{{Kind: "project", ID: "apply-other", Operation: "list"}, {Kind: "project", ID: "apply-other", Operation: "read"}, {Kind: "project", ID: "apply-resource", Operation: "list"}}
			if target != "managed-add" {
				rights = append(rights, access.Right{Kind: "project", ID: "apply-resource", Operation: "read"}, access.Right{Kind: "project", ID: "apply-resource", Operation: "manage"})
			}
			m, err = f.store.Replace(t.Context(), "ignore-issuer", "apply-subject", f.workspace, "restricted", m, rights)
			if err != nil {
				t.Fatal(err)
			}
			f.request.Revision = m.Revision
			// Nonempty unrelated choice and audit must survive every failure.
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			previous := f.request
			previous.Resource.ID = "apply-other"
			if err := f.store.RecordPresetSelectionIn(t.Context(), tx, f.owner, "apply-subject", f.workspace, m, previous); err != nil {
				t.Fatal(err)
			}
			notify, err := journal.AppendTx(t.Context(), tx, journal.Entry{WorkspaceID: f.workspace, Type: journal.EntryMemberAccessChanged, ActorType: journal.ActorUser, ActorID: f.owner, Summary: "Existing unrelated selection"})
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if notify != nil {
				notify()
			}
			if target == "managed-add" {
				f.request.Level = "manage"
			} else if target == "managed-reissue" {
				f.request.Reissue = []access.Right{{Kind: "project", ID: "apply-resource", Operation: "read"}}
			}
			preview := f.preview(t)
			wanted := "remove"
			if target == "managed-add" {
				wanted = "add"
			} else if target == "managed-reissue" {
				wanted = "reissue"
			}
			found := false
			for _, change := range preview.Changes {
				if change.Action == wanted && (wanted == "remove" || change.Right.Operation == "read") {
					found = true
				}
			}
			if !found {
				t.Fatalf("fixture does not exercise %s: %+v", wanted, preview.Changes)
			}
			snapshot := func() any {
				t.Helper()
				policy, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
				if err != nil {
					t.Fatal(err)
				}
				return struct {
					Policy access.Policy
					Rows   []string
				}{policy, []string{
					presetApplyAtomicRows(t, f.db, `SELECT * FROM access_grants WHERE member_id=? ORDER BY id`, m.ID),
					presetApplyAtomicRows(t, f.db, `SELECT * FROM access_preset_selections WHERE workspace_id=? ORDER BY id`, f.workspace),
					presetApplyAtomicRows(t, f.db, `SELECT * FROM journal_entries WHERE workspace_id=? ORDER BY id`, f.workspace),
				}}
			}
			before := snapshot()
			trigger := `CREATE TRIGGER ignore_apply_insert BEFORE INSERT ON access_preset_selections WHEN NEW.project_id='apply-resource' BEGIN SELECT RAISE(IGNORE); END`
			wantStatus := 409
			if target == "journal" {
				trigger = `CREATE TRIGGER ignore_apply_insert BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(IGNORE); END`
				wantStatus = 500
			} else if target == "managed-add" || target == "managed-reissue" {
				trigger = `CREATE TRIGGER ignore_apply_insert BEFORE INSERT ON access_grants WHEN NEW.member_id='apply-membership' AND NEW.project_id='apply-resource' AND NEW.operation='read' BEGIN SELECT RAISE(IGNORE); END`
			}
			execOrFatal(t, f.db, trigger)
			response := f.apply(t, applyBody(t, preview))
			if response.Code != wantStatus {
				t.Fatalf("ignored %s INSERT: status=%d want=%d body=%s", target, response.Code, wantStatus, response.Body.String())
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("ignored %s committed grants/revision/selection/audit or rewrote unrelated provenance: before=%+v after=%+v", target, before, after)
			}
		})
	}
}
