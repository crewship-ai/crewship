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

// The real binary against the real Router (R71 §0.14/1): only the page's
// owner grants an agent write. A workspace ADMIN is refused with exit 4 and
// the reason, still grants the agent read, and still revokes; the owner's
// grant goes through; `page grants` reports who may delegate agent Edit.
func TestAcceptance_PageAgentEditIsOwnerOnly(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cagenteditowner00000000"
	const ownerBearer = "crewship_cli_agenteditowner0000000000000"
	const adminBearer = "crewship_cli_agenteditadmin0000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + ws + `','Edit','agent-edit')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('ae-crew','` + ws + `','Operations','ops','free')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('ae-agent','ae-crew','` + ws + `','Scribe','scribe')`,
		`INSERT INTO users(id,email,full_name) VALUES('ae-owner','owner@agentedit.invalid','Owner'),('ae-admin','admin@agentedit.invalid','Admin')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ae-m1','` + ws + `','ae-owner','MANAGER'),('ae-m2','` + ws + `','ae-admin','ADMIN')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for id, tok := range map[string]string{"ae-owner": ownerBearer, "ae-admin": adminBearer} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	dir := t.TempDir()
	configs := map[string]string{}
	for who, tok := range map[string]string{"owner": ownerBearer, "admin": adminBearer} {
		cfg := filepath.Join(dir, who+".yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+tok+"\nformat: json\n"), 0600); err != nil {
			t.Fatal(err)
		}
		configs[who] = cfg
	}
	binary := buildCrewshipBinary(t)
	run := func(who string, args ...string) (string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(os.Environ(), "HOME="+dir, "CREWSHIP_CONFIG="+configs[who], "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
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
	must := func(who string, args ...string) string {
		t.Helper()
		out, code := run(who, args...)
		if code != 0 {
			t.Fatalf("%s: CLI %v: exit %d\n%s", who, args, code, out)
		}
		return out
	}
	pageFile := filepath.Join(dir, "page.yaml")
	if err := os.WriteFile(pageFile, []byte("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: notes\n  name: Notes\nspec:\n  panels:\n    - id: state\n      schema: status.v1\n      owner: crew/ops\n      producer: script/state.sh\n      sla: 30s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	must("owner", "page", "create", "--file", pageFile)

	type acl struct {
		CanDelegateAgentEdit bool `json:"can_delegate_agent_edit"`
		Grants               []struct {
			SubjectType string `json:"subject_type"`
			Level       string `json:"level"`
			Live        bool   `json:"live"`
		} `json:"grants"`
	}
	grants := func(who string) acl {
		t.Helper()
		var got acl
		if err := json.Unmarshal([]byte(must(who, "page", "grants", "notes")), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := grants("admin"); got.CanDelegateAgentEdit {
		t.Fatalf("admin listing says it may delegate agent Edit: %+v", got)
	}
	if got := grants("owner"); !got.CanDelegateAgentEdit {
		t.Fatalf("owner listing says it may not delegate agent Edit: %+v", got)
	}

	out, code := run("admin", "page", "grant", "notes", "--agent", "scribe", "--level", "write")
	if code != 4 || !strings.Contains(out, "only the artifact's owner can delegate Edit to an agent") {
		t.Fatalf("admin agent write: exit %d, want 4 with the owner-only reason\n%s", code, out)
	}
	if got := grants("owner"); len(got.Grants) != 0 {
		t.Fatalf("a refused grant changed the ACL: %+v", got)
	}
	must("admin", "page", "grant", "notes", "--agent", "scribe", "--level", "read")
	must("owner", "page", "grant", "notes", "--agent", "scribe", "--level", "write")
	got := grants("owner")
	levels := map[string]bool{}
	for _, g := range got.Grants {
		if g.SubjectType == "agent" && g.Live {
			levels[g.Level] = true
		}
	}
	if !levels["read"] || !levels["write"] || len(got.Grants) != 2 {
		t.Fatalf("grants after admin read + owner write: %+v", got)
	}
	must("admin", "page", "revoke", "notes", "--agent", "scribe")
	if got := grants("owner"); len(got.Grants) != 0 {
		t.Fatalf("admin revoke left grants: %+v", got)
	}
}
