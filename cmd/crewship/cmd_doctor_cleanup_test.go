//go:build !clionly

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDoctorCleanupNoCreateOrMigrate(t *testing.T) {
	for _, mode := range []string{"missing", "old_schema"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "diagnostics.db")
			t.Setenv("DATABASE_URL", "file:"+path)
			if mode == "old_schema" {
				db, err := sql.Open("sqlite", "file:"+path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE existing(id INTEGER)`); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			if _, err := readLocalCleanup(context.Background()); err == nil {
				t.Fatal("expected actionable unavailable diagnostics")
			}
			if mode == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("created diagnostic database")
				}
			} else {
				db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var n int
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='resource_cleanup_status'`).Scan(&n); err != nil || n != 0 {
					t.Fatal("diagnostic applied migration")
				}
			}
		})
	}
}
func TestDoctorCleanupLocalSnapshotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.db")
	t.Setenv("DATABASE_URL", "file:"+path)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../internal/database/migrations/20260930164209_container_cleanup_diagnostics.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_cleanup_status(instance_id,crew_id,state,observed_at,complete,error_code) VALUES ('instance','deleted-owner','error',?,1,'remove_failed')`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cmd := newDoctorCleanupCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Observation string                 `json:"observation"`
		Items       []localCleanupSnapshot `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	if body.Observation != "persisted" || len(body.Items) != 1 || body.Items[0].Error != "remove_failed" || !body.Items[0].Stale || body.Items[0].State != "unknown" {
		t.Fatalf("%+v", body)
	}
}
