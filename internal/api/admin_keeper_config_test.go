package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

func newKeeperConfigHandler(t *testing.T, dflt keepercfg.Defaults) (*AdminKeeperConfigHandler, *keepercfg.Store) {
	t.Helper()
	db := setupTestDB(t)
	// updated_by is a real foreign key into users, so the acting identities the
	// tests use have to exist — 'admin-1' is what manageReq signs requests as.
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (id, email, full_name)
		VALUES ('admin-1', 'admin@example.com', 'Admin'), ('u', 'u@example.com', 'U')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	store := keepercfg.New(db, dflt)
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load store: %v", err)
	}
	return NewAdminKeeperConfigHandler(store, nil, newTestLogger()), store
}

func decodeKeeperConfig(t *testing.T, rr *httptest.ResponseRecorder) keeperConfigResponse {
	t.Helper()
	var resp keeperConfigResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	return resp
}

var keeperEnvDefaults = keepercfg.Defaults{Enabled: true, EndpointURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"}

func TestAdminKeeperConfig_RequiresManageRole(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keeperEnvDefaults)

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		verb string
	}{
		{"get", h.Get, "GET"},
		{"put", h.Put, "PUT"},
		{"reset", h.Reset, "DELETE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.verb, "/api/v1/admin/keeper/config", nil)
			req = req.WithContext(context.WithValue(req.Context(), ctxRole, "VIEWER"))
			rr := httptest.NewRecorder()
			tc.fn(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("viewer got %d, want 403", rr.Code)
			}
		})
	}
}

// An unwired store must not look like "nothing configured" — that would render
// an empty, editable form whose saves silently vanish.
func TestAdminKeeperConfig_UnwiredStoreIs503(t *testing.T) {
	h := NewAdminKeeperConfigHandler(nil, nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/config", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}

func TestAdminKeeperConfig_GetReportsProvenance(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keeperEnvDefaults)

	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/config", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeKeeperConfig(t, rr)

	if !resp.Enabled.Value || resp.Enabled.Source != "env" {
		t.Errorf("enabled = %v/%s, want true/env", resp.Enabled.Value, resp.Enabled.Source)
	}
	if resp.EndpointURL.Value != keeperEnvDefaults.EndpointURL || resp.EndpointURL.Source != "env" {
		t.Errorf("endpoint = %q/%s", resp.EndpointURL.Value, resp.EndpointURL.Source)
	}
	if resp.Overridden {
		t.Error("overridden = true with no override stored")
	}
	if !resp.JudgeConfigured {
		t.Error("judge_configured = false with an endpoint and model in force")
	}
	// The fields the instance judge cannot honour yet must not advertise
	// themselves as editable.
	if resp.Provider.Editable || resp.Wire.Editable {
		t.Error("provider/wire reported as editable before the instance judge can build them")
	}
	if !resp.Enabled.Editable || !resp.EndpointURL.Editable || !resp.Model.Editable {
		t.Error("enabled/endpoint/model must be editable — that is the point of the endpoint")
	}
}

// The flow this endpoint exists for: an instance booted with no Keeper config
// gets a judge and turns Keeper on, in one request, and the change is in force
// immediately afterwards.
func TestAdminKeeperConfig_PutEnablesKeeper(t *testing.T) {
	h, store := newKeeperConfigHandler(t, keepercfg.Defaults{})

	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config",
		`{"enabled":true,"judge_endpoint_url":"http://192.168.1.40:11434","judge_model":"qwen2.5:7b"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeKeeperConfig(t, rr)
	if !resp.Enabled.Value || resp.Enabled.Source != "instance" {
		t.Errorf("enabled = %v/%s, want true/instance", resp.Enabled.Value, resp.Enabled.Source)
	}
	if resp.Model.Value != "qwen2.5:7b" {
		t.Errorf("model = %q", resp.Model.Value)
	}
	if !resp.Overridden {
		t.Error("overridden = false right after an override")
	}
	// And the store — the thing the judge actually reads — agrees.
	if eff := store.Effective(); !eff.Enabled.Value || eff.EndpointURL.Value != "http://192.168.1.40:11434" {
		t.Errorf("store did not take the change: %+v", eff)
	}
}

// null is the third state, and it has to survive the JSON round trip: it
// returns `enabled` to whatever KEEPER_ENABLED says rather than storing false.
func TestAdminKeeperConfig_PutEnabledNullInherits(t *testing.T) {
	h, store := newKeeperConfigHandler(t, keeperEnvDefaults)
	ctx := context.Background()
	off := keepercfg.TriOff
	if _, err := store.Apply(ctx, keepercfg.Patch{Enabled: &off}, "u"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", `{"enabled":null}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeKeeperConfig(t, rr)
	if !resp.Enabled.Value || resp.Enabled.Source != "env" {
		t.Errorf("enabled = %v/%s, want true/env", resp.Enabled.Value, resp.Enabled.Source)
	}
}

// An absent field is not an empty one. A console that PUTs only the model must
// not silently wipe the endpoint.
func TestAdminKeeperConfig_PutIsPartial(t *testing.T) {
	h, store := newKeeperConfigHandler(t, keepercfg.Defaults{})
	ctx := context.Background()
	on := keepercfg.TriOn
	if _, err := store.Apply(ctx, keepercfg.Patch{
		Enabled:     &on,
		EndpointURL: keeperStrPtr("http://10.0.0.5:11434"),
		Model:       keeperStrPtr("qwen2.5:7b"),
	}, "u"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", `{"judge_model":"qwen3:4b"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeKeeperConfig(t, rr)
	if resp.Model.Value != "qwen3:4b" {
		t.Errorf("model = %q", resp.Model.Value)
	}
	if resp.EndpointURL.Value != "http://10.0.0.5:11434" {
		t.Errorf("endpoint = %q — a partial update wiped a field it did not mention", resp.EndpointURL.Value)
	}
	if !resp.Enabled.Value {
		t.Error("enabled was cleared by a request that did not mention it")
	}
}

func TestAdminKeeperConfig_PutRejects(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantCode   int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"enabled as a string", `{"enabled":"yes"}`, http.StatusBadRequest},
		{"endpoint without a scheme", `{"judge_endpoint_url":"192.168.1.40:11434"}`, http.StatusBadRequest},
		{"endpoint carrying credentials", `{"judge_endpoint_url":"http://u:p@host:11434"}`, http.StatusBadRequest},
		{"unknown provider", `{"judge_provider":"bedrock"}`, http.StatusBadRequest},
		{"wire the judge cannot speak", `{"judge_wire":"openai-chat"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newKeeperConfigHandler(t, keeperEnvDefaults)
			rr := httptest.NewRecorder()
			h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", tc.body))
			if rr.Code != tc.wantCode {
				t.Errorf("got %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if store.Effective().Overridden {
				t.Error("a rejected request still wrote an override")
			}
		})
	}
}

// Fail-closed means the operator must not be able to configure the outage:
// Keeper on with no judge would DENY every credential request.
func TestAdminKeeperConfig_PutRefusesEnableWithoutJudge(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keepercfg.Defaults{})
	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/config", `{"enabled":true}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "endpoint") && !strings.Contains(body, "model") {
		t.Errorf("the message names neither the endpoint nor the model: %s", body)
	}
}

func TestAdminKeeperConfig_Reset(t *testing.T) {
	h, store := newKeeperConfigHandler(t, keeperEnvDefaults)
	ctx := context.Background()
	off := keepercfg.TriOff
	if _, err := store.Apply(ctx, keepercfg.Patch{Enabled: &off, Model: keeperStrPtr("bogus:1b")}, "u"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := httptest.NewRecorder()
	h.Reset(rr, manageReq("DELETE", "/api/v1/admin/keeper/config", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decodeKeeperConfig(t, rr)
	if resp.Overridden {
		t.Error("overridden = true after a reset")
	}
	if !resp.Enabled.Value || resp.Model.Value != keeperEnvDefaults.Model {
		t.Errorf("reset did not restore the env config: %+v", resp)
	}
}

// A URL is not a secret store, but KEEPER_OLLAMA_URL is not validated by us and
// an operator may have put a proxy token in it. It must not round-trip to a
// browser.
func TestAdminKeeperConfig_GetRedactsEndpointCredentials(t *testing.T) {
	h, _ := newKeeperConfigHandler(t, keepercfg.Defaults{
		Enabled: true, EndpointURL: "http://someone:s3cret@ollama.lan:11434", Model: "qwen2.5:7b",
	})
	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/config", ""))
	body := rr.Body.String()
	if strings.Contains(body, "s3cret") {
		t.Errorf("the response echoed the endpoint password: %s", body)
	}
	if !strings.Contains(body, "ollama.lan") {
		t.Errorf("redaction ate the host too: %s", body)
	}
}

func keeperStrPtr(s string) *string { return &s }
