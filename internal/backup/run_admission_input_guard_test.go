package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func admissionGuardExec(t *testing.T, q interface {
	Exec(string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := q.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func admissionGuardDB(t *testing.T, modern bool) *sql.DB {
	t.Helper()
	db := newReplaceTestDB(t)
	extra := ""
	if modern {
		extra = ",admission_required_format INTEGER NOT NULL DEFAULT 0,admission_id TEXT"
	}
	admissionGuardExec(t, db, `CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT`+extra+`)`)
	admissionGuardExec(t, db, `CREATE TABLE pipelines(id TEXT PRIMARY KEY,workspace_id TEXT)`)
	admissionGuardExec(t, db, `INSERT INTO pipelines VALUES('admission-p','admission-w')`)
	admissionGuardExec(t, db, `INSERT INTO workspaces(id,slug) VALUES('admission-w','admission')`)
	admissionGuardExec(t, db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id) VALUES('admission-r','admission-w','admission-p')`)
	if modern {
		admissionGuardExec(t, db, `CREATE TABLE pipeline_run_admissions(id TEXT PRIMARY KEY,workspace_id TEXT,run_id TEXT,format INTEGER,payload BLOB,created_at TEXT)`)
	}
	return db
}
func admissionGuardSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	all := []any{}
	for _, table := range []string{"workspaces", "pipelines", "pipeline_runs", "pipeline_run_admissions"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		rows, err := db.Query(`SELECT * FROM main.` + quoteIdent(table) + ` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			v := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range v {
				ptr[i] = &v[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			all = append(all, []any{table, v})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestRunAdmissionClosedInputBeforeEveryRawCallback(t *testing.T) {
	cases := map[string]*DBDump{
		"payload":        {Tables: map[string][]map[string]any{"pipeline_run_admissions": {{"id": "synthetic-opaque", "payload": []byte{255}}}}},
		"unknown-empty":  {Tables: map[string][]map[string]any{"pipeline_run_admissions_future": {}}},
		"case-variant":   {Tables: map[string][]map[string]any{"Pipeline_Run_Admissions": {}}},
		"required":       {Tables: map[string][]map[string]any{"pipeline_runs": {{"id": "admission-r", "admission_required_format": 1, "admission_id": nil}}}},
		"nonnull-ref":    {Tables: map[string][]map[string]any{"pipeline_runs": {{"admission_required_format": 0, "admission_id": ""}}}},
		"invalid-format": {Tables: map[string][]map[string]any{"pipeline_runs": {{"admission_required_format": "0", "admission_id": nil}}}},
		"unknown-marker": {Tables: map[string][]map[string]any{"pipeline_runs": {{"admission_future": 0}}}},
		"modern-missing": {Tables: map[string][]map[string]any{"pipeline_run_admissions": {}, "pipeline_runs": {{"id": "admission-r"}}}},
	}
	for name, d := range cases {
		for _, entry := range []string{"plain", "tx", "hooks"} {
			t.Run(name+"/"+entry, func(t *testing.T) {
				db := admissionGuardDB(t, true)
				before := admissionGuardSnapshot(t, db)
				called := false
				var err error
				switch entry {
				case "plain":
					err = RestoreDump(t.Context(), db, d)
				case "tx":
					_, err = RestoreDumpTx(t.Context(), db, d, func(context.Context) error { called = true; return nil })
				case "hooks":
					_, err = RestoreDumpTxHooks(t.Context(), db, d, &RestoreDumpHooks{PreInsert: func(context.Context, *sql.Tx) error { called = true; return nil }})
				}
				if !errors.Is(err, ErrRunAdmissionTransportClosed) || called || admissionGuardSnapshot(t, db) != before {
					t.Fatalf("unsupported input changed state/callback: %v called=%v", err, called)
				}
			})
		}
	}
}

func TestRunAdmissionClosedTransportRetainsUnknownZeroCompatibility(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			db := admissionGuardDB(t, modern)
			before := admissionGuardSnapshot(t, db)
			d := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"pipeline_runs": {{"id": "admission-r", "workspace_id": "admission-w"}}}}
			if modern {
				d.Tables["pipeline_run_admissions"] = []map[string]any{}
				d.Tables["pipeline_runs"][0]["admission_required_format"] = int64(0)
				d.Tables["pipeline_runs"][0]["admission_id"] = nil
			}
			called := false
			_, err := RestoreDumpTxHooks(t.Context(), db, d, &RestoreDumpHooks{PreInsert: func(context.Context, *sql.Tx) error { called = true; return nil }})
			if err != nil || !called || admissionGuardSnapshot(t, db) != before {
				t.Fatalf("UNKNOWN zero transport changed: %v called=%v", err, called)
			}
		})
	}
}
