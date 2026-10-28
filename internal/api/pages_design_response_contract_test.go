package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validate bytes produced by the real authorized handlers, including populated
// Yjs nodes, retained versions, anchored comments and a connected private chat.
// DTO zero values alone would not catch a mismatched actual response envelope.
func TestDesignActualResponsesMatchConcreteOpenAPIContracts(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("design-spec.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	validate := func(component string, w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("%s status %d: %s", component, w.Code, w.Body)
		}
		schema, e := compiler.Compile("design-spec.json#/components/schemas/" + component)
		if e != nil {
			t.Fatal(e)
		}
		var body any
		if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if e = schema.Validate(body); e != nil {
			t.Fatalf("actual %s violates schema: %v\n%s", component, e, w.Body)
		}
		if e = schema.Validate(map[string]any{}); e == nil {
			t.Fatalf("%s accepts an absent response body", component)
		}
		fields := body.(map[string]any)
		renamed := map[string]any{}
		for k, v := range fields {
			renamed[strings.ToUpper(k)] = v
		}
		if e = schema.Validate(renamed); e == nil {
			t.Fatalf("%s accepts every wire field renamed", component)
		}
	}
	h, ws, owner, call := designFixture(t)
	validate("DesignCreateResponse", call(owner, "create", `{"slug":"board","name":"Contract Design"}`), 201)
	validate("DesignSnapshot", call(owner, "get", ""), 200)
	validate("DesignOperationsResponse", call(owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"frame","type":"frame","name":"Contract frame","x":0,"y":0,"width":360,"height":640,"fill":"#ffffff"}},{"op":"upsert","node":{"id":"title","type":"text","name":"Title","parentId":"frame","x":20,"y":20,"width":200,"height":40,"fill":"#000000","text":"Contract title"}}],"message":"Contract nodes"}`), 200)
	validate("DesignSnapshot", call(owner, "get", ""), 200)
	validate("DesignVersionReceipt", call(owner, "snapshot", `{"message":"Contract checkpoint"}`), 200)
	validate("DesignVersionList", call(owner, "versions", ""), 200)
	validate("DesignCommentsResponse", call(owner, "comments", ""), 200)
	validate("ArtifactProjectComment", call(owner, "comment", `{"version":3,"path":"design.json","selector":"title","quote":"Contract title","text":"Change this title","send_to_agent":false}`), 201)
	validate("DesignCommentsResponse", call(owner, "comments", ""), 200)
	validate("DesignShareResponse", call(owner, "share", `{"version":3}`), 200)
	mustExec(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('design-contract-agent',?,'crew-lookout','Contract agent','design-contract-agent')`, ws)
	mustExec(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('design-contract-chat',?,'design-contract-agent',?,'private')`, ws, owner)
	validate("DesignAgentResponse", call(owner, "agent", `{"chat_id":"design-contract-chat"}`), 200)
	validate("DesignSnapshot", call(owner, "get", ""), 200)
	validate("DesignVersionReceipt", call(owner, "restore", `{"version":1}`), 200)
}
