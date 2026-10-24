package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func rawBackupDigest(v []byte) string {
	d := sha256.Sum256(v)
	return "sha256:" + hex.EncodeToString(d[:])
}
func seedRawDataBackup(t *testing.T, db *sql.DB, name, page string, raw []byte) string {
	t.Helper()
	ws, _ := seedCovWorkspace(t, db, name)
	actor := "u_cov_" + name
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'OWNER')`, []any{"member-" + page, ws, actor}},
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Raw data',?,'{}')`, []any{page, ws, page, actor}},
		{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'wordcraft','1','MA==')`, []any{page, ws}},
		{`INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,result_json) VALUES(?,?,'saved','sha256:synthetic','{"status":"saved"}'),(?,?,'legacy','sha256:synthetic','{"status":"saved"}')`, []any{page, ws, page, ws}},
		{`INSERT INTO mooring_collab_raw_changes(page_id,workspace_id,change_id,format,protocol_version,model_version,engine_version,raw_atomic,raw_digest,canonical_atomic,canonical_digest) VALUES(?,?,'saved','atomic-change-data.v1','0.4.0','0.2.0','0.10.0',?,?,?,?)`, []any{page, ws, raw, rawBackupDigest(raw), []byte("canonical"), rawBackupDigest([]byte("canonical"))}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

func TestMooringRawDataPortableExactAndReplaceRollback(t *testing.T) {
	source := openMigratedDBCov(t)
	raw := []byte("synthetic exact data \x00\xff\xfe 😀")
	ws := seedRawDataBackup(t, source, "raw-data", "source-raw", raw)
	other := seedRawDataBackup(t, source, "raw-foreign", "foreign-raw", []byte("not this workspace"))
	_ = other
	dump, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["mooring_collab_raw_changes"]) != 1 {
		t.Fatal("raw table not scoped/included")
	}
	wire, err := MarshalDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalDump(wire)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDBCov(t)
	seedRawDataBackup(t, target, "raw-data", "target-raw", []byte("target old data"))
	replace := func(ctx context.Context, tx *sql.Tx) error {
		_, err := ReplaceWorkspaceContents(ctx, tx, ws, "raw-data")
		return err
	}
	rollbackMarker := errors.New("raw-data test rollback after restore")
	if _, err = RestoreDumpTxHooks(t.Context(), target, restored, &RestoreDumpHooks{PreInsert: replace, PostInsert: func(context.Context, *sql.Tx) error { return rollbackMarker }}); !errors.Is(err, rollbackMarker) {
		t.Fatal("actual restore rollback", err)
	}
	var got []byte
	if err = target.QueryRow(`SELECT raw_atomic FROM mooring_collab_raw_changes WHERE page_id='target-raw'`).Scan(&got); err != nil || string(got) != "target old data" {
		t.Fatal("rollback lost target", err)
	}
	if _, err = RestoreDumpTxHooks(t.Context(), target, restored, &RestoreDumpHooks{PreInsert: replace}); err != nil {
		t.Fatal("replace restore", err)
	}
	if err = target.QueryRow(`SELECT raw_atomic FROM mooring_collab_raw_changes WHERE page_id='source-raw'`).Scan(&got); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("portable bytes changed", err)
	}
	var count int
	if err = target.QueryRow(`SELECT count(*) FROM mooring_collab_raw_changes WHERE page_id IN ('target-raw','foreign-raw') OR change_id='legacy'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("replacement/scope/legacy changed", count, err)
	}
}

func TestMooringRawDataRestoreCannotSilentlyDropRowsOrSchema(t *testing.T) {
	source := openMigratedDBCov(t)
	ws := seedRawDataBackup(t, source, "raw-invalid", "raw-invalid-page", []byte("original"))
	original, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalDump(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-table", "missing-column", "empty-binary", "oversized-binary", "bad-digest", "mismatched-digest", "missing-required"} {
		t.Run(mode, func(t *testing.T) {
			db := openMigratedDBCov(t)
			dump, err := UnmarshalDump(encoded)
			if err != nil {
				t.Fatal(err)
			}
			row := dump.Tables["mooring_collab_raw_changes"][0]
			switch mode {
			case "missing-table":
				_, err = db.Exec(`DROP TABLE mooring_collab_raw_changes`)
			case "missing-column":
				_, err = db.Exec(`ALTER TABLE mooring_collab_raw_changes DROP COLUMN engine_version`)
			case "empty-binary":
				row["raw_atomic"] = map[string]any{"encoding": "document-base64-v1", "data": ""}
			case "oversized-binary":
				row["raw_atomic"] = map[string]any{"encoding": "document-base64-v1", "data": strings.Repeat("A", ((maxMooringRawDataBytes+2)/3)*4+4)}
			case "bad-digest":
				row["raw_digest"] = "sha256:bad"
			case "mismatched-digest":
				row["raw_digest"] = "sha256:" + strings.Repeat("0", 64)
			case "missing-required":
				delete(row, "format")
			}
			if err != nil {
				t.Fatal("fixture", err)
			}
			if err = RestoreDump(t.Context(), db, dump); err == nil {
				t.Fatal("restore silently lost required raw data")
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id='raw-invalid-page'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed restore leaked content", count, err)
			}
		})
	}
}
