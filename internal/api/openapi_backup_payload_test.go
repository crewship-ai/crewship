package api

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Validate real encoded response structs, not just their property names:
// optional diagnostic slices serialize as null when nothing was recorded.
func TestOpenAPIBackupSchemas_AcceptEncodedResponses(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	// Translate the OpenAPI 3.0 nullable keyword for the JSON Schema
	// validator; this preserves existing nullable manifest semantics.
	var normalizeNullable func(any)
	normalizeNullable = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["nullable"] == true {
				if typ, ok := v["type"].(string); ok {
					v["type"] = []any{typ, "null"}
				}
				delete(v, "nullable")
			}
			for _, child := range v {
				normalizeNullable(child)
			}
		case []any:
			for _, child := range v {
				normalizeNullable(child)
			}
		}
	}
	normalizeNullable(document)
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7
	if err := compiler.AddResource("backup-api.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, component string
		response        any
	}{
		{"verify without diagnostics", "FinalAdminPlatformBackupVerify", backupVerifyResponse{Valid: true}},
		{"verify with diagnostics", "FinalAdminPlatformBackupVerify", backupVerifyResponse{TableRowCountMismatches: []backup.TableRowCountMismatch{{Table: "missions", Recorded: 2, Actual: 1}}}},
		{"restore without diagnostics", "FinalAdminPlatformBackupRestore", backupRestoreResponse{}},
		{"restore with empty diagnostics", "FinalAdminPlatformBackupRestore", backupRestoreResponse{DroppedCrewFilesystems: []string{}, SecurityLevelClamps: []backup.SecurityLevelClamp{}, DroppedColumns: []backup.DroppedColumn{}, PayloadRowCountMismatches: []backup.TableRowCountMismatch{}, RowsInsertedShortfalls: []backup.TableRowCountMismatch{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := compiler.Compile("backup-api.json#/components/schemas/" + tc.component)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(tc.response)
			if err != nil {
				t.Fatal(err)
			}
			var payload any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(payload); err != nil {
				t.Fatalf("schema rejects real response: %v", err)
			}
			field := "table_row_count_mismatches"
			if tc.component == "FinalAdminPlatformBackupRestore" {
				field = "rows_inserted_shortfalls"
			}
			payload.(map[string]any)[field] = "not a list"
			if schema.Validate(payload) == nil {
				t.Fatal("schema accepted diagnostic text instead of an array or null")
			}
		})
	}
}
