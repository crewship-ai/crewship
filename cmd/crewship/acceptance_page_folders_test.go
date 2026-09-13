package main

import (
	"encoding/json"
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

// A real CLI process against a real router on a migrated SQLite: the folder
// lifecycle as an operator drives it (#2527). Create, file, list, show, the
// refusal to delete a folder with a page in it, unfile, rename, delete.
func TestAcceptance_PageFoldersLifecycle(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cfolderacceptancews0"
	token := "crewship_cli_pagesfolder00000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cfolderacceptancews0','Folders','folders-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('folders-cli-crew','cfolderacceptancews0','Operations','ops','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('folders-cli-owner','folders@example.invalid','Folders Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('folders-cli-member','cfolderacceptancews0','folders-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('folders-cli-token','folders-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	pageYAML := "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: health\n  name: Health\nspec:\n  panels:\n    - id: mysql\n      schema: status.v1\n      owner: crew/ops\n      producer: script/check-mysql.sh\n      sla: 30s\n"
	if err := os.WriteFile(pageFile, []byte(pageYAML), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "create", "--file", pageFile)

	var folder struct {
		Slug, Owner, Icon, Color string
		PageCount                int `json:"page_count"`
	}
	if err := json.Unmarshal([]byte(must("page", "folder", "create", "ops-board", "--name", "Ops board", "--owner", "crew/ops", "--icon", "rocket", "--color", "amber")), &folder); err != nil {
		t.Fatal(err)
	}
	if folder.Slug != "ops-board" || folder.Owner != "crew/ops" || folder.Icon != "rocket" || folder.Color != "amber" {
		t.Fatalf("created folder: %+v", folder)
	}
	if out, err := run("page", "folder", "create", "bad-icon", "--owner", "crew/ops", "--icon", "memory"); err == nil || !strings.Contains(out, "crew icon") {
		t.Fatalf("a panel icon was accepted as a folder icon: %v %s", err, out)
	}

	must("page", "move", "health", "--folder", "ops-board")
	var page struct {
		PagesVersion int64 `json:"pages_version"`
		Folder       *struct{ Slug string }
	}
	if err := json.Unmarshal([]byte(must("page", "get", "health")), &page); err != nil {
		t.Fatal(err)
	}
	if page.Folder == nil || page.Folder.Slug != "ops-board" || page.PagesVersion != 1 {
		t.Fatalf("after move: %+v", page)
	}
	var list struct {
		Folders []struct {
			Slug      string
			PageCount int `json:"page_count"`
		}
	}
	if err := json.Unmarshal([]byte(must("page", "folder", "list")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Folders) != 1 || list.Folders[0].PageCount != 1 {
		t.Fatalf("list: %+v", list)
	}
	var show struct {
		Pages []struct{ Slug string }
	}
	if err := json.Unmarshal([]byte(must("page", "folder", "show", "ops-board")), &show); err != nil {
		t.Fatal(err)
	}
	if len(show.Pages) != 1 || show.Pages[0].Slug != "health" {
		t.Fatalf("show: %+v", show)
	}
	var rows []struct {
		Slug   string
		Folder *struct{ Slug string }
	}
	if err := json.Unmarshal([]byte(must("page", "list")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Folder == nil || rows[0].Folder.Slug != "ops-board" {
		t.Fatalf("page list rows: %+v", rows)
	}

	if out, err := run("page", "folder", "delete", "ops-board", "--yes"); err == nil || !strings.Contains(out, "not empty") {
		t.Fatalf("deleting a folder with a page in it: %v %s", err, out)
	}
	must("page", "folder", "update", "ops-board", "--name", "Operations", "--icon", "")
	must("page", "move", "health", "--unfiled")
	if err := json.Unmarshal([]byte(must("page", "get", "health")), &page); err != nil {
		t.Fatal(err)
	}
	if page.Folder != nil || page.PagesVersion != 2 {
		t.Fatalf("after unfile: %+v", page)
	}
	must("page", "folder", "add", "ops-board", "health")
	must("page", "folder", "remove", "ops-board", "health")

	// Sharing (#2533): share with everyone, read it back, see the label on
	// the folder, the caller's own paths, a batch move, and unshare.
	var acl struct {
		ACLVersion int64 `json:"acl_version"`
		ACL        []struct {
			SubjectType string `json:"subject_type"`
			CanWrite    bool   `json:"can_write"`
			SetBy       string `json:"set_by"`
		}
	}
	if err := json.Unmarshal([]byte(must("page", "folder", "share", "ops-board", "workspace", "--view")), &acl); err != nil {
		t.Fatal(err)
	}
	if acl.ACLVersion != 1 || len(acl.ACL) != 1 || acl.ACL[0].SubjectType != "workspace" || acl.ACL[0].CanWrite || acl.ACL[0].SetBy != "folders@example.invalid" {
		t.Fatalf("share: %+v", acl)
	}
	if out, err := run("page", "folder", "share", "ops-board", "agent:watcher", "--view"); err == nil || !strings.Contains(out, "agent") {
		t.Fatalf("sharing with an agent was accepted: %v %s", err, out)
	}
	var shown struct {
		Shared     string
		ACLVersion int64 `json:"acl_version"`
	}
	if err := json.Unmarshal([]byte(must("page", "folder", "show", "ops-board")), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Shared != "workspace" || shown.ACLVersion != 1 {
		t.Fatalf("show after share: %+v", shown)
	}
	secondPage := filepath.Join(t.TempDir(), "second.yaml")
	if err := os.WriteFile(secondPage, []byte(strings.ReplaceAll(pageYAML, "health", "health-2")), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "create", "--file", secondPage)
	var moved struct {
		Pages      []struct{ Slug string }
		ACLVersion int64 `json:"acl_version"`
	}
	if err := json.Unmarshal([]byte(must("page", "move", "health", "health-2", "--folder", "ops-board")), &moved); err != nil {
		t.Fatal(err)
	}
	if len(moved.Pages) != 2 || moved.ACLVersion != 1 {
		t.Fatalf("batch move: %+v", moved)
	}
	var access struct {
		Paths  []string
		Folder string
		Shared string
	}
	if err := json.Unmarshal([]byte(must("page", "access", "health", "--me")), &access); err != nil {
		t.Fatal(err)
	}
	if len(access.Paths) == 0 || access.Paths[0] != "owner" || access.Folder != "ops-board" || access.Shared != "workspace" {
		t.Fatalf("access --me: %+v", access)
	}
	must("page", "folder", "unshare", "ops-board", "workspace")
	if err := json.Unmarshal([]byte(must("page", "folder", "acl", "ops-board")), &acl); err != nil {
		t.Fatal(err)
	}
	if acl.ACLVersion != 2 || len(acl.ACL) != 0 {
		t.Fatalf("acl after unshare: %+v", acl)
	}
	must("page", "grant", "health", "--workspace", "--level", "read")
	if out, err := run("page", "grant", "health", "--workspace", "--level", "produce"); err == nil || !strings.Contains(out, "produce") {
		t.Fatalf("a produce grant to the workspace was accepted: %v %s", err, out)
	}
	must("page", "revoke", "health", "--workspace")
	must("page", "move", "health", "health-2", "--unfiled")
	must("page", "delete", "health-2", "--yes")
	must("page", "folder", "delete", "ops-board", "--yes")
	if err := json.Unmarshal([]byte(must("page", "folder", "list")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Folders) != 0 {
		t.Fatalf("list after delete: %+v", list)
	}
}
