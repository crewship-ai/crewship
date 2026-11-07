package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDesignMCPRejectsSpoofedOrAmbiguousArguments(t *testing.T) {
	for _, raw := range []string{`{"slug":"demo","operations":[],"human_user_id":"victim"}`, `{"slug":"demo","slug":"other","operations":[]}`, `{"slug":"../demo","operations":[]}`, `{"slug":"demo","operations":null}`, `{"slug":"demo","operations":[{"op":"upsert","node":{"id":"a","id":"b"}}]}`} {
		if _, handled, err := designMCPPacket("design_apply", []byte(raw)); !handled || err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestDesignMCPForwardsOriginalRunAndOperations(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/internal/pages/design-tool" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-design-ipc" {
			t.Error("lost original authority")
		}
		var body struct {
			Operation string `json:"operation"`
			Slug      string `json:"slug"`
			Payload   struct {
				Operations []map[string]any `json:"operations"`
			} `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Operation != "design_apply" || body.Slug != "demo" || len(body.Payload.Operations) != 1 {
			t.Error("changed operations")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"mandate revoked"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-design-ipc"
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"design_apply","arguments":{"slug":"demo","operations":[{"op":"upsert","node":{"id":"cart","type":"frame","name":"Cart","x":0,"y":0,"width":390,"height":844,"fill":"#fff"}}]}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	isError, text := documentCreateMCPResult(t, w)
	if !isError || !strings.Contains(text, "mandate revoked") || calls.Load() != 1 {
		t.Fatalf("revocation hidden/retried: %v %s %d", isError, text, calls.Load())
	}
}

func TestDesignMCPBoundsNestingBeforeRecursiveDecoder(t *testing.T) {
	raw := `{"slug":"demo","operations":` + strings.Repeat("[", 130) + strings.Repeat("]", 130) + `}`
	if _, handled, err := designMCPPacket("design_apply", []byte(raw)); !handled || err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("unbounded recursive tool argument: %v", err)
	}
}

// Agents express prototype links (#4449) through the same design_apply path as
// every other node field: the catalog names linkTo and the packet forwards it
// unchanged to the host, where the Yjs engine validates it.
func TestDesignMCPCarriesPrototypeLinks(t *testing.T) {
	var apply *memoryMCPToolDescriptor
	for _, tool := range designMCPCatalog() {
		if tool.Name == "design_apply" {
			apply = &tool
		}
	}
	if apply == nil || !strings.Contains(apply.Description, "linkTo") || !strings.Contains(string(apply.InputSchema), "linkTo") {
		t.Fatalf("design_apply does not document prototype links: %+v", apply)
	}
	raw := `{"slug":"demo","operations":[{"op":"upsert","node":{"id":"next","linkTo":"checkout"}},{"op":"upsert","node":{"id":"back","linkTo":""}}]}`
	packet, handled, err := designMCPPacket("design_apply", []byte(raw))
	if !handled || err != nil {
		t.Fatalf("link operations refused: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(string(packet), `"linkTo":"checkout"`) || !strings.Contains(string(packet), `"linkTo":""`) {
		t.Fatalf("link fields not forwarded: %s", packet)
	}
}
