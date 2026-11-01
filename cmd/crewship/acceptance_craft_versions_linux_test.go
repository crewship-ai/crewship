//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The real CLI binary lists native versions through the production Router over
// TCP: owner and reader see the same newest-first history with authors, an
// outsider and a disabled workspace flag get the refusal, and the private
// admission token never reaches the output.
func TestAcceptance_CraftVersionsList(t *testing.T) {
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
	const workspace = "ccraftcliversionsaccept01"
	const ownerBearer = "crewship_cli_craft_history_owner_00000000"
	const readerBearer = "crewship_cli_craft_history_reader_0000000"
	const outsiderBearer = "crewship_cli_craft_history_outside_000000"
	executable := []byte("#!/bin/sh\nexit 9\n")
	engine := filepath.Join(t.TempDir(), "fixture-wordcraft")
	if err = os.WriteFile(engine, executable, 0700); err != nil {
		t.Fatal(err)
	}
	engineHash := fmt.Sprintf("%x", sha256.Sum256(executable))
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + workspace + `','Craft history acceptance','craft-history')`,
		`INSERT INTO users(id,email,full_name) VALUES('craft-hist-pavel','pavel@history.invalid','Pavel'),('craft-hist-rita','rita@history.invalid','Rita'),('craft-hist-olga','olga@history.invalid','Olga')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('craft-hist-member-a','` + workspace + `','craft-hist-pavel','OWNER'),('craft-hist-member-b','` + workspace + `','craft-hist-rita','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('craft-hist-page','` + workspace + `','native-history','Native history','craft-hist-pavel','{}',0)`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('craft-hist-page','user','craft-hist-rita','read','craft-hist-pavel')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('craft-hist-agent','` + workspace + `','Drafting Agent','drafting-agent')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('craft-hist-page',?,'wordcraft',?,?,?)`, workspace, strings.Repeat("a", 40), engineHash, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, token string }{{"craft-hist-pavel", ownerBearer}, {"craft-hist-rita", readerBearer}, {"craft-hist-olga", outsiderBearer}} {
		if _, err = db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES(?,?,?,?,'["pages:*"]',datetime('now'))`, u.id+"-token", u.id, "native history acceptance", sha256HexToken(u.token)); err != nil {
			t.Fatal(err)
		}
	}
	// Three saved versions: two by Pavel, one by an agent. Seeded through the
	// VersionStore itself, never by writing version rows by hand.
	store := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	for i, actor := range []string{"user:craft-hist-pavel", "agent:craft-hist-agent", "user:craft-hist-pavel"} {
		content := []byte(fmt.Sprintf(`{"v":%d}`, i+1))
		if _, err = store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: workspace, PageID: "craft-hist-page"}, artifacts.VersionRequest{BaseVersion: int64(i), EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: content, MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return actor, nil }); err != nil {
			t.Fatal(err)
		}
	}
	profile := api.CraftNativeDocumentProfile{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: engine, CLISHA256: engineHash, EditorBundle: strings.Repeat("c", 64), EditorURL: "/_editor/b/" + strings.Repeat("c", 64) + "/index.html"}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCraftNativeHTTP(api.CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]api.CraftNativeDocumentProfile{"wordcraft": profile}}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	server := httptest.NewServer(router)
	defer server.Close()
	binary := buildCrewshipBinary(t)
	run := func(bearer string, stdin string, args ...string) (string, string, int) {
		t.Helper()
		home := t.TempDir()
		config := filepath.Join(home, "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+bearer+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		if len(args) == 0 {
			args = []string{"craft", "versions", "@craft-hist-page", "-f", "json"}
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "HOME="+home, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), code
	}
	type listing struct {
		CurrentVersion string `json:"currentVersion"`
		Truncated      bool   `json:"truncated"`
		Versions       []struct {
			Version   string `json:"version"`
			CreatedAt string `json:"createdAt"`
			Current   bool   `json:"current"`
			Author    struct {
				Kind        string `json:"kind"`
				DisplayName string `json:"displayName"`
			} `json:"author"`
		} `json:"versions"`
	}
	// Default off: the workspace flag is undefined.
	if out, stderr, code := run(ownerBearer, ""); code == 0 || strings.Contains(out, "createdAt") {
		t.Fatalf("history listed with the flag off: exit %d %s %s", code, out, stderr)
	} else if code != 3 {
		t.Fatalf("flag-off exit code %d, want 3 (not found): %s", code, stderr)
	}
	if _, err = db.Exec(`INSERT INTO feature_flags(id,key,description,enabled,percentage) VALUES('ff-craft-history-accept',?,'',1,100)`, api.CraftVersionHistoryFlag); err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct{ name, bearer string }{{"owner", ownerBearer}, {"reader", readerBearer}} {
		// The listing answers exactly as the whole document read does.
		if _, stderr, code := run(who.bearer, `{"action":"read"}`+"\n", "craft", "connect", "@craft-hist-page"); code != 0 {
			t.Fatalf("%s document read calibration exit %d: %s", who.name, code, stderr)
		}
		out, stderr, code := run(who.bearer, "")
		if code != 0 {
			t.Fatalf("%s exit %d: %s", who.name, code, stderr)
		}
		var got listing
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%s output %q: %v", who.name, out, err)
		}
		want := []struct{ version, kind, name string }{{"3", "human", "Pavel"}, {"2", "agent", "Drafting Agent"}, {"1", "human", "Pavel"}}
		if got.CurrentVersion != "3" || got.Truncated || len(got.Versions) != len(want) {
			t.Fatalf("%s listing %+v", who.name, got)
		}
		for i, w := range want {
			v := got.Versions[i]
			if v.Version != w.version || v.Author.Kind != w.kind || v.Author.DisplayName != w.name || v.Current != (i == 0) || v.CreatedAt == "" {
				t.Fatalf("%s version %d: %+v", who.name, i, v)
			}
		}
		if strings.Contains(out, "session_token") || strings.Contains(out, "bytes") {
			t.Fatalf("%s output leaked admission or content: %s", who.name, out)
		}
	}
	_, _, readCode := run(outsiderBearer, `{"action":"read"}`+"\n", "craft", "connect", "@craft-hist-page")
	out, stderr, code := run(outsiderBearer, "")
	if code != readCode {
		t.Fatalf("outsider listing exit %d differs from document read exit %d", code, readCode)
	}
	if code == 0 || strings.Contains(out, "createdAt") {
		t.Fatalf("outsider listed history: exit %d %s %s", code, out, stderr)
	}
	if code != 4 {
		t.Fatalf("outsider exit code %d, want 4 (forbidden): %s", code, stderr)
	}
}
