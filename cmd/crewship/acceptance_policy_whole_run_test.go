package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_PolicyWholeRunRollingUpgradeRefusesUnconfirmedSetting(t *testing.T) {
	for _, tc := range []struct {
		name, get, put string
		wantPuts       int
	}{
		{"old-backend", `{}`, `{}`, 0},
		{"malformed-preflight", `{"whole_run_approval":"false"}`, `{}`, 0},
		{"missing-confirmation", `{"whole_run_approval":false}`, `{}`, 1},
		{"mismatched-confirmation", `{"whole_run_approval":false}`, `{"whole_run_approval":false}`, 1},
		{"malformed-confirmation", `{"whole_run_approval":false}`, `{"whole_run_approval":"true"}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var puts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/crews":
					_, _ = w.Write([]byte(`[{"id":"cgatecrew000000000000001","slug":"synthetic"}]`))
				case r.URL.Path == "/api/v1/workspaces":
					_, _ = w.Write([]byte(`[{"id":"cpolicyws000000000000001","slug":"synthetic"}]`))
				case strings.HasSuffix(r.URL.Path, "/policy") && r.Method == "GET":
					_, _ = w.Write([]byte(tc.get))
				case strings.HasSuffix(r.URL.Path, "/policy") && r.Method == "PUT":
					puts.Add(1)
					_, _ = w.Write([]byte(tc.put))
				default:
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(srv.Close)
			cfg := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: cpolicyws000000000000001\ntoken: synthetic-policy-token\nformat: json\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := runFeedbackCLI(t, cfg, "policy", "set", "--crew", "synthetic", "--level", "guided", "--whole-run-approval")
			if err == nil || strings.Contains(out, "Policy updated") || int(puts.Load()) != tc.wantPuts {
				t.Fatalf("false success or mutation: err=%v puts=%d want=%d out=%s", err, puts.Load(), tc.wantPuts, out)
			}
		})
	}
}

func TestAcceptance_PolicyWholeRunOptInRealAPI(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const workspace = "cpolicyws000000000000001"
	const token = "crewship_cli_policyoptin0000000000000"
	for _, statement := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + workspace + `','Policy','policy')`,
		`INSERT INTO users(id,email) VALUES('policy-user','policy-optin@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('policy-member','` + workspace + `','policy-user','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('policy-crew','` + workspace + `','Policy crew','policy-crew')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at,scopes) VALUES('policy-token','policy-user','acceptance',?,datetime('now'),'["crews:write"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--whole-run-approval", "", "--whole-run-approval=false"} {
		args := []string{"policy", "set", "--crew", "policy-crew", "--level", "guided"}
		if flag != "" {
			args = append(args, flag)
		}
		if out, err := runFeedbackCLI(t, cfg, args...); err != nil {
			t.Fatalf("set %v %s", err, out)
		}
		out, err := runFeedbackCLI(t, cfg, "policy", "get", "--crew", "policy-crew")
		var got policyWire
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.WholeRunApproval != (flag != "--whole-run-approval=false") {
			t.Fatalf("get %v %s", err, out)
		}
	}
	if _, err := db.Exec(`UPDATE cli_tokens SET scopes='["agents:write"]' WHERE id='policy-token'`); err != nil {
		t.Fatal(err)
	}
	if out, err := runFeedbackCLI(t, cfg, "policy", "set", "--crew", "policy-crew", "--level", "guided", "--whole-run-approval"); err == nil {
		t.Fatalf("wrong scoped token changed policy: %s", out)
	}
	if _, err := db.Exec(`UPDATE cli_tokens SET scopes='["crews:write"]' WHERE id='policy-token'; UPDATE workspace_members SET role='MEMBER' WHERE id='policy-member'`); err != nil {
		t.Fatal(err)
	}
	if out, err := runFeedbackCLI(t, cfg, "policy", "set", "--crew", "policy-crew", "--level", "guided", "--whole-run-approval"); err == nil {
		t.Fatalf("member changed policy: %s", out)
	}
	var enabled bool
	if err := db.QueryRow(`SELECT whole_run_approval FROM crews WHERE id='policy-crew'`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("denied mutation persisted: %v %v", enabled, err)
	}
}
