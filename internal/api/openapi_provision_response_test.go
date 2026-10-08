package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Grade the committed OpenAPI against bytes from the handlers, including
// optional job/provenance fields and nullable database values.
func TestProvisionResponseSchemaMatchesHandlerWire(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var nullable func(any)
	nullable = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["nullable"] == true {
				if typ, ok := v["type"].(string); ok {
					v["type"] = []any{typ, "null"}
				}
				delete(v, "nullable")
			}
			for _, child := range v {
				nullable(child)
			}
		case []any:
			for _, child := range v {
				nullable(child)
			}
		}
	}
	nullable(doc)
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("provision.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("provision.json#/components/schemas/RemainingCrewProvisionStatusV1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		features any
		job      *ProvisionJob
	}{
		{name: "idle without provenance"},
		{name: "empty provenance", features: "[]"},
		{name: "null provenance", features: "null"},
		{name: "invalid provenance", features: "invalid"},
		{name: "running", features: `[{"ref":"feature:1","id":"feature","pinned":false}]`, job: &ProvisionJob{Status: "running", Step: 1, Total: 2, Message: "building", Steps: []string{"fetch", "build"}, LogTail: []string{"build output"}, StartedAt: time.Now()}},
		{name: "failed", job: &ProvisionJob{Status: "failed", Error: "build failed", StartedAt: time.Now(), CompletedAt: func() *time.Time { now := time.Now(); return &now }()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestProvisioningHandler(t)
			user := seedTestUser(t, h.db)
			ws := seedTestWorkspace(t, h.db, user)
			crew := seedCrewRow(t, h.db, "schema-crew", ws, "Schema", "schema")
			if _, err := h.db.Exec(`UPDATE crews SET resolved_features = ? WHERE id = ?`, tc.features, crew); err != nil {
				t.Fatal(err)
			}
			if tc.job != nil {
				h.jobs[crew] = tc.job
			}
			req := httptest.NewRequest("GET", "/api/v1/crews/"+crew+"/provision", nil)
			req.SetPathValue("crewId", crew)
			req = withWorkspaceUser(req, user, ws, "OWNER")
			rr := httptest.NewRecorder()
			h.ProvisionStatus(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(payload); err != nil {
				t.Fatal(err)
			}
			// Every wire key needs a declared property: the permissive default alone
			// would accept a schema that silently drops the optional job fields.
			props := doc["components"].(map[string]any)["schemas"].(map[string]any)["RemainingCrewProvisionStatusV1"].(map[string]any)["properties"].(map[string]any)
			for key := range payload {
				if props[key] == nil {
					t.Errorf("undeclared wire field %s", key)
				}
			}
			for _, key := range []string{"status", "cached_image", "toolchain"} {
				value := payload[key]
				delete(payload, key)
				if schema.Validate(payload) == nil {
					t.Errorf("accepted missing %s", key)
				}
				payload[key] = value
			}
		})
	}
	// Rebuild delegates to ProvisionTrigger: its typed unavailable error is
	// Problem Details, while denied access keeps the ordinary error envelope.
	for _, tc := range []struct {
		role string
		code int
	}{{"OWNER", 503}, {"MEMBER", 403}} {
		t.Run(fmt.Sprintf("rebuild %s", tc.role), func(t *testing.T) {
			h := newTestProvisioningHandler(t)
			req := httptest.NewRequest("POST", "/api/v1/crews/schema/rebuild", nil)
			req.SetPathValue("crewId", "schema")
			req = withWorkspaceUser(req, "user", "workspace", tc.role)
			rr := httptest.NewRecorder()
			h.ProvisionRebuild(rr, req)
			if rr.Code != tc.code {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			ptr := fmt.Sprintf("provision.json#/paths/~1api~1v1~1crews~1{crewId}~1rebuild/post/responses/%d/content/application~1json/schema", tc.code)
			responseSchema, err := compiler.Compile(ptr)
			if err != nil {
				t.Fatal(err)
			}
			var payload any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if err := responseSchema.Validate(payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
