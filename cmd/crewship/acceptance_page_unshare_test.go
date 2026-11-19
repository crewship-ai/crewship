package main

import (
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
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Actual binary -> POST /api/v1/pages/{slug}/return-private -> migrated
// SQLite (PX-11). The owner previews the accesses that end, confirms with the
// review token, and the same Page ID is private again: the grant holder's next
// read through the CLI is refused and the public link is revoked.
func TestAcceptance_PageUnshareReturnsSharedDocumentToPrivate(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cunsharefixture000000001"
	exec1 := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec1(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Unshare','unshare')`, ws)
	for _, user := range []string{"owner", "reader"} {
		exec1(`INSERT INTO users(id,email) VALUES(?,?)`, "unshare-"+user, user+"@example.invalid")
		exec1(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, "member-"+user, ws, "unshare-"+user)
		exec1(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES(?,?,'fixture',?,'STANDARD')`, "token-"+user, "unshare-"+user, sha256HexToken("crewship_cli_unshare_fixture_"+user))
	}
	exec1(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('shared-doc-id',?,'shared-doc','Shared doc','unshare-owner','{}',0)`, ws)
	exec1(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,'shared-doc-id',3,x'',x'')`, ws)
	exec1(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('shared-doc-id','user','unshare-reader','read','unshare-owner','2026-11-01T00:00:00Z')`)
	exec1(`INSERT INTO page_public_tokens(id,page_id,token_hash,expires_at,created_by_user_id) VALUES('shared-doc-link','shared-doc-id','unshare-link-hash','2999-01-01T00:00:00Z','unshare-owner')`)

	router, err := api.NewRouter(db, "unshare-acceptance-secret-32-chars", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	run := func(user string, args ...string) ([]byte, error) {
		t.Helper()
		config := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\nworkspace: %s\ntoken: crewship_cli_unshare_fixture_%s\n", server.URL, ws, user)), 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), binary, args...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}

	if out, err := run("reader", "page", "get", "shared-doc", "--format", "json"); err != nil {
		t.Fatalf("grant holder read before unshare: %v %s", err, out)
	}
	if out, err := run("reader", "page", "unshare", "shared-doc", "--dry-run", "--pages-version", "0"); err == nil {
		t.Fatalf("a grant holder previewed the owner's return: %s", out)
	}

	human, err := run("owner", "page", "unshare", "shared-doc", "--dry-run", "--pages-version", "0")
	if err != nil {
		t.Fatalf("human preview: %v %s", err, human)
	}
	for _, want := range []string{"Dry run", "reader@example.invalid", "grant: user reader@example.invalid read", "public link: shared-doc-link", "cannot be recalled", "--review-token"} {
		if !strings.Contains(string(human), want) {
			t.Fatalf("human preview lacks %q:\n%s", want, human)
		}
	}
	raw, err := run("owner", "page", "unshare", "shared-doc", "--dry-run", "--pages-version", "0", "--format", "json")
	if err != nil {
		t.Fatalf("json preview: %v %s", err, raw)
	}
	var preview struct {
		DryRun        bool   `json:"dry_run"`
		PageID        string `json:"page_id"`
		Kind          string `json:"kind"`
		ReviewToken   string `json:"review_token"`
		RemovedGrants []struct {
			SubjectID string `json:"subject_id"`
		} `json:"removed_grants"`
		RevokedPublicLinks []struct {
			ID string `json:"id"`
		} `json:"revoked_public_links"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil || !preview.DryRun || preview.Kind != "document" || preview.ReviewToken == "" || len(preview.RemovedGrants) != 1 || len(preview.RevokedPublicLinks) != 1 {
		t.Fatalf("json preview %s %v", raw, err)
	}
	var private int
	if err := db.QueryRow(`SELECT private_audience FROM pages WHERE id='shared-doc-id'`).Scan(&private); err != nil || private != 0 {
		t.Fatalf("dry run changed the audience: %d %v", private, err)
	}

	if out, err := run("owner", "page", "unshare", "shared-doc", "--yes", "--pages-version", "0", "--review-token", strings.Repeat("0", 64)); err == nil {
		t.Fatalf("forged review accepted: %s", out)
	}
	confirmed, err := run("owner", "page", "unshare", "shared-doc", "--yes", "--pages-version", "0", "--review-token", preview.ReviewToken, "--format", "json")
	if err != nil {
		t.Fatalf("confirm: %v %s", err, confirmed)
	}
	var done struct {
		Changed bool   `json:"changed"`
		PageID  string `json:"page_id"`
	}
	if err := json.Unmarshal(confirmed, &done); err != nil || !done.Changed || done.PageID != "shared-doc-id" {
		t.Fatalf("confirmation %s %v", confirmed, err)
	}
	var grants, links int
	if err := db.QueryRow(`SELECT private_audience,(SELECT count(*) FROM page_grants WHERE page_id='shared-doc-id'),(SELECT count(*) FROM page_public_tokens WHERE page_id='shared-doc-id' AND revoked_at IS NULL) FROM pages WHERE id='shared-doc-id' AND owner_user_id='unshare-owner' AND folder_id IS NULL`).Scan(&private, &grants, &links); err != nil || private != 1 || grants != 0 || links != 0 {
		t.Fatalf("after unshare: private=%d grants=%d links=%d err=%v", private, grants, links, err)
	}
	if out, err := run("reader", "page", "get", "shared-doc", "--format", "json"); err == nil {
		t.Fatalf("former grant holder still reads: %s", out)
	}
	if out, err := run("owner", "page", "get", "shared-doc", "--format", "json"); err != nil {
		t.Fatalf("owner read after unshare: %v %s", err, out)
	}
}
