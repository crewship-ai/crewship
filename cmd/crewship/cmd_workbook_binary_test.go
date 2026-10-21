package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type binaryWorkbookEngine struct{}

func (binaryWorkbookEngine) Identity() string { return "binary-fixture@1" }
func (binaryWorkbookEngine) Prepare(_ context.Context, b mooring.Binding, _ mooring.State, c mooring.Command) (mooring.Candidate, error) {
	return mooring.NewCandidate(b, c.Snapshot, []string{"*"}), nil
}
func TestWorkbookHumanBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA synchronous=FULL`,
		`INSERT INTO users(id,email) VALUES('workbook-human','workbook-human@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cworkbookworkspace000000001','Workbook','workbook')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('workbook-member','cworkbookworkspace000000001','workbook-human','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('workbook-binary-page','cworkbookworkspace000000001','binary-workbook','Workbook','workbook-human',1,'{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	const token = "crewship_cli_workbook_synthetic_token"
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('workbook-token','workbook-human','human',?,'STANDARD')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, strings.Repeat("workbook-secret-", 3), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.RegisterGridCraft(mooring.Options{Enabled: true}, binaryWorkbookEngine{})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: cworkbookworkspace000000001\nformat: json\n", server.URL, token)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
	}
	body := `{"mutation_id":"binary-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	first := run(body, "workbook", "import", "@workbook-binary-page", "--file", "-")
	retry := run(body, "workbook", "import", "@workbook-binary-page", "--file", "-")
	if string(first) != string(retry) {
		t.Fatalf("receipt changed: %s %s", first, retry)
	}
	var receipt mooring.Receipt
	if json.Unmarshal(retry, &receipt) != nil || receipt.Rev != 1 {
		t.Fatalf("receipt %s", retry)
	}
	run("", "workbook", "read", "@workbook-binary-page")
	run("", "workbook", "status", "@workbook-binary-page", "binary-import")
	run("", "workbook", "revoke", "@workbook-binary-page", "absent")
}
