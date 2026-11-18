package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

type nativeProbeCountingTransport struct{ calls int }

func (p *nativeProbeCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
}

func countNativeProbeCalls(t *testing.T) *nativeProbeCountingTransport {
	t.Helper()
	p := &nativeProbeCountingTransport{}
	previousClient, previousTransport := http.DefaultClient, http.DefaultTransport
	http.DefaultTransport = p
	http.DefaultClient = &http.Client{Transport: p}
	t.Cleanup(func() { http.DefaultClient, http.DefaultTransport = previousClient, previousTransport })
	return p
}

func TestNativeFileAndLoginValuesNeverEnterTokenProbes(t *testing.T) {
	for _, tc := range []struct{ typ, provider string }{
		{"SECRET", "GITLAB"}, {"GENERIC_SECRET", "GITLAB"}, {"USERPASS", "GITLAB"},
		{"SSH_KEY", "GITLAB"}, {"CERTIFICATE", "GITLAB"}, {"OAUTH2", "GITLAB"},
		{"AI_CLI_TOKEN", "GOOGLE"}, {"AI_CLI_TOKEN", "OPENAI"},
		{"PROVIDER_LOGIN", "OPENAI"}, {"UNKNOWN", "GITLAB"},
	} {
		t.Run(tc.typ+"/"+tc.provider, func(t *testing.T) {
			transport := countNativeProbeCalls(t)
			got := probeProvider(context.Background(), tc.provider, tc.typ, `{"access_token":"synthetic-only","refresh_token":"synthetic-only"}`, false)
			if transport.calls != 0 || got.Supported || probeSupported(tc.provider, tc.typ) {
				t.Fatalf("non-token type %s reached a token probe: calls%d supported%t", tc.typ, transport.calls, got.Supported)
			}
			if tc.typ == "UNKNOWN" && got.Valid {
				t.Fatal("unknown type claimed validity")
			}
			if credentialTestable(credentialResponse{Provider: tc.provider, Type: tc.typ}) {
				t.Fatal("non-token type offered an API-key test")
			}
		})
	}
}

func TestNativeTokenProbesKeepSupportedScalarTypes(t *testing.T) {
	for _, typ := range []string{"API_KEY", "CLI_TOKEN"} {
		t.Run(typ, func(t *testing.T) {
			transport := countNativeProbeCalls(t)
			got := probeProvider(t.Context(), "GITLAB", typ, "synthetic-only", false)
			if transport.calls != 1 || !got.Supported || !got.Valid {
				t.Fatal("supported token probe was lost")
			}
		})
	}
	if !probeSupported("ANTHROPIC", "AI_CLI_TOKEN") || !probeSupported("NONE", "ENDPOINT_URL") {
		t.Fatal("dedicated OAuth probe or stored endpoint reachability lost")
	}
}

func TestNativeProbeMetadataRespectsExplicitTypesAndLegacyDefault(t *testing.T) {
	h := NewCredentialHandler(nil, newTestLogger())
	for _, typ := range []string{"", "API_KEY", "CLI_TOKEN", "SECRET", "SSH_KEY", "CERTIFICATE", "USERPASS", "OAUTH2", "UNKNOWN"} {
		rr := httptest.NewRecorder()
		h.DefaultEnvVar(rr, httptest.NewRequest("GET", "/api/v1/credentials/default-env-var?provider=GITLAB&type="+typ, nil))
		var got struct {
			Testable bool `json:"testable"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := typ == "" || typ == "API_KEY" || typ == "CLI_TOKEN"; got.Testable != want {
			t.Errorf("metadata type %q: testable=%t want%t", typ, got.Testable, want)
		}
	}
}

func TestNativeEndpointProbeCannotBecomeVendorAuthentication(t *testing.T) {
	transport := countNativeProbeCalls(t)
	got := probeProvider(t.Context(), "GITLAB", "ENDPOINT_URL", "https://fixture.example/v1", false)
	if transport.calls != 0 || got.Supported || !got.Valid {
		t.Fatal("raw endpoint syntax check performed or claimed authentication")
	}
}

func TestNativeCompatSyntaxCheckDoesNotClaimUpstreamProbe(t *testing.T) {
	transport := countNativeProbeCalls(t)
	for _, typ := range []string{"API_KEY", "CLI_TOKEN"} {
		got := probeProvider(t.Context(), "OPENAI_COMPAT", typ, `{"baseURL":"https://fixture.example/v1","apiKey":"synthetic-only"}`, false)
		if transport.calls != 0 || got.Supported || !got.Valid {
			t.Fatal("raw compatibility endpoint syntax check claimed an upstream probe")
		}
	}
}

func TestNativeProbeHTTPRejectsFileMaterialWithoutOutboundRequest(t *testing.T) {
	transport := countNativeProbeCalls(t)
	h := NewCredentialHandler(nil, newTestLogger())
	for _, typ := range []string{"SECRET", "GENERIC_SECRET", "USERPASS", "SSH_KEY", "CERTIFICATE", "OAUTH2", "UNKNOWN"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/credentials/test", strings.NewReader(fmt.Sprintf(`{"provider":"GITLAB","type":%q,"value":"synthetic-only"}`, typ)))
		rr := httptest.NewRecorder()
		h.Test(rr, req)
		if transport.calls != 0 {
			t.Fatalf("raw test sent %s material to a token endpoint", typ)
		}
		if typ == "UNKNOWN" {
			if rr.Code != http.StatusBadRequest {
				t.Fatal("unknown raw-test type was not rejected")
			}
			continue
		}
		var got testResult
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Supported {
			t.Fatalf("raw non-token test claimed a probe for %s", typ)
		}
	}
	// The historical raw-test shape omitted type for a scalar API key.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/credentials/test", strings.NewReader(`{"provider":"GITLAB","value":"synthetic-only"}`))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusOK || transport.calls != 1 {
		t.Fatal("legacy raw API-key test default was lost")
	}
}

func TestNativeStoredFileProbeCannotSendOrAuditAuthenticationSuccess(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	transport := countNativeProbeCalls(t)
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	enc, err := encryption.Encrypt("synthetic-only")
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range []string{"SECRET", "GENERIC_SECRET", "USERPASS", "SSH_KEY", "CERTIFICATE", "OAUTH2"} {
		id := fmt.Sprintf("native-probe-%d", i)
		if _, err := db.Exec(`INSERT INTO credentials(id,workspace_id,name,type,provider,encrypted_value,username,status,scope,created_by) VALUES (?,?,?,?,'GITLAB',?,'fixture-user','ACTIVE','WORKSPACE',?)`, id, workspace, id, typ, enc, user); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/credentials/"+id+"/test", nil)
		req.SetPathValue("credentialId", id)
		req = withWorkspaceUser(req, user, workspace, "OWNER")
		rr := httptest.NewRecorder()
		NewCredentialHandler(db, newTestLogger()).TestStored(rr, req)
		var got testResult
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Supported || transport.calls != 0 {
			t.Fatalf("stored %s reached token probe or claimed a check", typ)
		}
		var metadata string
		if err := db.QueryRow(`SELECT metadata_json FROM credential_audit WHERE credential_id=? AND event_type='TEST'`, id).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		var audit map[string]any
		if json.Unmarshal([]byte(metadata), &audit) != nil || audit["supported"] != false {
			t.Fatal("unsupported probe not represented truthfully in audit")
		}
		if _, claimed := audit["valid"]; claimed {
			t.Fatal("unsupported probe audited authentication success")
		}
	}
}
