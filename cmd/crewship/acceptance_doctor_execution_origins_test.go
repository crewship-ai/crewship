//go:build !clionly

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func TestAcceptanceDoctorExecutionOriginsLocalReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	markedDBAt(t, path, "audit-install")
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('audit-ws','SECRET_WORKSPACE','audit-ws')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('audit-crew','audit-ws','Crew','audit')`,
		`INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,enabled) VALUES('audit-template','audit-ws','audit-crew','SECRET_TITLE','0 9 * * *',1)`,
	} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	cfg := filepath.Join(dir, "cli.yaml")
	if err = os.WriteFile(cfg, []byte("format: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runLocalDBCLI(t, cfg, filepath.Join(dir, "unused"), []string{"DATABASE_URL=file:" + path}, "doctor", "execution-origins", "--json", "--limit", "1")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var result struct {
		ReadOnly bool                                  `json:"read_only"`
		Records  []struct{ ID, Classification string } `json:"records"`
	}
	if err = json.Unmarshal([]byte(out), &result); err != nil || !result.ReadOnly || len(result.Records) != 1 || result.Records[0].ID != "audit-template" || result.Records[0].Classification != "blocked" {
		t.Fatalf("%v %s", err, out)
	}
	if strings.Contains(out, "SECRET_") {
		t.Fatalf("source body leaked %s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("doctor changed database bytes: %v", err)
	}
	missing := filepath.Join(dir, "missing.db")
	out, err = runLocalDBCLI(t, cfg, filepath.Join(dir, "unused"), []string{"DATABASE_URL=file:" + missing}, "doctor", "execution-origins", "--json")
	if err == nil || !strings.Contains(out, "execution_origins_database_unavailable") {
		t.Fatalf("missing DB %v %s", err, out)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("doctor created missing DB: %v", err)
	}
}
