package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestMooringHumanBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA synchronous=FULL`, `INSERT INTO users(id,email) VALUES('mooring-human','mooring-human@example.test')`, `INSERT INTO workspaces(id,name,slug) VALUES('cmooringworkspace000000001','Mooring','mooring')`, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mooring-member','cmooringworkspace000000001','mooring-human','OWNER')`} {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	const token = "crewship_cli_mooring_synthetic_token"
	if _, e := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('mooring-token','mooring-human','human',?,'STANDARD')`, sha256HexToken(token)); e != nil {
		t.Fatal(e)
	}
	router, e := api.NewRouter(db, strings.Repeat("mooring-secret-", 3), slog.Default())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(router.Shutdown)
	router.RegisterGridCraft(appengine.Options{Enabled: true}, binaryWorkbookEngine{})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if e = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: cmooringworkspace000000001\nformat: json\n", server.URL, token)), 0600); e != nil {
		t.Fatal(e)
	}
	run := func(wantError bool, input string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, e := cmd.CombinedOutput()
		if (e != nil) != wantError {
			t.Fatalf("%v: %v %s", args, e, out)
		}
		return string(out)
	}
	if out := run(false, "", "mooring", "apps"); !strings.Contains(out, `"input_schema"`) {
		t.Fatal(out)
	}
	run(false, `{"app_id":"hello","slug":"binary-hello","name":"Hello"}`, "mooring", "create", "--file", "-")
	if out := run(false, "", "mooring", "read", "binary-hello"); !strings.Contains(out, "Hello Mooring") {
		t.Fatal(out)
	}
	packet := `{"mutation_id":"binary-one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"saved through binary"}}`
	for i := 0; i < 2; i++ {
		if out := run(false, packet, "mooring", "command", "binary-hello", "--file", "-"); !strings.Contains(out, `"rev": 2`) && !strings.Contains(out, `"rev":2`) {
			t.Fatal(out)
		}
	}
	if out := run(false, "", "mooring", "read", "binary-hello"); !strings.Contains(out, "saved through binary") {
		t.Fatal(out)
	}
	run(true, `{"mandate_id":"test","run_id":"not-a-live-run","base_rev":2,"generation":1,"command":"set"}`, "mooring", "admit", "binary-hello", "--file", "-")
	run(false, "", "mooring", "revoke", "binary-hello", "test")
}
func TestMooringAgentBinaryPacketIsUnchanged(t *testing.T) {
	packet := `{"action":"command","artifact_id":"artifact","mandate_id":"retained","request":{"mutation_id":"one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"agent"}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/mooring" || r.Header.Get("Authorization") != "Bearer synthetic-agent" {
			t.Errorf("bad authority or route")
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil || string(raw) != packet {
			t.Errorf("packet changed: %s %v", raw, e)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"outcome":"committed","rev":2}`)
	}))
	defer server.Close()
	binary := buildCrewshipBinary(t)
	cmd := exec.CommandContext(t.Context(), binary, "mooring", "agent", "--file", "-")
	cmd.Stdin = strings.NewReader(packet)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_AGENT_TOKEN=synthetic-agent", "CREWSHIP_SIDECAR_URL="+server.URL)
	out, e := cmd.CombinedOutput()
	if e != nil || !strings.Contains(string(out), "committed") {
		t.Fatalf("%s %v", out, e)
	}
}
