package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The real binary against the real Router: `page grant` and `page revoke` act
// in the workspace the global --workspace flag selects, not the configured
// one, and "everyone in the workspace" is its own flag that cannot shadow it.
func TestAcceptance_PageGrantHonoursGlobalWorkspace(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const home = "cgrantworkspacehome0000"
	const other = "cgrantworkspaceother000"
	const bearer = "crewship_cli_grantworkspace000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + home + `','Home','grant-home'),('` + other + `','Other','grant-other')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('grant-crew','` + other + `','Operations','ops','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('grant-owner','owner@grant.invalid','Owner'),('grant-mate','mate@grant.invalid','Mate')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('grant-m1','` + home + `','grant-owner','OWNER'),('grant-m2','` + other + `','grant-owner','OWNER'),('grant-m3','` + other + `','grant-mate','MEMBER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('grant-token','grant-owner','test',?,datetime('now'))`, sha256HexToken(bearer)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+home+"\ntoken: "+bearer+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "HOME="+dir, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.String(), exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return out.String(), 0
	}
	must := func(args ...string) string {
		t.Helper()
		out, code := run(args...)
		if code != 0 {
			t.Fatalf("CLI %v: exit %d\n%s", args, code, out)
		}
		return out
	}
	pageFile := filepath.Join(dir, "page.yaml")
	if err := os.WriteFile(pageFile, []byte("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: only-other\n  name: Only other\nspec:\n  panels:\n    - id: state\n      schema: status.v1\n      owner: crew/ops\n      producer: script/state.sh\n      sla: 30s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "create", "--file", pageFile, "--workspace", other)
	if out, code := run("page", "grant", "only-other", "--user", "mate@grant.invalid", "--level", "read"); code != 3 {
		t.Fatalf("the configured workspace reached the page: exit %d\n%s", code, out)
	}

	type acl struct {
		Grants []struct {
			SubjectType string `json:"subject_type"`
			Level       string `json:"level"`
		} `json:"grants"`
	}
	grants := func() acl {
		t.Helper()
		var got acl
		if err := json.Unmarshal([]byte(must("page", "grants", "only-other", "--workspace", other)), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	must("page", "grant", "only-other", "--workspace", other, "--user", "mate@grant.invalid", "--level", "read")
	must("page", "grant", "only-other", "-w", other, "--everyone", "--level", "write")
	if got := grants(); len(got.Grants) != 2 {
		t.Fatalf("grants after two grants in the named workspace: %+v", got)
	}
	must("page", "revoke", "only-other", "--workspace", other, "--user", "mate@grant.invalid")
	must("page", "revoke", "only-other", "--workspace", other, "--everyone")
	if got := grants(); len(got.Grants) != 0 {
		t.Fatalf("grants after revoking both: %+v", got)
	}

	// The old boolean spelling fails closed, with a pointer to the new one.
	for _, old := range [][]string{
		{"page", "grant", "only-other", "--workspace", "--level", "read"},
		{"page", "grant", "only-other", "--level", "read", "--workspace"},
		{"page", "revoke", "only-other", "--workspace"},
		{"page", "share", "only-other", "--workspace", "--level", "read"},
	} {
		out, code := run(old...)
		if code != 2 || !strings.Contains(out, "--everyone") {
			t.Fatalf("old form %v: exit %d\n%s", old, code, out)
		}
	}
	if out, code := run("page", "grant", "only-other", "--workspace", other, "--everyone", "--user", "mate@grant.invalid", "--level", "read"); code != 2 || !strings.Contains(out, "both given") {
		t.Fatalf("two subjects: exit %d\n%s", code, out)
	}
	if got := grants(); len(got.Grants) != 0 {
		t.Fatalf("a refused command changed the grants: %+v", got)
	}
}
