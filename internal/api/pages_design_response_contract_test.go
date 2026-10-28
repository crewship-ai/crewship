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

// Create performs the engine operation before opening its publication transaction.
// A cold subprocess can exhaust the same bounded availability window as a live
// server. Replay only this declared pre-commit failure, once, after checking the
// actual database; an ambiguous write or any other response must fail the test.
func designContractCreate(t *testing.T, h *PageHandler, workspace string, create func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	tables := []string{"pages", "artifact_version_heads", "artifact_design_documents", "artifact_versions", "artifact_version_blobs"}
	baseline := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := h.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=?`, workspace).Scan(&count); err != nil {
			t.Fatal(err)
		}
		baseline[table] = count
	}
	response := create()
	if response.Code != 503 {
		return response
	}
	var failure map[string]string
	if json.Unmarshal(response.Body.Bytes(), &failure) != nil || len(failure) != 1 || failure["error"] != "Design engine is unavailable; changes are not acknowledged" {
		t.Fatalf("unexpected unavailable Create response: %s", response.Body)
	}
	for _, table := range tables {
		var count int
		if err := h.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != baseline[table] {
			t.Fatalf("unavailable Create changed %s before replay: before=%d after=%d error=%v", table, baseline[table], count, err)
		}
	}
	t.Logf("first Create attempt: status=%d error=%q; unchanged Page/head/live-state/version/blob counts verified, replaying the unchanged request once", response.Code, failure["error"])
	return create()
}

func TestDesignUnavailableCreateHasNoWritesBeforeSingleReplay(t *testing.T) {
	h, workspace, owner, call := designFixture(t)
	originalPath := os.Getenv("PATH")
	unavailablePath := t.TempDir()
	attempts := 0
	response := designContractCreate(t, h, workspace, func() *httptest.ResponseRecorder {
		attempts++
		if attempts == 1 {
			t.Setenv("PATH", unavailablePath)
		} else {
			t.Setenv("PATH", originalPath)
		}
		return call(owner, "create", `{"slug":"board","name":"Availability contract"}`)
	})
	if attempts != 2 || response.Code != 201 {
		t.Fatalf("bounded Create recovery attempts=%d response=%d %s", attempts, response.Code, response.Body)
	}
	for _, table := range []string{"pages", "artifact_version_heads", "artifact_design_documents", "artifact_versions"} {
		var count int
		if err := h.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 1 {
			t.Fatalf("single recovered Create %s rows=%d error=%v", table, count, err)
		}
	}
}

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
	validate("DesignCreateResponse", designContractCreate(t, h, ws, func() *httptest.ResponseRecorder {
		return call(owner, "create", `{"slug":"board","name":"Contract Design"}`)
	}), 201)
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
