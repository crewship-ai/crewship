package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// PRD section 0 point 9: every MCP tool that writes native Craft content or
// custody must carry the lease generation, and the sidecar must refuse a call
// without a canonical one before it reaches the host. A new tool must be
// classified here or the test fails.
func TestNativeCraftMCPWriteToolsRequireLeaseGeneration(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	handler := s.buildHandler(NewProxy(ProxyConfig{CredStore: NewCredStore(), Allowlist: NewDomainAllowlist(nil), Logger: testLogger()}))
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9119/mcp/craft-native", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Result.Tools) == 0 {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body)
	}
	// generation: tool must require it. exempt: documented lease-free by design.
	classes := map[string]string{
		"craft_apply":   "generation", // publishes a version
		"craft_renew":   "generation",
		"craft_release": "generation",
		"craft_acquire": "exempt", // creates the generation
		"craft_read":    "exempt", // read only
		"craft_propose": "exempt", // records a proposal; the document is unchanged and a human accept is fenced
	}
	seen := map[string]bool{}
	for _, tool := range listed.Result.Tools {
		seen[tool.Name] = true
		class, ok := classes[tool.Name]
		if !ok {
			t.Errorf("native Craft MCP tool %q is not lease-classified", tool.Name)
			continue
		}
		required := strings.Join(tool.InputSchema.Required, ",")
		if class == "generation" && !strings.Contains(required, "generation") {
			t.Errorf("%s writes but does not require generation (required: %q)", tool.Name, required)
		}
		if class == "exempt" && strings.Contains(required, "generation") {
			t.Errorf("%s is classified lease-free but requires generation; reclassify", tool.Name)
		}
	}
	for name := range classes {
		if !seen[name] {
			t.Errorf("classified tool %q is no longer exposed", name)
		}
	}
	for _, args := range []string{
		`{"base_version":"1","request_id":"r","commands":[{"command":"text.insert","params":{}}]}`,
		`{"generation":"0","base_version":"1","request_id":"r","commands":[{"command":"text.insert","params":{}}]}`,
		`{"generation":"01","base_version":"1","request_id":"r","commands":[{"command":"text.insert","params":{}}]}`,
		`{"generation":1,"base_version":"1","request_id":"r","commands":[{"command":"text.insert","params":{}}]}`,
	} {
		if _, ok := nativeCraftToolPacket("craft_apply", json.RawMessage(args)); ok {
			t.Errorf("apply without a canonical generation was forwarded: %s", args)
		}
	}
	for _, op := range []string{"renew", "release"} {
		if _, ok := nativeCraftToolPacket("craft_"+op, json.RawMessage(`{}`)); ok {
			t.Errorf("%s without generation was forwarded", op)
		}
	}
}
