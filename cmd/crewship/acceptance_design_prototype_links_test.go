package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Prototype links (#4449) need no new endpoint: `crewship page design apply`
// carries linkTo in an ordinary upsert. Drive the real binary against a real
// router so the CLI contract agents use is covered end to end.
func TestAcceptance_DesignPrototypeLinkBinary(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	db := opened.DB
	const workspace = "cdesignlinkcliworkspacexyz"
	const token = "crewship_cli_design_link_binary_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('design-link-cli-owner','design-link-cli-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdesignlinkcliworkspacexyz','Design links','design-link-cli')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('design-link-cli-member','cdesignlinkcliworkspacexyz','design-link-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('design-link-cli-token','design-link-cli-owner','fixture',?,'STANDARD','["pages:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{6}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "design-link-cli-synthetic-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(input string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"page", "design"}, args...)...)
		command.Stdin = strings.NewReader(input)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	links := func(args ...string) map[string]any {
		t.Helper()
		out, err := run("", append([]string{"get", "proto"}, args...)...)
		if err != nil {
			t.Fatalf("get %v: %v %s", args, err, out)
		}
		var snapshot struct {
			Nodes []map[string]any `json:"nodes"`
		}
		if err := json.Unmarshal(out, &snapshot); err != nil || len(snapshot.Nodes) == 0 {
			t.Fatalf("decode get %v: %v %s", args, err, out)
		}
		found := map[string]any{}
		for _, node := range snapshot.Nodes {
			if link, ok := node["linkTo"]; ok {
				found[node["id"].(string)] = link
			}
		}
		return found
	}
	if out, err := run("", "create", "proto", "--name", "Prototype"); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	if out, err := run(`{"operations":[
		{"op":"upsert","node":{"id":"home","type":"frame","name":"Home","x":0,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"detail","type":"frame","name":"Detail","x":400,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"next","type":"rect","parentId":"home","x":24,"y":24,"width":120,"height":48,"linkTo":"detail"}}
	],"message":"link home to detail"}`, "apply", "proto", "--file", "-"); err != nil {
		t.Fatalf("apply link: %v %s", err, out)
	}
	if got := links(); len(got) != 1 || got["next"] != "detail" {
		t.Fatalf("linkTo not stored through CLI: %v", got)
	}
	if out, err := run(`{"operations":[{"op":"upsert","node":{"id":"next","linkTo":"`+strings.Repeat("x", 129)+`"}}]}`, "apply", "proto", "--file", "-"); err == nil {
		t.Fatalf("over-long linkTo accepted: %s", out)
	}
	if out, err := run(`{"operations":[{"op":"upsert","node":{"id":"next","linkTo":""}}],"message":"clear link"}`, "apply", "proto", "--file", "-"); err != nil {
		t.Fatalf("clear link: %v %s", err, out)
	}
	if got := links(); len(got) != 1 || got["next"] != "" {
		t.Fatalf("empty linkTo did not clear the link: %v", got)
	}
	// Version 2 (create is 1) retains the link after it was cleared live.
	if got := links("--version", "2"); got["next"] != "detail" {
		t.Fatalf("retained version lost linkTo: %v", got)
	}
}
