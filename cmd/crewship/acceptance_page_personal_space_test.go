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

// Actual binary -> existing list route -> migrated SQLite, without a model or
// document engine. This qualifies the explicit Page classification projection,
// not Document creation, publishing or personal-space browser acceptance.
func TestAcceptance_PagePersonalSpaceClassification(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cpersonalspacefixture0001"
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Space','personal-space')`, ws); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"MEMBER", "ADMIN", "OWNER"} {
		user := "space-" + role
		if _, err := db.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, "member-"+role, ws, user, role); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES(?,?,'fixture',?,'STANDARD')`, "token-"+role, user, sha256HexToken("crewship_cli_personal_space_fixture_"+role)); err != nil {
			t.Fatal(err)
		}
	}
	for _, private := range []bool{true, false} {
		slug := "legacy-unfiled"
		if private {
			slug = "explicit-private"
		}
		if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',?)`, slug, ws, slug, slug, "space-MEMBER", private); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "personal-space-test-secret-32-chars", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	for _, role := range []string{"MEMBER", "ADMIN", "OWNER"} {
		t.Run(role, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\nworkspace: %s\ntoken: crewship_cli_personal_space_fixture_%s\nformat: json\n", server.URL, ws, role)), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), binary, "page", "list")
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "CREWSHIP_") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("page list: %v %s", err, out)
			}
			var rows []struct {
				Slug    string `json:"slug"`
				Private *bool  `json:"private_audience"`
			}
			if err := json.Unmarshal(out, &rows); err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, row := range rows {
				if row.Private == nil {
					t.Fatalf("%s has no explicit audience classification", row.Slug)
				}
				found[row.Slug] = true
				if *row.Private != (row.Slug == "explicit-private") {
					t.Errorf("%s classification=%v", row.Slug, *row.Private)
				}
			}
			if !found["legacy-unfiled"] || found["explicit-private"] != (role != "ADMIN") {
				t.Fatalf("unexpected authorized list: %#v", found)
			}
			personal := exec.CommandContext(t.Context(), binary, "page", "list", "--audience", "personal", "--limit", "1")
			personal.Env = command.Env
			personalOut, err := personal.CombinedOutput()
			if err != nil {
				t.Fatalf("personal catalog: %v %s", err, personalOut)
			}
			var personalRows []struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(personalOut, &personalRows); err != nil {
				t.Fatal(err)
			}
			want := 0
			if role == "MEMBER" {
				want = 1
			}
			if len(personalRows) != want || want == 1 && personalRows[0].Slug != "explicit-private" {
				t.Fatalf("personal catalog leaked custody or legacy ownership: %s", personalOut)
			}
		})
	}
}
