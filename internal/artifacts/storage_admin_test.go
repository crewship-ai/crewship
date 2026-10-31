package artifacts

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStorageSourceSQLMatchesMigration(t *testing.T) {
	raw, err := os.ReadFile("../database/migrations/20261030224950_artifact_aggregate_storage_budget.sql")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "CREATE TEMP TABLE artifact_storage_seed AS "
	var seed string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			seed = strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";")
		}
	}
	if seed == "" || seed != storageSourceBytesSQL {
		t.Fatal("storageSourceBytesSQL no longer matches the migration seed; update both together")
	}
}

func TestRecomputeStorageRepairsDriftIdempotentlyWithoutDeletingData(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, strings.Repeat("a", 600)), a); err != nil {
		t.Fatal(err)
	}
	var trueUsed int64
	if err := db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws'`).Scan(&trueUsed); err != nil || trueUsed < 600 {
		t.Fatalf("fixture counter %d %v", trueUsed, err)
	}
	var rowsBefore int
	_ = db.QueryRow(`SELECT (SELECT count(*) FROM artifact_versions)+(SELECT count(*) FROM artifact_version_blobs)`).Scan(&rowsBefore)

	cases := []struct {
		name      string
		damage    []string
		wantWSMax int64 // 0: not asserted
	}{
		{"counters too low", []string{`UPDATE artifact_storage_workspaces SET used_bytes=1`, `UPDATE artifact_storage_global SET used_bytes=1`}, 0},
		{"counters too low under a limit below true usage", []string{`UPDATE artifact_storage_workspaces SET used_bytes=1`, `UPDATE artifact_storage_global SET used_bytes=1`, `UPDATE artifact_storage_workspaces SET max_bytes=500`, `UPDATE artifact_storage_global SET max_bytes=500`}, 500},
		{"counters too high", []string{`UPDATE artifact_storage_workspaces SET used_bytes=5000`, `UPDATE artifact_storage_global SET used_bytes=7000`}, 0},
		{"missing workspace row", []string{`DELETE FROM artifact_storage_workspaces`, `UPDATE artifact_storage_global SET used_bytes=0`}, 0},
		{"stale reservation counter", []string{`UPDATE artifact_storage_workspaces SET reserved_bytes=99`, `UPDATE artifact_storage_global SET reserved_bytes=99`}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wsLimit, instLimit int64
			for _, q := range []string{`UPDATE artifact_storage_global SET max_bytes=68719476736`, `UPDATE artifact_storage_workspaces SET max_bytes=34359738368`} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range c.damage {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.QueryRow(`SELECT workspace_max_bytes,max_bytes FROM artifact_storage_global WHERE id=1`).Scan(&wsLimit, &instLimit); err != nil {
				t.Fatal(err)
			}

			var preUsed, preReserved int64
			_ = db.QueryRow(`SELECT used_bytes,reserved_bytes FROM artifact_storage_global WHERE id=1`).Scan(&preUsed, &preReserved)
			dry, err := RecomputeStorage(context.Background(), db.DB, true, nil)
			if err != nil || !dry.Changed || !dry.DryRun {
				t.Fatalf("dry run %+v %v", dry, err)
			}
			var postUsed, postReserved int64
			_ = db.QueryRow(`SELECT used_bytes,reserved_bytes FROM artifact_storage_global WHERE id=1`).Scan(&postUsed, &postReserved)
			if postUsed != preUsed || postReserved != preReserved {
				t.Fatalf("dry run wrote counters: %d/%d -> %d/%d", preUsed, preReserved, postUsed, postReserved)
			}
			first, err := RecomputeStorage(context.Background(), db.DB, false, nil)
			if err != nil || !first.Changed || first.DataDeleted {
				t.Fatalf("recompute %+v %v", first, err)
			}
			if first.Instance.UsedAfter != trueUsed || first.Instance.ReservedAfter != 0 {
				t.Fatalf("after %+v want used %d", first.Instance, trueUsed)
			}
			second, err := RecomputeStorage(context.Background(), db.DB, false, nil)
			if err != nil || second.Changed || second.WorkspacesDrifted != 0 || len(second.Workspaces) != 0 {
				t.Fatalf("not idempotent %+v %v", second, err)
			}
			var gotWS, gotInst, used, ws, wsMax int64
			_ = db.QueryRow(`SELECT workspace_max_bytes,max_bytes,used_bytes FROM artifact_storage_global WHERE id=1`).Scan(&gotWS, &gotInst, &used)
			if gotWS != wsLimit || gotInst != instLimit || used != trueUsed {
				t.Fatalf("limits or counter changed: %d %d %d", gotWS, gotInst, used)
			}
			if err := db.QueryRow(`SELECT used_bytes,max_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws'`).Scan(&ws, &wsMax); err != nil || ws != trueUsed || wsMax <= 0 || c.wantWSMax != 0 && wsMax != c.wantWSMax {
				t.Fatalf("workspace row %d %d %v", ws, wsMax, err)
			}
			var rowsAfter int
			_ = db.QueryRow(`SELECT (SELECT count(*) FROM artifact_versions)+(SELECT count(*) FROM artifact_version_blobs)`).Scan(&rowsAfter)
			if rowsAfter != rowsBefore {
				t.Fatalf("recompute changed data rows: %d -> %d", rowsBefore, rowsAfter)
			}
		})
	}
}

func TestSetStorageLimitsTable(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "x"), a); err != nil {
		t.Fatal(err)
	}
	i := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		name         string
		ws, inst     *int64
		wantErr      bool
		wantWS, want int64
	}{
		{"both", i(5 << 20), i(9 << 20), false, 5 << 20, 9 << 20},
		{"only instance keeps workspace", nil, i(11 << 20), false, 5 << 20, 11 << 20},
		{"none", nil, nil, true, 0, 0},
		{"too small", i(10), nil, true, 0, 0},
		{"too large", nil, i(MaxStorageLimitBytes + 1), true, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			_, after, err := SetStorageLimits(context.Background(), tx, c.ws, c.inst)
			if c.wantErr {
				_ = tx.Rollback()
				if !errors.Is(err, ErrStorageLimit) {
					t.Fatalf("want ErrStorageLimit, got %v", err)
				}
				return
			}
			if err != nil || tx.Commit() != nil {
				t.Fatal(err)
			}
			if after.WorkspaceLimitBytes != c.wantWS || after.InstanceLimitBytes != c.want {
				t.Fatalf("after %+v", after)
			}
			var rowMax int64
			if err := db.QueryRow(`SELECT max_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws'`).Scan(&rowMax); err != nil || rowMax != c.wantWS {
				t.Fatalf("workspace row limit %d %v", rowMax, err)
			}
		})
	}
}
