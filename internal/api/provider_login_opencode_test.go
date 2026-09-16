package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProviderLoginOpenCode(t *testing.T) {
	for _, tc := range []struct{ provider, slot string }{{"OPENCODE", "OPENCODE_API_KEY"}, {"OPENCODE_GO", "OPENCODE_GO_API_KEY"}} {
		t.Run(tc.provider, func(t *testing.T) {
			h, db := newCredHandler(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			code, out := plCreate(t, h, user, ws, map[string]any{"name": tc.provider, "type": "PROVIDER_LOGIN", "provider": tc.provider, "mode": "api_key", "value": "private-gateway-key"})
			if code != http.StatusCreated {
				t.Fatalf("create %d: %v", code, out)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "private-gateway-key") {
				t.Fatal("create response leaks key")
			}
			id := out["id"].(string)
			if got := plDecryptColumn(t, db, `SELECT encrypted_value FROM credentials WHERE id = ?`, id); got != "private-gateway-key" {
				t.Fatal("key not encrypted intact")
			}
			login, _, err := loadLoginView(t.Context(), db, nil, id)
			if err != nil || login == nil {
				t.Fatalf("login view: %v", err)
			}
			if login.Delivery.Target != tc.slot || login.Mode != "api_key" || login.Refresh.Supported {
				t.Fatalf("wrong auth contract: %+v", login)
			}
			if tc.provider == "OPENCODE_GO" && (login.PlanLabel == nil || *login.PlanLabel != "Go subscription") {
				t.Fatal("Go must not be labelled metered")
			}

			models := NewModelsHandler(db, nil, "")
			if _, authoritative := models.providerModelIDs(t.Context(), ws, tc.provider); authoritative {
				t.Fatal("partial gateway suggestions must not reject custom native model IDs")
			}
			if adapter, model := providerRuntimeDefaults(tc.provider); adapter != "OPENCODE" || model == "" {
				t.Fatal("gateway default must use OpenCode")
			}
			if !validLLMProviders[tc.provider] || len(curatedOrEmpty(tc.provider)) == 0 {
				t.Fatal("connected provider cannot be selected with models")
			}
		})
	}
}

func TestAgentUpdateOpenCodeGateway(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"OPENCODE", "opencode/claude-sonnet-5"},
		{"OPENCODE_GO", "opencode-go/kimi-k3"},
		// Custom native IDs remain accepted; the server's suggestions are not
		// an authoritative inventory of the upstream account.
		{"OPENCODE_GO", "opencode-go/custom-model"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			h, user, ws := covAUHandler(t)
			h.SetModelValidator(NewModelsHandler(h.db, nil, ""))
			seedAgentRow(t, h.db, "gateway-agent", ws, "", "Gateway Agent", "gateway-agent", "AGENT")
			body, _ := json.Marshal(map[string]string{"llm_provider": tc.provider, "llm_model": tc.model, "cli_adapter": "OPENCODE"})
			rr := covAUPatch(t, h, user, ws, "OWNER", "gateway-agent", string(body))
			if rr.Code != http.StatusOK {
				t.Fatalf("update %d: %s", rr.Code, rr.Body.String())
			}
			var provider, model, adapter string
			if err := h.db.QueryRowContext(t.Context(), `SELECT llm_provider,llm_model,cli_adapter FROM agents WHERE id = 'gateway-agent'`).Scan(&provider, &model, &adapter); err != nil {
				t.Fatal(err)
			}
			if provider != tc.provider || model != tc.model || adapter != "OPENCODE" {
				t.Fatalf("payer changed: %s %s %s", provider, model, adapter)
			}
		})
	}
}
