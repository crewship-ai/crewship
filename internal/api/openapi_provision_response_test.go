package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
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
	// JSON Schema needs an adapted copy; preserve the original OpenAPI for
	// property/contract assertions below.
	var schemaDoc map[string]any
	if err := json.Unmarshal(raw, &schemaDoc); err != nil {
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
	nullable(schemaDoc)
	raw, err = json.Marshal(schemaDoc)
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
		name         string
		features     any
		job          *ProvisionJob
		wantFeatures bool
		built        *devcontainer.ToolchainInventory
	}{
		{name: "idle without provenance"},
		{name: "built managed toolchain", built: &devcontainer.ToolchainInventory{
			SchemaVersion: 1, Status: "available", ImageID: "sha256:built",
			Tools: []devcontainer.ToolchainTool{{
				Binary: "codex", Status: "available", Path: "/usr/local/bin/codex", Version: "1.2.3",
				ManagedPath: "/opt/crewship/toolchain/codex", ManagedVersion: "1.2.3",
				LaunchArtifact: &managedlaunch.Artifact{Path: "/opt/crewship/toolchain/codex", SHA256: strings.Repeat("a", 64), Format: "static_elf"},
			}},
			Qualification: &devcontainer.ToolchainQualification{Status: "qualified", ImageID: "sha256:built", Tools: []devcontainer.ToolchainProbe{{Binary: "codex", Status: "available"}}},
		}},
		{name: "built legacy toolchain", built: &devcontainer.ToolchainInventory{
			SchemaVersion: 1, Status: "available",
			Tools: []devcontainer.ToolchainTool{{Binary: "codex", Status: "unavailable"}},
		}},
		{name: "empty provenance", features: "[]", wantFeatures: true},
		{name: "null provenance", features: "null", wantFeatures: true},
		{name: "invalid provenance", features: "invalid"},
		{name: "running", wantFeatures: true, features: `[{"ref":"feature:1","id":"feature","pinned":false}]`, job: &ProvisionJob{Status: "running", Step: 1, Total: 2, Message: "building", Steps: []string{"fetch", "build"}, LogTail: []string{"build output"}, StartedAt: time.Now()}},
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
			if tc.built != nil {
				requirements, err := json.Marshal(devcontainer.AggregatedRequirements{Toolchain: tc.built})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.db.Exec(`UPDATE crews SET cached_image = ?, cached_requirements = ? WHERE id = ?`, "sha256:built", string(requirements), crew); err != nil {
					t.Fatal(err)
				}
			}
			if tc.job != nil {
				h.mu.Lock()
				h.jobs[crew] = tc.job
				h.mu.Unlock()
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
			features, present := payload["resolved_features"]
			if present != tc.wantFeatures {
				t.Fatalf("provenance presence=%v, want %v", present, tc.wantFeatures)
			}
			if tc.features == "null" && features != nil {
				t.Fatalf("null provenance: %v", features)
			}
			if tc.features == "[]" {
				if values, ok := features.([]any); !ok || len(values) != 0 {
					t.Fatalf("empty provenance: %v", features)
				}
			}
			if err := schema.Validate(payload); err != nil {
				t.Fatal(err)
			}
			// Validate declarations recursively: permissive additional properties
			// must not hide missing generated client fields inside tool inventories.
			statusSchema := doc["components"].(map[string]any)["schemas"].(map[string]any)["RemainingCrewProvisionStatusV1"].(map[string]any)
			assertProvisionWireFieldsDeclared(t, payload, statusSchema, "$")
			if tc.built != nil {
				toolchain, ok := payload["toolchain"].(map[string]any)
				if !ok || toolchain["built"] == nil {
					t.Fatal("fixture did not exercise built toolchain")
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
	}{{"OWNER", 503}, {"MEMBER", 403}, {"OWNER", 409}, {"OWNER", 429}} {
		t.Run(fmt.Sprintf("rebuild %s %d", tc.role, tc.code), func(t *testing.T) {
			var h *ProvisioningHandler
			crew, workspace := "schema", "workspace"
			if tc.code == 409 || tc.code == 429 {
				h, workspace, crew = covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
				if tc.code == 409 {
					h.mu.Lock()
					h.jobs[crew] = &ProvisionJob{CrewID: crew, Status: "running"}
					h.mu.Unlock()
				} else {
					h.rateLimiter.mu.Lock()
					h.rateLimiter.running[workspace] = ratelimitcfg.Int(ratelimitcfg.KeyProvMaxConcurrentWS)
					h.rateLimiter.mu.Unlock()
				}
			} else {
				h = newTestProvisioningHandler(t)
			}
			req := httptest.NewRequest("POST", "/api/v1/crews/"+crew+"/rebuild", nil)
			req.SetPathValue("crewId", crew)
			req = withWorkspaceUser(req, "user", workspace, tc.role)
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
			if tc.code == 409 {
				body := payload.(map[string]any)
				if body["job_status"] != "running" {
					t.Fatalf("missing conflict status: %v", body)
				}
				// Unknown extension fields remain legal. A payload satisfying
				// both permissive envelopes must not fail an exclusive union.
				body["error"] = "extension diagnostic"
				if err := responseSchema.Validate(body); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// This contract contains inline objects/arrays; walk only its emitted wire data.
func assertProvisionWireFieldsDeclared(t *testing.T, value any, schema map[string]any, path string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: missing object properties", path)
			return
		}
		for key, child := range value {
			childSchema, ok := properties[key].(map[string]any)
			if !ok {
				t.Errorf("undeclared wire field %s.%s", path, key)
				continue
			}
			assertProvisionWireFieldsDeclared(t, child, childSchema, path+"."+key)
		}
	case []any:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			t.Errorf("%s: missing array item schema", path)
			return
		}
		for index, child := range value {
			assertProvisionWireFieldsDeclared(t, child, items, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}
