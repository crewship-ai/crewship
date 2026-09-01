package api

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// The spec's `required` list must be DERIVED from the response struct, not
// written next to it by hand.
//
// Hand-writing it re-creates the defect this whole exercise came out of: a
// description of what the code does, maintained beside the code, drifting from
// it. /api/v1/approvals had four such descriptions — the generated spec, the
// web client's zod schema, docs/api-reference/approvals.mdx and the Go struct.
// Three agreed with each other and none with the wire.
//
// So this test reads the one artifact that cannot drift, because it IS the
// wire: the `json` struct tags. A field without `,omitempty` is emitted on
// every response and belongs in `required`. A field with it may be absent and
// must not. Nothing here is a judgement call, which is the point — the
// generator's required list is checked against the encoder's own behaviour.
//
// Adding a pair to this table is the unit of work for tightening the spec:
// the pair goes red, its message names the exact fields, cmd/gen-openapi is
// updated, the ratchet in cmd/gen-openapi/response_shape_contract_test.go
// drops by one.
var responseShapeContracts = []struct {
	name string
	// JSON pointer into internal/api/openapi.gen.json, addressing the object
	// schema that describes this struct.
	pointer string
	// A zero value of the struct the handler serializes.
	value any
}{
	{
		name:    "GET /api/v1/inbox rows[]",
		pointer: "/components/schemas/FinalInboxList/properties/rows/items",
		value:   inboxItemResponse{},
	},
	{
		name:    "GET /api/v1/inbox envelope",
		pointer: "/components/schemas/FinalInboxList",
		value:   inboxListResponse{},
	},
	{
		name:    "GET /api/v1/approvals rows[]",
		pointer: "/paths/~1api~1v1~1approvals/get/responses/200/content/application~1json/schema/properties/rows/items",
		value:   harbormaster.Request{},
	},
	// ── B2: the daily surfaces ─────────────────────────────────────────────
	// These three already carried a `required` list. It was written by hand and
	// it is incomplete — which is the failure mode this test exists to make
	// impossible to leave in place.
	{
		name:    "Workspace",
		pointer: "/components/schemas/Workspace",
		value:   workspaceResponse{},
	},
	{
		name:    "Crew",
		pointer: "/components/schemas/Crew",
		value:   crewResponse{},
	},
	{
		name:    "Agent",
		pointer: "/components/schemas/Agent",
		value:   agentResponse{},
	},
	{
		name:    "Agent.crew",
		pointer: "/components/schemas/AgentCrew",
		value:   agentCrewInfo{},
	},
	{
		// Added after a mechanical edit appended Crew's and Agent's required
		// fields onto THIS schema's list instead of theirs. The cross-check
		// caught that crew and agent were still wrong; nothing caught the
		// corruption here, because Project had no pair. A schema being edited
		// gets a pair first.
		name:    "Project",
		pointer: "/components/schemas/Project",
		value:   projectResponse{},
	},
	{
		name:    "Issue",
		pointer: "/components/schemas/Issue",
		value:   issueResponse{},
	},
	{
		name:    "Issue.labels[]",
		pointer: "/components/schemas/Label",
		value:   labelResponse{},
	},
	{
		name:    "Skill",
		pointer: "/components/schemas/Skill",
		value:   skillResponse{},
	},
	{
		name:    "WorkspaceCounts",
		pointer: "/components/schemas/WorkspaceCounts",
		value:   workspaceCounts{},
	},
	{
		name:    "CrewCounts",
		pointer: "/components/schemas/CrewCounts",
		value:   crewCountResponse{},
	},
	{
		name:    "AgentCounts",
		pointer: "/components/schemas/AgentCounts",
		value:   agentCounts{},
	},
	// ── B3: admin and keeper ───────────────────────────────────────────────
	// Inline schemas, so the pointer addresses the path rather than a
	// component. Note what is NOT here: /admin/stats, /admin/workspaces and
	// /admin/users serialize structs declared INSIDE their handler functions,
	// and a function-local type cannot be named from a test. See §2b of the
	// PRD — those need their DTO promoted to package level before the
	// mechanism can reach them at all.
	{
		name:    "GET /api/v1/admin/security-posture",
		pointer: "/paths/~1api~1v1~1admin~1security-posture/get/responses/200/content/application~1json/schema",
		value:   securityPostureResponse{},
	},
	{
		name:    "GET /api/v1/admin/memory/config",
		pointer: "/paths/~1api~1v1~1admin~1memory~1config/get/responses/200/content/application~1json/schema",
		value:   memoryConfigResponse{},
	},
	{
		name:    "GET /api/v1/admin/memory/stats",
		pointer: "/paths/~1api~1v1~1admin~1memory~1stats/get/responses/200/content/application~1json/schema",
		value:   memoryStatsResponse{},
	},
	{
		name:    "GET /api/v1/admin/memory/versions",
		pointer: "/paths/~1api~1v1~1admin~1memory~1versions/get/responses/200/content/application~1json/schema",
		value:   memVersionsListResponse{},
	},
	// ── B3: credentials ────────────────────────────────────────────────────
	// All three already carried a hand-written required list, and all three
	// were silently wrong the same way: nullable `*T` fields with no
	// `,omitempty` were left out, on the reading that a nullable field cannot
	// be required. It can. Required means present; a *string with no omitempty
	// is present on every response, as null.
	{
		name:    "Credential",
		pointer: "/components/schemas/Credential",
		value:   credentialResponse{},
	},
	{
		name:    "CredentialField",
		pointer: "/components/schemas/CredentialField",
		value:   credentialFieldResponse{},
	},
	{
		name:    "CredentialBinding",
		pointer: "/components/schemas/CredentialBinding",
		value:   credentialBindingResponse{},
	},
	// ── B4: keeper ─────────────────────────────────────────────────────────
	{
		name:    "GET /api/v1/admin/keeper/health",
		pointer: "/paths/~1api~1v1~1admin~1keeper~1health/get/responses/200/content/application~1json/schema",
		value:   keeperHealthResponse{},
	},
	{
		name:    "GET /api/v1/admin/keeper/config",
		pointer: "/paths/~1api~1v1~1admin~1keeper~1config/get/responses/200/content/application~1json/schema",
		value:   keeperConfigResponse{},
	},
	{
		name:    "GET /api/v1/admin/keeper/aux",
		pointer: "/paths/~1api~1v1~1admin~1keeper~1aux/get/responses/200/content/application~1json/schema",
		value:   keeperAuxResponse{},
	},
	{
		name:    "GET /api/v1/admin/keeper/judge/models",
		pointer: "/paths/~1api~1v1~1admin~1keeper~1judge~1models/get/responses/200/content/application~1json/schema",
		value:   judgeModelsResponse{},
	},
	// ── B4: connectors, integrations, profile ──────────────────────────────
	{name: "ConnectorListItem", pointer: "/components/schemas/ConnectorListItem", value: ConnectorListItem{}},
	{name: "ConnectorVerifyResponse", pointer: "/components/schemas/ConnectorVerifyResponse", value: VerifyResponse{}},
	{name: "ConnectorInstallResponse", pointer: "/components/schemas/ConnectorInstallResponse", value: InstallResponse{}},
	{name: "CredentialProbeResponse", pointer: "/components/schemas/CredentialProbeResponse", value: testResult{}},
	{name: "CredentialRotation", pointer: "/components/schemas/CredentialRotation", value: rotationResponse{}},
	{name: "AgentIntegrationBinding", pointer: "/components/schemas/AgentIntegrationBinding", value: agentMCPBindingResponse{}},
	{name: "IntegrationTool", pointer: "/components/schemas/IntegrationTool", value: toolBindingResponse{}},
	{name: "Profile", pointer: "/components/schemas/Profile", value: userProfileResponse{}},
	{name: "CredentialTestResponse", pointer: "/components/schemas/CredentialTestResponse", value: testConnectionResponse{}},
}

func TestOpenAPIRequired_MatchesTheStructsOwnJSONTags(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}

	for _, tc := range responseShapeContracts {
		t.Run(tc.name, func(t *testing.T) {
			schema, ok := resolvePointer(doc, tc.pointer).(map[string]any)
			if !ok {
				t.Fatalf("no object schema at %s — the pointer is stale", tc.pointer)
			}

			always, sometimes := jsonFieldsOf(reflect.TypeOf(tc.value))
			declared := map[string]bool{}
			if props, ok := schema["properties"].(map[string]any); ok {
				for name := range props {
					declared[name] = true
				}
			}
			required := map[string]bool{}
			if list, ok := schema["required"].([]any); ok {
				for _, name := range list {
					if s, ok := name.(string); ok {
						required[s] = true
					}
				}
			}

			var missing, spurious []string
			for _, f := range always {
				// Only grade fields the schema actually describes; a schema may
				// legitimately document a subset while it is being tightened.
				if declared[f] && !required[f] {
					missing = append(missing, f)
				}
			}
			for f := range required {
				if sometimes[f] {
					spurious = append(spurious, f)
				}
			}
			sort.Strings(missing)
			sort.Strings(spurious)

			if len(missing) > 0 {
				t.Errorf("these fields are emitted on every response (no `,omitempty`) but are not in `required`:\n  %s\n"+
					"Until they are, a body that omits them — or renames them all — validates, and the contract gate cannot see it.",
					strings.Join(missing, ", "))
			}
			if len(spurious) > 0 {
				t.Errorf("these fields carry `,omitempty` but are marked `required`:\n  %s\n"+
					"A response that legitimately omits them would be reported as a contract violation, which is how a gate earns being ignored.",
					strings.Join(spurious, ", "))
			}
		})
	}
}

// jsonFieldsOf splits a struct's JSON field names into those the encoder always
// emits and those it may omit. Embedded structs are flattened the way
// encoding/json flattens them.
func jsonFieldsOf(t reflect.Type) (always []string, sometimes map[string]bool) {
	sometimes = map[string]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous && f.Tag.Get("json") == "" {
				walk(f.Type)
				continue
			}
			if f.PkgPath != "" {
				continue // unexported: never serialized
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name // encoding/json falls back to the field name
			}
			if strings.Contains(opts, "omitempty") {
				sometimes[name] = true
			} else {
				always = append(always, name)
			}
		}
	}
	walk(t)
	return always, sometimes
}

// resolvePointer walks an RFC 6901 JSON pointer, with the usual ~1 / ~0
// unescaping, over a decoded document.
func resolvePointer(doc any, pointer string) any {
	cur := doc
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[token]
	}
	return cur
}

var _ = bytes.MinRead
