package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type keeperRuntimeSecretGetter struct{ calls int }

func (g *keeperRuntimeSecretGetter) Get(string) (string, bool) {
	g.calls++
	return "synthetic-keeper-runtime-value", true
}

func TestKeeperExecuteIncludesCanonicalProxyEnvironment(t *testing.T) {
	t.Setenv("CREWSHIP_PARENT_PRIVATE_VALUE", "must-not-be-forwarded")
	for _, slot := range []string{"GITLAB_TOKEN", "OPENAI_API_KEY"} {
		t.Run(slot, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, credential := seedKeeperFixture(t, db)
			ctr := &mockContainerExec{output: "ok", execID: "runtime-env-exec", userResult: "2000:2000"}
			secret := &keeperRuntimeSecretGetter{}
			h := NewKeeperHandler(db, "internal-token", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "synthetic fixture", RiskScore: 1}}, slog.New(slog.NewTextHandler(io.Discard, nil))).WithContainer(ctr).WithSecrets(secret)
			response := doKeeperExecute(h, keeperExecuteBody{RequestingAgentID: agent, RequestingCrewID: crew, WorkspaceID: ws, CredentialID: credential, ContainerID: "ignored-body-target", Intent: "Verify synthetic standard CLI runtime", Command: "true", EnvVar: slot})
			if response.Code != http.StatusOK {
				t.Fatalf("execute status=%d", response.Code)
			}
			values := map[string]string{}
			for _, entry := range ctr.lastExecEnv {
				name, value, ok := strings.Cut(entry, "=")
				if !ok {
					t.Fatal("malformed runtime assignment")
				}
				if _, exists := values[name]; exists {
					t.Fatalf("duplicate runtime name %s", name)
				}
				values[name] = value
			}
			for _, entry := range orchestrator.SidecarProxyEnv() {
				name, value, _ := strings.Cut(entry, "=")
				if values[name] != value {
					t.Errorf("canonical runtime name %s missing or changed", name)
				}
			}
			if values[slot] != "synthetic-keeper-runtime-value" || len(values) != len(orchestrator.SidecarProxyEnv())+1 {
				t.Fatal("unexpected credential or inherited parent environment")
			}
			if ctr.lastExecUser != "2000:2000" {
				t.Fatal("container user resolution changed")
			}
		})
	}
}

func TestKeeperExecuteRefusesReservedRuntimeCredentialNames(t *testing.T) {
	for _, source := range []string{"body", "assignment", "derived-name"} {
		for _, name := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "HtTp_PrOxY", "NODE_USE_ENV_PROXY", "HOME", "PATH", "BASH_ENV", "LD_PRELOAD", "CREWSHIP_AGENT_TOKEN", "ANTHROPIC_BASE_URL", "OPENCODE_CONFIG_CONTENT"} {
			// Derived names are canonicalized to upper case by the existing resolver.
			if source == "derived-name" && name != strings.ToUpper(name) {
				continue
			}
			t.Run(source+"/"+name, func(t *testing.T) {
				db := setupTestDB(t)
				ws, crew, agent, credential := seedKeeperFixture(t, db)
				body := keeperExecuteBody{RequestingAgentID: agent, RequestingCrewID: crew, WorkspaceID: ws, CredentialID: credential, ContainerID: "ignored-body-target", Intent: "Verify synthetic runtime slot refusal", Command: "true"}
				if source == "body" {
					body.EnvVar = name
				}
				if source == "assignment" {
					if _, err := db.Exec(`UPDATE agent_credentials SET env_var_name=? WHERE agent_id=? AND credential_id=?`, name, agent, credential); err != nil {
						t.Fatal(err)
					}
				}
				if source == "derived-name" {
					if _, err := db.Exec(`UPDATE agent_credentials SET env_var_name='' WHERE agent_id=? AND credential_id=?`, agent, credential); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`UPDATE credentials SET name=? WHERE id=?`, name, credential); err != nil {
						t.Fatal(err)
					}
				}
				ctr, secret := &mockContainerExec{output: "ok", execID: "should-not-execute"}, &keeperRuntimeSecretGetter{}
				h := NewKeeperHandler(db, "internal-token", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "synthetic fixture", RiskScore: 1}}, slog.New(slog.NewTextHandler(io.Discard, nil))).WithContainer(ctr).WithSecrets(secret)
				response := doKeeperExecute(h, body)
				if response.Code != http.StatusBadRequest {
					t.Errorf("reserved name accepted: status=%d", response.Code)
				}
				if ctr.lastExecContainerID != "" || secret.calls != 0 {
					t.Error("reserved slot reached secret delivery or execution")
				}
				var requests int
				if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&requests); err != nil {
					t.Fatal(err)
				}
				if requests != 0 {
					t.Error("reserved slot reached pending/approval side effects")
				}
			})
		}
	}
}
