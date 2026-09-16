package api

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// The pair table in openapi_response_shape_test.go grades one thing: that a
// field the encoder always emits is in `required`. It deliberately grades only
// the fields the schema already names, so a schema that simply never learned
// a field passes it — and that is how the 2026-09-15 audit found `Run` without
// `kind`, `mission_id`, `crew_slug` or `pipeline_slug` (#2316, #2325, #2464),
// the backup verify/restore bodies missing eight fields across three PRs
// (#2009, #2251, #2245), `Workspace` without `approvals_retention_days`
// (#2254) and `CrewAssignmentsResponseV1` describing three fields the handler
// has never emitted while lacking the one #2269 added. The generator's
// schemas are hand-declared, so the "OpenAPI up to date" gate only proves the
// generator is deterministic; nothing compared a declaration to its struct.
//
// This table does. For each pair the struct's JSON keys — the wire, read off
// the json tags — must equal the schema's declared properties, in both
// directions: a key the struct emits that the schema does not name is a field
// no client generated from the spec can see, and a property the schema names
// that the struct does not have is a promise the server never keeps. The
// required list is still the other test's job; this one is about the field
// SET.
//
// A handler that writes a map literal cannot be in this table, which is the
// point: promoting the literal to a struct is the unit of work that makes the
// schema checkable (backup verify/restore and the webhook receipts were
// promoted for exactly that reason).
var schemaKeyContracts = []struct {
	name string
	// JSON pointer into internal/api/openapi.gen.json, addressing the object
	// schema that describes this struct. Array indices (oneOf/0) are allowed.
	pointer string
	// A zero value of the struct the handler serializes.
	value any
}{
	{name: "Run", pointer: "/components/schemas/Run", value: runResponse{}},
	{name: "RunList", pointer: "/components/schemas/RunList", value: runListResponse{}},
	{name: "Workspace", pointer: "/components/schemas/Workspace", value: workspaceResponse{}},
	// PATCH /workspaces/{id} references CoreWorkspaceUpdateRequestV2, not
	// the legacy WorkspaceUpdateRequest (0 $refs) — the pointer names the
	// component the operation uses, or the test grades a schema no client
	// ever sees.
	{name: "PATCH /api/v1/workspaces/{workspaceId} body", pointer: "/components/schemas/CoreWorkspaceUpdateRequestV2", value: updateWorkspaceRequest{}},
	{name: "CrewAssignmentsResponseV1[]", pointer: "/components/schemas/CrewAssignmentsResponseV1/items", value: assignmentListItem{}},
	{name: "GET /api/v1/admin/backups/verify", pointer: "/components/schemas/FinalAdminPlatformBackupVerify", value: backupVerifyResponse{}},
	{name: "POST /api/v1/admin/backups/restore", pointer: "/components/schemas/FinalAdminPlatformBackupRestore", value: backupRestoreResponse{}},
	{
		name:    "GET /api/v1/approvals rows[]",
		pointer: "/paths/~1api~1v1~1approvals/get/responses/200/content/application~1json/schema/properties/rows/items",
		value:   harbormaster.Request{},
	},
	{name: "POST .../trigger 202", pointer: "/components/schemas/FinalWebhookFire/oneOf/0", value: webhook.AcceptedReceipt{}},
	{name: "POST .../trigger 200", pointer: "/components/schemas/FinalWebhookFire/oneOf/1", value: webhook.IgnoredReceipt{}},
}

func TestOpenAPIProperties_MatchTheStructsOwnJSONTags(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}

	for _, tc := range schemaKeyContracts {
		t.Run(tc.name, func(t *testing.T) {
			schema, ok := resolvePointer(doc, tc.pointer).(map[string]any)
			if !ok {
				t.Fatalf("no object schema at %s — the pointer is stale", tc.pointer)
			}
			props, _ := schema["properties"].(map[string]any)
			if len(props) == 0 {
				t.Fatalf("schema at %s names no properties; a free-form object grades nothing", tc.pointer)
			}

			always, sometimes := jsonFieldsOf(reflect.TypeOf(tc.value))
			emitted := map[string]bool{}
			for _, f := range always {
				emitted[f] = true
			}
			for f := range sometimes {
				emitted[f] = true
			}

			var undeclared, stale []string
			for f := range emitted {
				if _, ok := props[f]; !ok {
					undeclared = append(undeclared, f)
				}
			}
			for f := range props {
				if !emitted[f] {
					stale = append(stale, f)
				}
			}
			sort.Strings(undeclared)
			sort.Strings(stale)

			if len(undeclared) > 0 {
				t.Errorf("the handler emits these fields and the schema does not name them:\n  %s\n"+
					"A client generated from the spec cannot see them. Add them in cmd/gen-openapi and regenerate.",
					strings.Join(undeclared, ", "))
			}
			if len(stale) > 0 {
				t.Errorf("the schema names these properties and the handler never emits them:\n  %s\n"+
					"Every one is a promise the server does not keep. Remove them in cmd/gen-openapi and regenerate.",
					strings.Join(stale, ", "))
			}
		})
	}
}
