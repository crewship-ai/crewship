package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/crewship-ai/crewship/internal/bedrock"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

func bedrockAccountFixture(t *testing.T) string {
	t.Helper()
	b, _ := json.Marshal(bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "bedrock-synthetic-secret", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true})
	return string(b)
}

func TestBedrockAccountCreateAssignAndIsolation(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	otherWS := generateCUID()
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES(?,'Other','bedrock-other')`, otherWS)
	seedCrewRow(t, db, "bedrock-crew", ws, "Bedrock", "bedrock-crew")
	for _, id := range []string{"bedrock-a", "bedrock-b"} {
		execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES(?,?,'bedrock-crew',?,?)`, id, ws, id, id)
	}
	execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('bedrock-foreign',?,'Foreign','bedrock-foreign')`, otherWS)
	create := func(name string) string {
		code, out := plCreate(t, h, user, ws, map[string]any{"name": name, "provider": bedrock.Provider, "type": "PROVIDER_LOGIN", "mode": "api_key", "value": bedrockAccountFixture(t)})
		if code != 201 {
			t.Fatalf("create: %d %v", code, out)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "bedrock-synthetic-secret") {
			t.Fatal("create leaked secret")
		}
		return out["id"].(string)
	}
	first, second := create("EU one"), create("EU two")
	for _, id := range []string{first, second} {
		execOrFatal(t, db, `INSERT INTO credential_crews(credential_id,crew_id) VALUES(?,'bedrock-crew')`, id)
	}
	if d, _, e := loadDeliveredCredentials(t.Context(), db, "bedrock-a"); e != nil || len(d) != 0 {
		t.Fatalf("crew-only Bedrock accounts must not auto-pay: %v %+v", e, d)
	}
	assign := func(id, role string, agents []string) int {
		raw, _ := json.Marshal(map[string]any{"agent_ids": agents, "model": "eu.anthropic.claude-opus-5-5"})
		r := plRequest(t, "POST", "/api/v1/credentials/"+id+"/bedrock-assign", string(raw), user, ws, role)
		r.SetPathValue("credentialId", id)
		w := httptest.NewRecorder()
		h.AssignBedrock(w, r)
		if w.Code == http.StatusOK {
			assertBedrockAssignmentSchema(t, w.Body.Bytes())
		}
		return w.Code
	}
	if code := assign(first, "MEMBER", []string{"bedrock-a"}); code != 403 {
		t.Fatalf("member assigned account: %d", code)
	}
	if code := assign(first, "OWNER", []string{"bedrock-a", "bedrock-foreign"}); code != 404 {
		t.Fatalf("cross-workspace assignment: %d", code)
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM credential_bindings WHERE credential_id=?`, first).Scan(&count)
	if count != 0 {
		t.Fatal("failed batch partially committed")
	}
	if code := assign(first, "OWNER", []string{"bedrock-a"}); code != 200 {
		t.Fatalf("assign: %d", code)
	}
	var runner, provider, model string
	_ = db.QueryRow(`SELECT cli_adapter,llm_provider,llm_model FROM agents WHERE id='bedrock-a'`).Scan(&runner, &provider, &model)
	if runner != "OPENCODE" || provider != bedrock.Provider || model != "amazon-bedrock/eu.anthropic.claude-opus-5-5" {
		t.Fatal("assignment did not provision the native runner/model")
	}
	if code := assign(second, "OWNER", []string{"bedrock-a"}); code != 409 {
		t.Fatalf("payer silently replaced: %d", code)
	}
	d, _, e := loadDeliveredCredentials(t.Context(), db, "bedrock-a")
	if e != nil || len(d) != 1 || d[0].ID != first {
		t.Fatalf("wrong delivery: %v %+v", e, d)
	}
	if d, _, e := loadDeliveredCredentials(t.Context(), db, "bedrock-b"); e != nil || len(d) != 0 {
		t.Fatalf("peer inherited payer: %v %+v", e, d)
	}
	thirdCode, thirdAccount := plCreate(t, h, user, ws, map[string]any{"name": "Other payer", "provider": "ANTHROPIC", "type": "PROVIDER_LOGIN", "mode": "api_key", "value": "other-model-key"})
	if thirdCode != 201 {
		t.Fatal("could not create other model account")
	}
	third := thirdAccount["id"].(string)
	execOrFatal(t, db, `INSERT INTO credential_crews(credential_id,crew_id) VALUES(?,'bedrock-crew')`, third)
	d, _, e = loadDeliveredCredentials(t.Context(), db, "bedrock-a")
	if e != nil || len(d) != 1 || d[0].ID != first {
		t.Fatal("Bedrock agent received another provider's account")
	}
	grants, e := loadEffectiveCredentialGrants(t.Context(), db, "bedrock-a")
	if e != nil || len(grants[second]) != 0 || len(grants[first]) != 1 {
		t.Fatalf("grant snapshots disagree with explicit payer: %v %+v", e, grants)
	}
	if _, exists := grants[third]["bedrock-a"]; exists {
		t.Fatal("another provider can pay for Bedrock agent")
	}
	view, _, e := loadLoginView(t.Context(), db, nil, first)
	raw, _ := json.Marshal(view)
	if e != nil || view == nil || view.Bedrock == nil || view.Bedrock.Region != "eu-central-1" || strings.Contains(string(raw), "bedrock-synthetic-secret") || view.Refresh.Supported {
		t.Fatal("unsafe or misleading account view")
	}
	var renewed bedrock.Account
	_ = json.Unmarshal([]byte(bedrockAccountFixture(t)), &renewed)
	renewed.APIKey = "renewed-bedrock-key"
	renewed.ExpiresAt = time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	replacement, _ := json.Marshal(renewed)
	patchBody, _ := json.Marshal(map[string]any{"value": string(replacement), "mode": "api_key"})
	patch := plRequest(t, "PATCH", "/api/v1/credentials/"+first, string(patchBody), user, ws, "OWNER")
	patch.SetPathValue("credentialId", first)
	rr := httptest.NewRecorder()
	h.Update(rr, patch)
	if rr.Code != 200 {
		t.Fatalf("renewal failed: %d %s", rr.Code, rr.Body.String())
	}
	if got := plDecryptColumn(t, db, `SELECT encrypted_value FROM credentials WHERE id=?`, first); got != string(replacement) {
		t.Fatal("renewal did not replace the complete account atomically")
	}
	var expiry string
	_ = db.QueryRow(`SELECT token_expires_at FROM credentials WHERE id=?`, first).Scan(&expiry)
	if expiry != renewed.ExpiresAt {
		t.Fatal("renewal expiry was not replaced")
	}
	d, _, e = loadDeliveredCredentials(t.Context(), db, "bedrock-a")
	if e != nil || len(d) != 1 || d[0].ID != first {
		t.Fatal("renewal lost account bindings")
	}
	execOrFatal(t, db, `UPDATE credentials SET status='REVOKED' WHERE id=?`, first)
	if d, _, e := loadDeliveredCredentials(t.Context(), db, "bedrock-a"); e != nil || len(d) != 0 {
		t.Fatalf("revocation fell back to another payer: %v %+v", e, d)
	}
}

type bedrockProbeTransport func(*http.Request) (*http.Response, error)

func (f bedrockProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBedrockProbeChecksModelsAndSanitizesErrors(t *testing.T) {
	old := bedrockProbeHTTPClient
	defer func() { bedrockProbeHTTPClient = old }()
	client := *old
	bedrockProbeHTTPClient = &client
	for _, status := range []int{200, 401, 403, 404, 429, 500, 307} {
		calls := 0
		client.Transport = bedrockProbeTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Host != "bedrock-runtime.eu-central-1.amazonaws.com" || r.Header.Get("Authorization") != "Bearer bedrock-synthetic-secret" || r.URL.Path != "/model/eu.anthropic.claude-opus-5-5/converse" {
				t.Fatal("unsafe Bedrock probe target/auth")
			}
			body := `{"output":{"message":{"role":"assistant"}}}`
			if status != 200 {
				body = `{"message":"bedrock-synthetic-secret"}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		result := probeProviderLogin(t.Context(), bedrock.Provider, "api_key", bedrockAccountFixture(t), false)
		if !result.Supported || result.Valid != (status == 200) || calls != 1 || strings.Contains(result.Error, "bedrock-synthetic-secret") {
			t.Fatalf("probe %d: %+v calls=%d", status, result, calls)
		}
	}
}

func assertBedrockAssignmentSchema(t *testing.T, wire []byte) {
	t.Helper()
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("spec.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("spec.json#/components/schemas/BedrockAssignmentResponse")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(body); err != nil {
		t.Fatalf("actual Bedrock assignment response violates OpenAPI: %v", err)
	}
	for _, field := range []string{"assigned", "provider", "model"} {
		value := body[field]
		delete(body, field)
		if schema.Validate(body) == nil {
			t.Fatalf("assignment schema accepts missing %s", field)
		}
		body[field] = value
	}
}

func TestBedrockProbeTransportRejectsPrivateDestinations(t *testing.T) {
	transport, ok := bedrockProbeHTTPClient.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatal("Bedrock probe must use a guarded transport")
	}
	for _, addr := range []string{"127.0.0.1:443", "169.254.169.254:443", "[::1]:443", "[fd00:ec2::254]:443"} {
		conn, err := transport.DialContext(t.Context(), "tcp", addr)
		if conn != nil {
			conn.Close()
			t.Fatalf("probe reached private destination %s", addr)
		}
		if !errors.Is(err, httpsafe.ErrBlocked) {
			t.Fatalf("private destination %s was not blocked: %v", addr, err)
		}
	}
	if bedrockProbeHTTPClient.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("probe must refuse all redirects")
	}
}
