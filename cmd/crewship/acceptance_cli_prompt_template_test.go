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

func TestAcceptancePromptTemplateRolesAndWorkspace(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cparityworkspace000001','Parity','parity-cli'),('cparityworkspace000002','Other','parity-other')`,
		`INSERT INTO users(id,email,full_name) VALUES('parity-owner','parity-owner@example.invalid','Owner'),('parity-admin','parity-admin@example.invalid','Admin'),('parity-manager','parity-manager@example.invalid','Manager'),('parity-member','parity-member@example.invalid','Member'),('parity-viewer','parity-viewer@example.invalid','Viewer')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('parity-owner-member','cparityworkspace000001','parity-owner','OWNER'),('parity-admin-member','cparityworkspace000001','parity-admin','ADMIN'),('parity-manager-member','cparityworkspace000001','parity-manager','MANAGER'),('parity-member-member','cparityworkspace000001','parity-member','MEMBER'),('parity-viewer-member','cparityworkspace000001','parity-viewer','VIEWER'),('parity-other-member','cparityworkspace000002','parity-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{"parity-owner": "crewship_cli_parityowner00000000000000000", "parity-admin": "crewship_cli_parityadmin00000000000000000", "parity-manager": "crewship_cli_paritymanager000000000000000", "parity-member": "crewship_cli_paritymember0000000000000000", "parity-viewer": "crewship_cli_parityviewer0000000000000000"}
	for user, token := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "token-"+user, user, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	binary := buildCrewshipBinary(t)
	run := func(user, ws string, args ...string) (string, error) {
		t.Helper()
		cfg := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+server.URL+"\nworkspace: "+ws+"\ntoken: "+tokens[user]+"\nformat: json\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		b, e := cmd.CombinedOutput()
		return string(b), e
	}
	const ws = "cparityworkspace000001"
	if out, err := run("parity-owner", ws, "workspace", "member", "capabilities", "list"); err != nil || !strings.Contains(out, "parity-member") {
		t.Fatalf("owner capabilities: %v %s", err, out)
	}
	if out, err := run("parity-member", ws, "workspace", "member", "capabilities", "list"); err == nil {
		t.Fatalf("member read capability topology: %s", out)
	}
	if out, err := run("parity-admin", ws, "workspace", "member", "capabilities", "list"); err != nil {
		t.Fatalf("admin capabilities: %v %s", err, out)
	}
	for _, user := range []string{"parity-manager", "parity-viewer"} {
		if out, err := run(user, ws, "workspace", "member", "capabilities", "list"); err == nil {
			t.Fatalf("%s read capability topology: %s", user, out)
		}
	}
	input := filepath.Join(t.TempDir(), "template.json")
	if err := os.WriteFile(input, []byte(`{"name":"Parity prompt","template_json":{"prompt":"hello"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := run("parity-member", ws, "template", "prompt", "create", "--file", input); err == nil {
		t.Fatalf("member created template: %s", out)
	}
	out, err := run("parity-owner", ws, "template", "prompt", "create", "--file", input)
	if err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(out), &created) != nil || created.ID == "" {
		t.Fatalf("create result: %s", out)
	}
	if out, err = run("parity-owner", ws, "template", "prompt", "get", created.ID); err != nil || !strings.Contains(out, "Parity prompt") {
		t.Fatalf("get: %v %s", err, out)
	}
	if out, err = run("parity-owner", "cparityworkspace000002", "template", "prompt", "get", created.ID); err == nil {
		t.Fatalf("cross-workspace template read: %s", out)
	}
	if out, err = run("parity-owner", ws, "template", "prompt", "delete", created.ID, "--yes"); err != nil {
		t.Fatalf("delete: %v %s", err, out)
	}
}
