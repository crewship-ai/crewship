package documents

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestSchemaCapabilities (#5233) fixes the capability table: new Docs are
// born at a schema with every agent capability, never by following
// LatestSchema; only older schemas upgrade; a later schema keeps every
// capability an earlier one released; unknown schemas support nothing.
func TestSchemaCapabilities(t *testing.T) {
	want := map[int]string{
		0: "",
		1: "read,edit,restore,export,upgrade",
		2: "read,edit,restore,export,upgrade",
		3: "read,edit,draft,restore,export,upgrade",
		4: "read,edit,draft,propose,accept,restore,export,upgrade",
		5: "read,edit,draft,propose,accept,restore,export,upgrade",
		// Enabling Canvas (6) keeps every capability schema 5 released.
		6: "read,edit,draft,propose,accept,restore,export",
		7: "",
	}
	for schema, list := range want {
		if got := strings.Join(SchemaCapabilities(schema), ","); got != list {
			t.Errorf("schema %d: %s, want %s", schema, got, list)
		}
	}
	for _, c := range []Capability{CapRead, CapEdit, CapDraft, CapPropose, CapAccept, CapRestore, CapExport} {
		if !SchemaAllows(CreateSchema, c) {
			t.Errorf("the creation schema %d lacks %s", CreateSchema, c)
		}
	}
	if SchemaAllows(LatestSchema, CapUpgrade) {
		t.Error("the latest schema cannot upgrade further")
	}
	for schema := 1; schema < LatestSchema; schema++ {
		if !SchemaAllows(schema, CapUpgrade) {
			t.Errorf("schema %d cannot upgrade", schema)
		}
	}
	if CreateSchema > LatestSchema {
		t.Error("the creation schema is newer than the engine's latest schema")
	}
	// Upgrading never takes a capability away (opposition answer 4): every
	// agent-facing capability of a schema stays at every later schema.
	for schema := 1; schema < LatestSchema; schema++ {
		for _, c := range []Capability{CapRead, CapEdit, CapDraft, CapPropose, CapAccept, CapRestore, CapExport} {
			if SchemaAllows(schema, c) && !SchemaAllows(LatestSchema, c) {
				t.Errorf("upgrading schema %d to %d loses %s", schema, LatestSchema, c)
			}
		}
	}
}

// TestSchemaCapabilitiesMatchTheEngine keeps the Go table and the engine's
// SCHEMA_CAPABILITIES and CREATE_VERSION (tools/docs-service/schema-core.mjs)
// equal: both gate the same proposals, drafts and upgrades.
func TestSchemaCapabilitiesMatchTheEngine(t *testing.T) {
	raw, err := os.ReadFile("../../tools/docs-service/schema-core.mjs")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	create := regexp.MustCompile(`export const CREATE_VERSION = (\d+);`).FindStringSubmatch(source)
	if create == nil || create[1] != strconv.Itoa(CreateSchema) {
		t.Fatalf("engine CREATE_VERSION %v, Go CreateSchema %d", create, CreateSchema)
	}
	block := regexp.MustCompile(`(?s)export const SCHEMA_CAPABILITIES = Object\.freeze\(\{(.*?)\n\}\);`).FindStringSubmatch(source)
	if block == nil {
		t.Fatal("engine SCHEMA_CAPABILITIES not found")
	}
	rows := regexp.MustCompile(`(\d+): Object\.freeze\((\[[^\]]*\])\)`).FindAllStringSubmatch(block[1], -1)
	if len(rows) != LatestSchema {
		t.Fatalf("engine lists %d schemas, Go %d", len(rows), LatestSchema)
	}
	for _, row := range rows {
		schema, _ := strconv.Atoi(row[1])
		var list []string
		if err := json.Unmarshal([]byte(strings.ReplaceAll(row[2], "'", `"`)), &list); err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(list, ","), strings.Join(SchemaCapabilities(schema), ","); got != want {
			t.Errorf("schema %d: engine %s, Go %s", schema, got, want)
		}
	}
}

// TestIterationIntentBindsTheDocumentSchema (#5233): a mandate names the Doc's
// own schema; on a schema that supports proposals it is propose-only and may
// name blocks, schema 3 is a direct draft without blocks, anything else is refused.
func TestIterationIntentBindsTheDocumentSchema(t *testing.T) {
	for _, tt := range []struct {
		schema  int
		blocks  string
		valid   bool
		propose bool
	}{
		{3, "", true, false},
		{3, "a", false, false},
		{4, "a,b", true, true},
		{5, "", true, true},
		{5, "a,b", true, true},
		{2, "", false, false},
		{6, "", false, false},
	} {
		intent := IterationIntent{SubmissionID: "submission-1", PageID: "page-1", Schema: tt.schema, Rev: 1, Generation: 0, Blocks: tt.blocks}
		if got := intent.Validate() == nil; got != tt.valid {
			t.Errorf("schema %d blocks %q: valid %v, want %v", tt.schema, tt.blocks, got, tt.valid)
		}
		if tt.valid && intent.Propose() != tt.propose {
			t.Errorf("schema %d: propose %v, want %v", tt.schema, intent.Propose(), tt.propose)
		}
	}
}
