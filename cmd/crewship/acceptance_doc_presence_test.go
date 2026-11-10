//go:build unix

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
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Docs presence through the real CLI binary and the registered Router (#5166):
// names come from the server, a revoked reader is refused and disappears, and
// a person without a grant on another document sees nobody there.
func TestAcceptance_DocumentPresenceBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cdocpresenceworkspace01"
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cdocpresenceworkspace01','Doc presence','doc-presence')`,
		`INSERT INTO users(id,email,full_name) VALUES('presence-owner','presence-owner@example.invalid','Olive Owner'),('presence-reader','presence-reader@example.invalid','Rory Reader')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pm1','cdocpresenceworkspace01','presence-owner','MEMBER'),('pm2','cdocpresenceworkspace01','presence-reader','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('presence-a','cdocpresenceworkspace01','presence-a','A','presence-owner','{}'),('presence-b','cdocpresenceworkspace01','presence-b','B','presence-owner','{}')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('presence-a','user','presence-reader','read','presence-owner')`,
	} {
		execSQL(query)
	}
	tokens := map[string]string{}
	for _, user := range []string{"presence-owner", "presence-reader"} {
		token := "crewship_cli_documentpresence_" + user
		execSQL(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,?,?,'STANDARD','["pages:read"]')`, "token-"+user, user, "presence test", sha256HexToken(token))
		tokens[user] = token
	}
	dir, err := os.MkdirTemp("", "doc-presence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("presence-service-secret-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	// Presence never calls the engine: its socket need not exist.
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithDocumentAuthority(filepath.Join(dir, "a.sock"), secret), api.WithDocumentService(filepath.Join(dir, "e.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	run := func(user string, args ...string) (string, error) {
		t.Helper()
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, tokens[user], workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"doc", "presence"}, args...)...)
		command.Dir = dir
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	peers := func(user string, args ...string) []string {
		t.Helper()
		output, err := run(user, args...)
		if err != nil {
			t.Fatalf("presence %s %v: %v %s", user, args, err, output)
		}
		var wire struct {
			Peers []struct {
				Name   string `json:"name"`
				UserID string `json:"user_id"`
			} `json:"peers"`
		}
		if err := json.Unmarshal([]byte(output), &wire); err != nil {
			t.Fatalf("presence reply: %v %s", err, output)
		}
		names := []string{}
		for _, peer := range wire.Peers {
			names = append(names, peer.Name+"/"+peer.UserID)
		}
		return names
	}
	if got := peers("presence-reader", "presence-a", "--client", "reader-tab-0001"); len(got) != 0 {
		t.Fatalf("reader alone sees %v", got)
	}
	if got := peers("presence-owner", "presence-a", "--client", "owner-tab-0001"); len(got) != 1 || got[0] != "Rory Reader/presence-reader" {
		t.Fatalf("owner sees %v", got)
	}
	// Canvas presence through the binary: the owner's pointer and selection reach the reader.
	if output, err := run("presence-owner", "presence-a", "--client", "owner-tab-0001", "--pointer", "240.5,-60", "--select", "intro,note-1"); err != nil {
		t.Fatalf("canvas presence: %v %s", err, output)
	}
	output, err := run("presence-reader", "presence-a", "--client", "reader-tab-0001")
	if err != nil || !strings.Contains(output, `"canvas"`) || !strings.Contains(output, `240.5`) || !strings.Contains(output, `"note-1"`) {
		t.Fatalf("reader does not see the owner's canvas presence: %v %s", err, output)
	}
	for _, bad := range [][]string{{"--select", "a b"}, {"--select", "a,a"}, {"--pointer", "1"}, {"--pointer", "2000000,0"}, {"--leave", "--pointer", "1,2"}} {
		if output, err := run("presence-owner", append([]string{"presence-a", "--client", "owner-tab-0001"}, bad...)...); err == nil || !strings.Contains(output, "--") {
			t.Fatalf("invalid canvas flags %v accepted: %v %s", bad, err, output)
		}
	}
	// No grant on B: refused, nobody revealed.
	if output, err := run("presence-reader", "presence-b", "--client", "reader-tab-0002"); err == nil || !strings.Contains(output, "404") || strings.Contains(output, "Olive") {
		t.Fatalf("reader on B: err=%v %s", err, output)
	}
	execSQL(`DELETE FROM page_grants WHERE page_id='presence-a' AND subject_id='presence-reader'`)
	if output, err := run("presence-reader", "presence-a", "--client", "reader-tab-0001"); err == nil || !strings.Contains(output, "404") || strings.Contains(output, "Olive") {
		t.Fatalf("revoked reader: err=%v %s", err, output)
	}
	if got := peers("presence-owner", "presence-a", "--client", "owner-tab-0001"); len(got) != 0 {
		t.Fatalf("revoked reader still listed: %v", got)
	}
	if got := peers("presence-owner", "presence-a", "--client", "owner-tab-0001", "--leave"); len(got) != 0 {
		t.Fatalf("leave reply lists %v", got)
	}
}
