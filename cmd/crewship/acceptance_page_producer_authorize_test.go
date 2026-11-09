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

// The real binary against the real Router (#5260): a panel whose agent
// producer the owner did not authorize says so on `page get`, a workspace
// ADMIN cannot authorize it (exit 4, the owner-only reason), and the owner's
// `page authorize-producer` does, once, after which the panel no longer
// reports it.
func TestAcceptance_PageOwnerAuthorizesAgentProducer(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cproducerauthz000000000"
	const ownerBearer = "crewship_cli_producerauthzowner000000000"
	const adminBearer = "crewship_cli_producerauthzadmin000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + ws + `','Edit','producer-authz')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('pa-crew','` + ws + `','Operations','ops','free')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('pa-agent','pa-crew','` + ws + `','Scribe','scribe')`,
		`INSERT INTO users(id,email,full_name) VALUES('pa-owner','owner@producerauthz.invalid','Owner'),('pa-admin','admin@producerauthz.invalid','Admin')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pa-m1','` + ws + `','pa-owner','MANAGER'),('pa-m2','` + ws + `','pa-admin','ADMIN')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for id, tok := range map[string]string{"pa-owner": ownerBearer, "pa-admin": adminBearer} {
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
	if err := os.WriteFile(pageFile, []byte("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: notes\n  name: Notes\nspec:\n  panels:\n    - id: state\n      schema: status.v1\n      owner: crew/ops\n      producer: agent/scribe\n      sla: 30s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	must("owner", "page", "create", "--file", pageFile)
	// As the upgrade leaves a designation the owner did not make.
	if _, err := db.Exec(`UPDATE page_panels SET producer_designated_by_user_id='pa-admin' WHERE panel_id='state'`); err != nil {
		t.Fatal(err)
	}
	type panel struct {
		ID                    string `json:"id"`
		Reason                string `json:"reason"`
		ProducerAuthorization *struct {
			Producer     string `json:"producer"`
			CanAuthorize bool   `json:"can_authorize"`
		} `json:"producer_authorization"`
	}
	state := func(who string) panel {
		t.Helper()
		var doc struct {
			Panels []panel `json:"panels"`
		}
		if err := json.Unmarshal([]byte(must(who, "page", "get", "notes")), &doc); err != nil {
			t.Fatal(err)
		}
		for _, p := range doc.Panels {
			if p.ID == "state" {
				return p
			}
		}
		t.Fatalf("panel state missing")
		return panel{}
	}
	if p := state("owner"); p.ProducerAuthorization == nil || !p.ProducerAuthorization.CanAuthorize ||
		p.ProducerAuthorization.Producer != "agent/scribe" || !strings.HasPrefix(p.Reason, "Producer not authorized by the owner") {
		t.Fatalf("owner reads the unauthorized panel as %+v", p)
	}
	if p := state("admin"); p.ProducerAuthorization == nil || p.ProducerAuthorization.CanAuthorize {
		t.Fatalf("admin reads the unauthorized panel as %+v", p)
	}

	out, code := run("admin", "page", "authorize-producer", "notes", "state")
	if code != 4 || !strings.Contains(out, "only the artifact's owner can authorize") {
		t.Fatalf("admin authorize-producer: exit %d, want 4 with the owner-only reason\n%s", code, out)
	}
	if p := state("owner"); p.ProducerAuthorization == nil {
		t.Fatalf("a refused authorization changed the panel: %+v", p)
	}

	var got struct {
		Producer string `json:"producer"`
		Changed  bool   `json:"changed"`
	}
	if err := json.Unmarshal([]byte(must("owner", "page", "authorize-producer", "notes", "state")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Producer != "agent/scribe" || !got.Changed {
		t.Fatalf("owner authorize-producer = %+v", got)
	}
	if p := state("owner"); p.ProducerAuthorization != nil || strings.Contains(p.Reason, "not authorized") {
		t.Fatalf("the panel still reads unauthorized after the owner authorized it: %+v", p)
	}
	var live bool
	if err := db.QueryRow(`SELECT producer_retired_at IS NULL AND producer_designated_by_user_id='pa-owner' FROM page_panels WHERE panel_id='state'`).Scan(&live); err != nil || !live {
		t.Fatalf("designation after authorize-producer: live=%v err=%v", live, err)
	}
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_producer.authorized'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("authorization audits = %d (%v), want 1", audits, err)
	}
}
