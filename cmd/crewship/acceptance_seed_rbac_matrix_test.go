package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Only execution/telemetry are synthetic. Provisioning, password setup, login,
// sessions, middleware, workspace resolution and every API handler are real.
// Transform-only routines never contact Docker or a model provider.
type matrixRunner struct{}

func (matrixRunner) RunStep(context.Context, pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	return pipeline.AgentStepResult{}, fmt.Errorf("unexpected model execution in RBAC fixture")
}

func TestAcceptance_DemoRBACMatrix(t *testing.T) {
	t.Setenv("CREWSHIP_RATELIMIT_DISABLED", "true")
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	db := testutil.MigratedDB(t).DB
	execSQL := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ownerPassword := "matrix-owner-pass"
	ownerHash, err := bcrypt.GenerateFromPassword([]byte(ownerPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(`INSERT INTO users(id,email,full_name,hashed_password) VALUES('matrix-owner','owner@matrix.invalid','Owner',?)`, string(ownerHash))
	execSQL(`INSERT INTO workspaces(id,name,slug,credential_reveal_enabled) VALUES('matrix-ws','RBAC target','matrix-ws',1),('matrix-other','Other tenant','matrix-other',1)`)
	execSQL(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('matrix-owner-member','matrix-ws','matrix-owner','OWNER')`)
	const token = "crewship_cli_matrix00000000000000000000000000"
	execSQL(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('matrix-token','matrix-owner','matrix',?,datetime('now'))`, sha256HexToken(token))
	execSQL(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('matrix-crew','matrix-ws','Lookout','lookout')`)
	execSQL(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('matrix-agent','matrix-ws','matrix-crew','Probe','probe','LEAD')`)
	const def = `{"dsl_version":"1.0","name":"matrix-probe","agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`
	execSQL(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,status,last_test_run_at) VALUES('matrix-pipeline','matrix-ws','matrix-probe','Probe',?,'hash','active',datetime('now'))`, def)
	enc, err := encryption.Encrypt("matrix-secret")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by,sensitivity) VALUES('matrix-credential','matrix-ws','MATRIX_SECRET',?,'matrix-owner','STANDARD')`, enc)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{})
	defer audit.Close()
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithAllowSignup(false), api.WithJournal(audit), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	router.PipelinesHandler.SetRunner(matrixRunner{})
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	srv := httptest.NewServer(router)
	defer srv.Close()
	owner := cli.NewClient(srv.URL, token, "matrix-ws")
	roster := append(append([]demoUser{}, demoUsers...), demoCapabilityUsers()...)
	if err := seedUserRoster(t.Context(), owner, roster); err != nil {
		t.Fatal(err)
	}
	// Provisioning adds memberships to the explicit target, never personal workspaces.
	var workspaces int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&workspaces); err != nil || workspaces != 2 {
		t.Fatalf("unexpected personal workspaces: %d %v", workspaces, err)
	}
	request := func(t *testing.T, client *cli.Client, method, path string, body any, want int) []byte {
		t.Helper()
		var response *http.Response
		var err error
		switch method {
		case "POST":
			response, err = client.Post(path, body)
		case "PATCH":
			response, err = client.Patch(path, body)
		default:
			response, err = client.Get(path)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, response.StatusCode, want, raw)
		}
		return raw
	}
	accounts := append(slices.Clone(roster), demoUser{Email: "owner@matrix.invalid", Password: ownerPassword, Role: "OWNER"})
	for i, u := range accounts {
		t.Run(strings.TrimSuffix(u.Email, "@crewship.local"), func(t *testing.T) {
			session, err := exchangeCredentialsForSession(cli.NewClient(srv.URL, "", ""), srv.URL, u.Email, u.Password)
			if err != nil {
				t.Fatal(err)
			}
			self := cli.NewClient(srv.URL, session, "matrix-ws")
			defer closeTransientSeedSession(t.Context(), self, session)
			raw := request(t, self, "GET", "/api/v1/workspaces", nil, 200)
			var visible []struct {
				ID           string   `json:"id"`
				Role         string   `json:"currentUserRole"`
				Capabilities []string `json:"currentUserCapabilities"`
			}
			if err := json.Unmarshal(raw, &visible); err != nil {
				t.Fatal(err)
			}
			if len(visible) != 1 || visible[0].ID != "matrix-ws" || visible[0].Role != u.Role {
				t.Fatalf("workspace visibility/role: %s", raw)
			}
			if u.Capabilities != nil {
				expected := slices.Clone(u.Capabilities)
				actual := slices.Clone(visible[0].Capabilities)
				slices.Sort(expected)
				slices.Sort(actual)
				if !slices.Equal(expected, actual) {
					t.Fatalf("signed-in capability set: %v, want %v", actual, expected)
				}
			}
			// Independent expected policy: role OR grant for creation, ADMIN+ OR
			// grant for rotation; reveal additionally has a MANAGER role floor.
			manager := u.Role == "MANAGER" || u.Role == "ADMIN" || u.Role == "OWNER"
			admin := u.Role == "ADMIN" || u.Role == "OWNER"
			has := func(cap string) bool { return slices.Contains(u.Capabilities, cap) }
			want := func(allowed bool, success int) int {
				if allowed {
					return success
				}
				return 403
			}
			request(t, self, "POST", "/api/v1/agents/matrix-agent/chats", map[string]string{"session_id": fmt.Sprintf("matrix-chat-%d", i)}, 201)
			request(t, self, "POST", "/api/v1/workspaces/matrix-ws/pipelines/matrix-probe/run", map[string]any{"inputs": map[string]any{}}, want(manager || has("routine.run"), 200))
			request(t, self, "POST", "/api/v1/workspaces/matrix-ws/pipeline-schedules", map[string]any{"cron_expr": "0 0 1 1 *", "target_pipeline_slug": "matrix-probe", "enabled": false}, want(manager || has("routine.create"), 201))
			request(t, self, "POST", "/api/v1/crews/matrix-crew/issues", map[string]any{"title": fmt.Sprintf("RBAC issue %d", i)}, want(manager || has("issue.create"), 201))
			request(t, self, "POST", "/api/v1/pages", map[string]any{"slug": fmt.Sprintf("matrix-page-%d", i), "name": "RBAC Page", "panels": []any{map[string]any{"id": "status", "schema": "status.v1", "title": "Status", "owner": "crew/lookout", "producer": "script/probe.sh", "sla_seconds": 30, "span": 8}}}, want(manager || has("page.create"), 201))
			request(t, self, "POST", "/api/v1/credentials", map[string]any{"name": fmt.Sprintf("MATRIX_%d", i), "value": "fixture-value", "type": "SECRET", "scope": "WORKSPACE"}, want(manager || has("credential.create"), 201))
			request(t, self, "POST", "/api/v1/credentials/matrix-credential/rotate", map[string]string{"value": "matrix-secret"}, want(admin || has("credential.rotate"), 200))
			// Import provides a complete, provider-free skill authoring success path.
			content := fmt.Sprintf("---\nname: matrix-skill-%d\ndescription: Use when testing RBAC permissions.\nlicense: MIT\n---\n# RBAC probe\nReturn the supplied test input unchanged. Never call external services.\n", i)
			request(t, self, "POST", "/api/v1/workspaces/matrix-ws/skills/import", map[string]string{"content": content}, want(manager || has("skill.create"), 201))
			// Generation uses a model; assert its authorization and input validation
			// separately, without claiming a model actually generated anything.
			request(t, self, "POST", "/api/v1/workspaces/matrix-ws/skills/generate", map[string]string{}, want(manager || has("skill.create"), 400))
			// Null baseline caps inherit ADMIN/MANAGER defaults; only ADMIN gets
			// reveal by default. Explicit variant caps replace those defaults.
			reveal := manager && (has("credentials:reveal") || u.Capabilities == nil && admin)
			revealed := request(t, self, "POST", "/api/v1/credentials/matrix-credential/reveal", map[string]string{"reason": "Verifying the demo RBAC authorization matrix for acceptance testing"}, want(reveal, 200))
			if reveal && !strings.Contains(string(revealed), "matrix-secret") {
				t.Fatalf("reveal did not return fixture secret: %s", revealed)
			}
			if !reveal && strings.Contains(string(revealed), "matrix-secret") {
				t.Fatal("denied reveal leaked secret")
			}
			// memory.write is reserved: there is no public write endpoint yet.
			request(t, self, "POST", "/api/v1/memory/write", map[string]string{"content": "fixture"}, 404)
			foreign := cli.NewClient(srv.URL, session, "")
			request(t, foreign, "GET", "/api/v1/workspaces/matrix-other/pipelines?workspace_id=matrix-other", nil, 403)
			// No role/capability can turn an unrelated tenant into the target.
			request(t, foreign, "POST", "/api/v1/workspaces/matrix-other/pipelines/matrix-probe/run?workspace_id=matrix-other", map[string]any{}, 403)
		})
	}
	// The shipped additive CLI works without provider credentials or --nuke.
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: matrix-ws\ntoken: "+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "seed", "rbac-matrix", "--server", srv.URL, "--workspace", "matrix-ws")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "SEED_ANTHROPIC_API_KEY=", "SEED_CODEX_AUTH_FILE=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("additive CLI: %v: %s", err, output)
	}
	// A second seed proves existing logins work and preserves all identities.
	if err := seedUserRoster(t.Context(), owner, roster); err != nil {
		t.Fatal(err)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil || users != len(roster)+1 {
		t.Fatalf("non-idempotent user seed: %d %v", users, err)
	}
	request(t, cli.NewClient(srv.URL, "", ""), "GET", "/api/v1/workspaces", nil, 401)
	t.Cleanup(func() {
		var active int
		if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE revoked_at IS NULL`).Scan(&active); err != nil || active != 0 {
			t.Errorf("fixture left active sessions: %d %v", active, err)
		}
	})
	// Exercise live grant/revoke with the SAME authenticated session (cache).
	u := demoUsers[3]
	session, err := exchangeCredentialsForSession(cli.NewClient(srv.URL, "", ""), srv.URL, u.Email, u.Password)
	if err != nil {
		t.Fatal(err)
	}
	self := cli.NewClient(srv.URL, session, "matrix-ws")
	defer closeTransientSeedSession(t.Context(), self, session)
	var userID string
	if err := db.QueryRow(`SELECT id FROM users WHERE email=?`, u.Email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	capPath := "/api/v1/workspaces/matrix-ws/members/" + userID + "/capabilities"
	runPath := "/api/v1/workspaces/matrix-ws/pipelines/matrix-probe/run"
	request(t, self, "POST", runPath, map[string]any{}, 403)
	request(t, owner, "PATCH", capPath, map[string]any{"grant": []string{"routine.run"}}, 200)
	request(t, self, "POST", runPath, map[string]any{}, 200)
	request(t, owner, "PATCH", capPath, map[string]any{"revoke": []string{"routine.run"}}, 200)
	request(t, self, "POST", runPath, map[string]any{}, 403)
	request(t, self, "PATCH", capPath, map[string]any{"grant": []string{"routine.run"}}, 403)
	variant := demoCapabilityUsers()[0]
	variantID, _, err := findWorkspaceMemberByEmail(owner, "matrix-ws", variant.Email)
	if err != nil {
		t.Fatal(err)
	}
	variantPath := "/api/v1/workspaces/matrix-ws/members/" + variantID + "/capabilities"
	request(t, owner, "PATCH", variantPath, map[string]any{"set": []string{"chat"}}, 200)
	if err := seedUserRoster(t.Context(), owner, []demoUser{variant}); err == nil {
		t.Fatal("manual capability drift silently accepted")
	}
	raw := request(t, owner, "GET", variantPath, nil, 200)
	var state struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || !slices.Equal(state.Capabilities, []string{"chat"}) {
		t.Fatalf("seed overwrote manual grants: %s %v", raw, err)
	}

}
