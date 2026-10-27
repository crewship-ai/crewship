package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestGridCraftRestoreRejectsMalformedRowsWithoutPartialData(t *testing.T) {
	source := openMigratedDBCov(t)
	ws, _ := seedCovWorkspace(t, source, "gridcraft-validation")
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) SELECT 'workbook-validation',?,'workbook-validation','Workbook',id,1,'{}' FROM users LIMIT 1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('workbook-validation',?,1,1,1,'{"sheets":[]}')`, ws); err != nil {
		t.Fatal(err)
	}
	dump, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		modify func(*DBDump)
	}{
		{"invalid snapshot", func(d *DBDump) { d.Tables["gridcraft_heads"][0]["snapshot"] = "invalid" }},
		{"exhausted generation", func(d *DBDump) { d.Tables["gridcraft_heads"][0]["generation"] = int64(math.MaxInt64) }},
		{"missing Page", func(d *DBDump) { delete(d.Tables["gridcraft_heads"][0], "page_id") }},
		{"mismatched receipt", func(d *DBDump) {
			d.Tables["gridcraft_operations"] = []map[string]any{{"workspace_id": ws, "page_id": "workbook-validation", "authority_id": "human", "mutation_id": "mutation", "payload_hash": "original", "state": "applied", "token": int64(1), "lease_until": int64(0), "receipt": `{"operation_key":{"authority_id":"other","mutation_id":"mutation"},"payload_hash":"original","outcome":"applied","rev":1}`}}
		}},
		{"exhausted candidate", func(d *DBDump) {
			d.Tables["gridcraft_operations"] = []map[string]any{{"workspace_id": ws, "page_id": "workbook-validation", "authority_id": "human", "mutation_id": "mutation", "payload_hash": "original", "state": "in_progress", "token": int64(math.MaxInt64), "lease_until": int64(1)}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(dump)
			if err != nil {
				t.Fatal(err)
			}
			d, err := UnmarshalDump(raw)
			if err != nil {
				t.Fatal(err)
			}
			tt.modify(d)
			target := openMigratedDBCov(t)
			if err := RestoreDump(t.Context(), target, d); err == nil {
				t.Fatal("invalid workbook accepted")
			}
			var count int
			if err := target.QueryRow(`SELECT count(*) FROM gridcraft_heads`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial workbook restored: count=%d err=%v", count, err)
			}
		})
	}
}

func TestGridCraftPhysicalFenceRefusesCounterOverflowAtomically(t *testing.T) {
	for _, column := range []string{"generation", "token"} {
		t.Run(column, func(t *testing.T) {
			db := openMigratedDBCov(t)
			ws, _ := seedCovWorkspace(t, db, "gridcraft-counter-"+column)
			if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) SELECT 'counter-workbook',?,'counter-workbook','Workbook',id,1,'{}' FROM users LIMIT 1`, ws); err != nil {
				t.Fatal(err)
			}
			generation, token := int64(1), int64(1)
			if column == "generation" {
				generation = math.MaxInt64
			} else {
				token = math.MaxInt64
			}
			if _, err := db.Exec(`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('counter-workbook',?,1,1,?,'{"sheets":[]}')`, ws, generation); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until) VALUES(?,'counter-workbook','human','pending','hash','in_progress',?,1)`, ws, token); err != nil {
				t.Fatal(err)
			}
			if err := resetRecoveredGridCraft(t.Context(), db); err == nil || !strings.Contains(err.Error(), "counter exhausted") {
				t.Fatalf("overflow accepted: %v", err)
			}
			var afterGeneration, afterToken int64
			if err := db.QueryRow(`SELECT generation FROM gridcraft_heads`).Scan(&afterGeneration); err != nil || afterGeneration != generation {
				t.Fatalf("generation changed: %d %v", afterGeneration, err)
			}
			if err := db.QueryRow(`SELECT token FROM gridcraft_operations`).Scan(&afterToken); err != nil || afterToken != token {
				t.Fatalf("token changed: %d %v", afterToken, err)
			}
		})
	}
}

func TestGridCraftRestoreRefusesHistoryCollisionBeforeReplace(t *testing.T) {
	db := openMigratedDBCov(t)
	ws, _ := seedCovWorkspace(t, db, "gridcraft-collision")
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) SELECT 'collision-workbook',?,'collision-workbook','Workbook',id,1,'{}' FROM users LIMIT 1`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('collision-workbook',?,1,1,17,'{"sheets":[]}')`, ws); err != nil {
		t.Fatal(err)
	}
	dump, err := DumpWorkspace(t.Context(), db, ws)
	if err != nil {
		t.Fatal(err)
	}
	dump.Tables["gridcraft_heads"][0]["generation"] = int64(1)
	replaced := false
	_, err = RestoreDumpTxHooks(t.Context(), db, dump, &RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
		replaced = true
		_, err := tx.ExecContext(ctx, `DELETE FROM gridcraft_heads WHERE page_id='collision-workbook'`)
		return err
	}})
	if err == nil || !strings.Contains(err.Error(), "workbook history merge") {
		t.Fatalf("unqualified history merge accepted: %v", err)
	}
	if replaced {
		t.Fatal("destructive replace ran before workbook collision refusal")
	}
	var generation int64
	if err := db.QueryRow(`SELECT generation FROM gridcraft_heads WHERE page_id='collision-workbook'`).Scan(&generation); err != nil || generation != 17 {
		t.Fatalf("target fence changed: %d %v", generation, err)
	}
}
