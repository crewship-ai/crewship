package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func rawDataDigest(v []byte) string {
	d := sha256.Sum256(v)
	return "sha256:" + hex.EncodeToString(d[:])
}

// SQL storage only; synthetic bytes below are not original-source qualification.
func TestMooringRawAtomicDataStorageGuardsAndContentLifecycle(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, q := range []string{
		`PRAGMA recursive_triggers=OFF`,
		`INSERT INTO workspaces(id,name,slug) VALUES('raw-ws','Raw','raw-ws'),('raw-other','Other','raw-other')`,
		`INSERT INTO users(id,email) VALUES('raw-owner','raw-owner@synthetic.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('raw-member','raw-ws','raw-owner','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('raw-page','raw-ws','raw-page','Raw','raw-owner','{}')`,
		`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('raw-page','raw-ws','wordcraft','1','MA==')`,
		`INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,result_json) VALUES('raw-page','raw-ws','saved','sha256:synthetic','{"status":"saved"}'),('raw-page','raw-ws','legacy','sha256:synthetic','{"status":"saved"}'),('raw-page','raw-ws','denied','sha256:synthetic','{"status":"rejected"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte(" exact synthetic bytes \x00\xff ")
	canonical := []byte("canonical synthetic")
	const insert = `INSERT INTO mooring_collab_raw_changes(page_id,workspace_id,change_id,format,protocol_version,model_version,engine_version,raw_atomic,raw_digest,canonical_atomic,canonical_digest) VALUES('raw-page',?,?,'atomic-change-data.v1','0.4.0','0.2.0','0.10.0',?,?,?,?)`
	args := []any{"raw-ws", "saved", raw, rawDataDigest(raw), canonical, rawDataDigest(canonical)}
	if _, err := db.Exec(insert, args...); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE mooring_collab_raw_changes SET raw_atomic=raw_atomic`,
		`INSERT OR REPLACE INTO mooring_collab_raw_changes SELECT * FROM mooring_collab_raw_changes`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatal("immutable history replaced/updated", q)
		}
	}
	for name, changes := range map[string]map[int]any{
		"foreign workspace": {0: "raw-other", 1: "legacy"},
		"rejected":          {1: "denied"},
		"missing parent":    {1: "missing"},
		"oversized":         {1: "legacy", 2: bytes.Repeat([]byte{1}, 262145)},
		"empty":             {1: "legacy", 2: []byte{}},
		"text not blob":     {1: "legacy", 2: "{}"},
		"bad digest":        {1: "legacy", 3: "sha256:bad"},
	} {
		values := append([]any(nil), args...)
		for i, v := range changes {
			values[i] = v
		}
		if _, err := db.Exec(insert, values...); err == nil {
			t.Fatal("invalid data accepted", name)
		}
	}
	var got []byte
	if err := db.QueryRow(`SELECT raw_atomic FROM mooring_collab_raw_changes`).Scan(&got); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("exact data changed", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM mooring_collab_raw_changes WHERE change_id='legacy'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy backfilled", count, err)
	}
	if _, err := db.Exec(`DELETE FROM mooring_collab_changes WHERE change_id='saved'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM mooring_collab_raw_changes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("content deletion left child", count, err)
	}
}
