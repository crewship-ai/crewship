package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestSlashFormSchemaForInputs is the translation table. Every
// InputSpec.Type the DSL accepts appears here with the widget it must
// draw and the value_type the client must send back, because the two
// answers are different and the wire carries both.
func TestSlashFormSchemaForInputs(t *testing.T) {
	cases := []struct {
		name          string
		in            pipeline.InputSpec
		wantType      string
		wantValueType string
		wantDefault   string
		wantRequired  bool
		wantHelp      string
	}{
		{
			name:          "string",
			in:            pipeline.InputSpec{Name: "obdobi", Type: "string"},
			wantType:      "text",
			wantValueType: "string",
		},
		{
			name:          "string with default",
			in:            pipeline.InputSpec{Name: "ucetnictvi_root", Type: "string", Default: "Unify - Účetnictví"},
			wantType:      "text",
			wantValueType: "string",
			wantDefault:   "Unify - Účetnictví",
		},
		{
			// The author's description is the one place they say what the
			// value means; without it the form is a bare box labelled
			// `obdobi`.
			name:          "description becomes field help",
			in:            pipeline.InputSpec{Name: "obdobi", Type: "string", Description: "YYYY-MM; empty means the previous month"},
			wantType:      "text",
			wantValueType: "string",
			wantHelp:      "YYYY-MM; empty means the previous month",
		},
		{
			name:          "required string",
			in:            pipeline.InputSpec{Name: "vypis_odesilatel", Type: "string", Required: true},
			wantType:      "text",
			wantValueType: "string",
			wantRequired:  true,
		},
		{
			// The whole reason defaults are formatted rather than
			// fmt.Sprint'd: JSON hands every number over as a float64,
			// and "42.0" is not what the routine declared.
			name:          "integer default keeps its shape",
			in:            pipeline.InputSpec{Name: "limit", Type: "integer", Default: float64(42)},
			wantType:      "number",
			wantValueType: "integer",
			wantDefault:   "42",
		},
		{
			name:          "number default",
			in:            pipeline.InputSpec{Name: "rate", Type: "number", Default: 0.5},
			wantType:      "number",
			wantValueType: "number",
			wantDefault:   "0.5",
		},
		{
			// %v would have printed this as 1e+06.
			name:          "large number default does not go exponential",
			in:            pipeline.InputSpec{Name: "cap", Type: "number", Default: float64(1000000)},
			wantType:      "number",
			wantValueType: "number",
			wantDefault:   "1000000",
		},
		{
			name:          "boolean default is lowercase",
			in:            pipeline.InputSpec{Name: "dry", Type: "boolean", Default: true},
			wantType:      "boolean",
			wantValueType: "boolean",
			wantDefault:   "true",
		},
		{
			name:          "false boolean default still renders",
			in:            pipeline.InputSpec{Name: "verbose", Type: "boolean", Default: false},
			wantType:      "boolean",
			wantValueType: "boolean",
			wantDefault:   "false",
		},
		{
			name:          "array default is compact JSON",
			in:            pipeline.InputSpec{Name: "tags", Type: "array", Default: []any{"a", "b"}},
			wantType:      "textarea",
			wantValueType: "array",
			wantDefault:   `["a","b"]`,
		},
		{
			name:          "object default is compact JSON",
			in:            pipeline.InputSpec{Name: "opts", Type: "object", Default: map[string]any{"k": "v"}},
			wantType:      "textarea",
			wantValueType: "object",
			wantDefault:   `{"k":"v"}`,
		},
		{
			name:          "nil default renders empty",
			in:            pipeline.InputSpec{Name: "maybe", Type: "string", Default: nil},
			wantType:      "text",
			wantValueType: "string",
			wantDefault:   "",
		},
		{
			name:          "undeclared type falls back to text",
			in:            pipeline.InputSpec{Name: "mystery"},
			wantType:      "text",
			wantValueType: "",
		},
		{
			// A type from a DSL newer than this build. It must still
			// draw something the server can validate rather than
			// vanishing from the form.
			name:          "unknown future type falls back to text",
			in:            pipeline.InputSpec{Name: "geo", Type: "geopoint"},
			wantType:      "text",
			wantValueType: "geopoint",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := slashFormSchemaForInputs([]pipeline.InputSpec{c.in})
			if len(got) != 1 {
				t.Fatalf("got %d fields, want 1", len(got))
			}
			f := got[0]
			if f.Name != c.in.Name {
				t.Errorf("Name = %q, want %q", f.Name, c.in.Name)
			}
			if f.Type != c.wantType {
				t.Errorf("Type = %q, want %q", f.Type, c.wantType)
			}
			if f.ValueType != c.wantValueType {
				t.Errorf("ValueType = %q, want %q", f.ValueType, c.wantValueType)
			}
			if f.Default != c.wantDefault {
				t.Errorf("Default = %q, want %q", f.Default, c.wantDefault)
			}
			if f.Required != c.wantRequired {
				t.Errorf("Required = %v, want %v", f.Required, c.wantRequired)
			}
			if f.Help != c.wantHelp {
				t.Errorf("Help = %q, want %q", f.Help, c.wantHelp)
			}
		})
	}
}

// A default that survives JSON is the only one that matters: the
// definition arrives as bytes from the database, not as a Go literal, so
// the formatter must be fed what encoding/json actually produces.
func TestFormatInputDefaultAfterJSONRoundTrip(t *testing.T) {
	const def = `{"inputs":[
		{"name":"count","type":"integer","default":42},
		{"name":"ratio","type":"number","default":0.5},
		{"name":"big","type":"number","default":1000000},
		{"name":"flag","type":"boolean","default":true},
		{"name":"tags","type":"array","default":["a","b"]},
		{"name":"opts","type":"object","default":{"k":"v"}},
		{"name":"plain","type":"string","default":"hello"},
		{"name":"absent","type":"string"}
	]}`
	var parsed routineSlashDefinition
	if err := json.Unmarshal([]byte(def), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]string{}
	for _, f := range slashFormSchemaForInputs(parsed.Inputs) {
		got[f.Name] = f.Default
	}
	want := map[string]string{
		"count":  "42",
		"ratio":  "0.5",
		"big":    "1000000",
		"flag":   "true",
		"tags":   `["a","b"]`,
		"opts":   `{"k":"v"}`,
		"plain":  "hello",
		"absent": "",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("default for %q = %q, want %q", name, got[name], w)
		}
	}
}

// An input with no name cannot be keyed by the client, so it is not a
// field. Dropping it beats drawing an unlabelled box that sends nothing.
func TestSlashFormSchemaDropsUnnamedInputs(t *testing.T) {
	got := slashFormSchemaForInputs([]pipeline.InputSpec{
		{Name: "", Type: "string"},
		{Name: "kept", Type: "string"},
	})
	if len(got) != 1 || got[0].Name != "kept" {
		t.Fatalf("got %+v, want just the named input", got)
	}
	if slashFormSchemaForInputs(nil) != nil {
		t.Error("no inputs should produce a nil schema, not an empty one")
	}
	if slashFormSchemaForInputs([]pipeline.InputSpec{{Name: ""}}) != nil {
		t.Error("only-unnamed inputs should produce a nil schema")
	}
}

// seedSlashRoutine inserts a routine with the given definition + status.
func seedSlashRoutine(t *testing.T, db *sql.DB, wsID, slug, definition, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash,
			ephemeral, workspace_visible, invocation_count, authored_via, status)
		VALUES (?, ?, ?, ?, ?, ?, 0, 1, 0, 'user_api', ?)`,
		"pipe-"+slug, wsID, slug, slug, definition, "hash-"+slug, status); err != nil {
		t.Fatalf("seed routine %s: %v", slug, err)
	}
}

// slashDef builds a definition with an optional slash block.
func slashDef(name, slashBlock, inputs string) string {
	var b strings.Builder
	b.WriteString(`{"dsl_version":"1.0","name":"` + name + `"`)
	if slashBlock != "" {
		b.WriteString(`,"slash":` + slashBlock)
	}
	if inputs != "" {
		b.WriteString(`,"inputs":` + inputs)
	}
	b.WriteString(`,"steps":[]}`)
	return b.String()
}

// listSlashCommands drives the handler and returns the decoded catalog.
func listSlashCommands(t *testing.T, h *SlashCommandsHandler, wsID, userID string) []slashCommand {
	t.Helper()
	InvalidateCapabilityCache(wsID, userID)
	r := httptest.NewRequest("GET", "/api/v1/slash-commands", nil)
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got []slashCommand
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func catalogIDs(cmds []slashCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.ID)
	}
	return out
}

func hasCatalogID(cmds []slashCommand, id string) bool {
	for _, c := range cmds {
		if c.ID == id {
			return true
		}
	}
	return false
}

// TestSlashCatalog_RoutineAdmission covers what puts a routine in the
// palette and what keeps it out. Each case is a rule someone could
// plausibly get wrong in the opposite direction.
func TestSlashCatalog_RoutineAdmission(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	const enabled = `{"enabled":true,"label":"Accounting pack","label_cs":"Účetní podklady","icon":"receipt"}`

	// Opted in and active — the one that must appear.
	seedSlashRoutine(t, db, wsID, "msn-etn-podklady",
		slashDef("msn-etn-podklady", enabled, `[{"name":"obdobi","type":"string"}]`), "active")
	// No slash block at all: every routine written before this feature.
	seedSlashRoutine(t, db, wsID, "silent-routine",
		slashDef("silent-routine", "", ""), "active")
	// Block present, opted out.
	seedSlashRoutine(t, db, wsID, "opted-out",
		slashDef("opted-out", `{"enabled":false,"label":"No"}`, ""), "active")
	// Opted in but awaiting approval — the run endpoint would 409 it.
	seedSlashRoutine(t, db, wsID, "not-yet-approved",
		slashDef("not-yet-approved", enabled, ""), "proposed")
	// Opted in but pulled by an admin.
	seedSlashRoutine(t, db, wsID, "pulled",
		slashDef("pulled", enabled, ""), "disabled")
	// Slug collides with a platform slash command. The static catalog
	// owns `/issue` in every workspace.
	seedSlashRoutine(t, db, wsID, "issue",
		slashDef("issue", enabled, ""), "active")
	// Definition JSON that no longer decodes. It costs this routine its
	// row, not the whole catalog.
	seedSlashRoutine(t, db, wsID, "corrupt", `{"dsl_version":`, "active")

	runner := seedMemberWithCapabilities(t, db, wsID, "MEMBER",
		`["chat","routine.run"]`, "slashrt-runner")

	router := &Router{db: db, logger: newTestLogger()}
	h := NewSlashCommandsHandler(router)
	got := listSlashCommands(t, h, wsID, runner)

	if !hasCatalogID(got, "routine.run:msn-etn-podklady") {
		t.Errorf("opted-in active routine missing from the catalog; got %v", catalogIDs(got))
	}
	for _, absent := range []string{
		"routine.run:silent-routine",
		"routine.run:opted-out",
		"routine.run:not-yet-approved",
		"routine.run:pulled",
		"routine.run:issue",
		"routine.run:corrupt",
	} {
		if hasCatalogID(got, absent) {
			t.Errorf("%s must not be in the catalog; got %v", absent, catalogIDs(got))
		}
	}
	// The collision drops the ROUTINE, never the platform command — and
	// the member holds no issue.create, so `/issue` is absent for a
	// reason that has nothing to do with the collision. What must not
	// happen is the routine taking the id.
	for _, c := range got {
		if c.ID == "issue" && c.Capability == CapabilityRoutineRun {
			t.Error("a routine slugged 'issue' shadowed the platform command")
		}
	}
}

// The palette must not offer what the endpoint refuses. A member with no
// routine.run sees no routine entries at all.
func TestSlashCatalog_RoutineEntriesNeedTheCapability(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	seedSlashRoutine(t, db, wsID, "msn-etn-podklady",
		slashDef("msn-etn-podklady", `{"enabled":true,"label":"Accounting pack"}`, ""), "active")

	router := &Router{db: db, logger: newTestLogger()}
	h := NewSlashCommandsHandler(router)

	chatOnly := seedMemberWithCapabilities(t, db, wsID, "MEMBER", `["chat"]`, "slashrt-chatonly")
	if got := listSlashCommands(t, h, wsID, chatOnly); hasCatalogID(got, "routine.run:msn-etn-podklady") {
		t.Errorf("a member without routine.run must not see routine entries; got %v", catalogIDs(got))
	}

	granted := seedMemberWithCapabilities(t, db, wsID, "MEMBER", `["chat","routine.run"]`, "slashrt-granted")
	if got := listSlashCommands(t, h, wsID, granted); !hasCatalogID(got, "routine.run:msn-etn-podklady") {
		t.Errorf("a member with routine.run must see routine entries; got %v", catalogIDs(got))
	}
}

// An ADMIN whose capability column was written by the v109 backfill
// holds routine.create and NOT routine.run — that migration predates the
// capability. They clear the run endpoint's role gate anyway, so the
// palette must not hide the routines they can plainly run. Filtering on
// the capability alone would have blanked the palette for exactly the
// people who administer the workspace, and only on databases old enough
// to have been backfilled.
func TestSlashCatalog_RoleAloneAdmitsRoutineEntries(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	seedSlashRoutine(t, db, wsID, "msn-etn-podklady",
		slashDef("msn-etn-podklady", `{"enabled":true,"label":"Accounting pack"}`, ""), "active")

	backfilledAdmin := seedMemberWithCapabilities(t, db, wsID, "ADMIN",
		`["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`,
		"slashrt-v109admin")

	router := &Router{db: db, logger: newTestLogger()}
	h := NewSlashCommandsHandler(router)
	got := listSlashCommands(t, h, wsID, backfilledAdmin)
	if !hasCatalogID(got, "routine.run:msn-etn-podklady") {
		t.Errorf("a v109-backfilled ADMIN must still see routine entries; got %v", catalogIDs(got))
	}
}

// The catalog entry a routine produces, field by field: the id carries
// the dispatch prefix, the typed command is the bare slug, and the form
// schema is the routine's own inputs with their defaults.
func TestSlashCatalog_RoutineEntryShape(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	seedSlashRoutine(t, db, wsID, "msn-etn-podklady", slashDef("msn-etn-podklady",
		`{"enabled":true,"label":"Monthly accounting pack","label_cs":"Účetní podklady za měsíc","icon":"receipt"}`,
		`[{"name":"obdobi","type":"string"},
		  {"name":"ucetnictvi_root","type":"string","default":"Unify - Účetnictví"},
		  {"name":"vypis_odesilatel","type":"string","default":"info@rb.cz"}]`), "active")

	runner := seedMemberWithCapabilities(t, db, wsID, "MEMBER", `["chat","routine.run"]`, "slashrt-shape")
	router := &Router{db: db, logger: newTestLogger()}
	h := NewSlashCommandsHandler(router)

	var entry slashCommand
	for _, c := range listSlashCommands(t, h, wsID, runner) {
		if c.ID == "routine.run:msn-etn-podklady" {
			entry = c
		}
	}
	if entry.ID == "" {
		t.Fatal("routine entry missing")
	}
	if entry.Label != "Monthly accounting pack" {
		t.Errorf("Label = %q", entry.Label)
	}
	if entry.LabelCS != "Účetní podklady za měsíc" {
		t.Errorf("LabelCS = %q", entry.LabelCS)
	}
	if entry.Icon != "receipt" {
		t.Errorf("Icon = %q", entry.Icon)
	}
	if entry.Capability != CapabilityRoutineRun {
		t.Errorf("Capability = %q, want %q", entry.Capability, CapabilityRoutineRun)
	}
	if len(entry.FormSchema) != 3 {
		t.Fatalf("FormSchema has %d fields, want 3: %+v", len(entry.FormSchema), entry.FormSchema)
	}
	// The three fields of msn-etn-podklady, in declaration order, with
	// the defaults the form opens prefilled with.
	want := []slashFormField{
		{Name: "obdobi", Type: "text", ValueType: "string"},
		{Name: "ucetnictvi_root", Type: "text", ValueType: "string", Default: "Unify - Účetnictví"},
		{Name: "vypis_odesilatel", Type: "text", ValueType: "string", Default: "info@rb.cz"},
	}
	for i, w := range want {
		if entry.FormSchema[i] != w {
			t.Errorf("field %d = %+v, want %+v", i, entry.FormSchema[i], w)
		}
	}
}

// Label precedence: the slash block's own label, then the routine's
// name, then the slug. A routine that opts in without naming itself
// still renders as something readable.
func TestSlashCatalog_RoutineLabelFallback(t *testing.T) {
	cases := []struct {
		name  string
		p     pipeline.Pipeline
		want  string
		block string
	}{
		{
			name:  "slash label wins",
			block: `{"enabled":true,"label":"From the block"}`,
			p:     pipeline.Pipeline{Slug: "s", Name: "From the name"},
			want:  "From the block",
		},
		{
			name:  "routine name is next",
			block: `{"enabled":true}`,
			p:     pipeline.Pipeline{Slug: "s", Name: "From the name"},
			want:  "From the name",
		},
		{
			name:  "slug is the floor",
			block: `{"enabled":true}`,
			p:     pipeline.Pipeline{Slug: "msn-etn-podklady"},
			want:  "msn-etn-podklady",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.p
			p.DefinitionJSON = `{"slash":` + c.block + `}`
			got, ok := slashCommandForRoutine(&p)
			if !ok {
				t.Fatal("routine was not admitted")
			}
			if got.Label != c.want {
				t.Errorf("Label = %q, want %q", got.Label, c.want)
			}
		})
	}
}

// A routine with no slug has no command to be typed, so it cannot be a
// palette entry regardless of what its slash block says.
func TestSlashCommandForRoutine_RejectsSlugless(t *testing.T) {
	if _, ok := slashCommandForRoutine(nil); ok {
		t.Error("nil routine was admitted")
	}
	if _, ok := slashCommandForRoutine(&pipeline.Pipeline{
		DefinitionJSON: `{"slash":{"enabled":true}}`,
	}); ok {
		t.Error("a routine with no slug was admitted")
	}
}

// Every static catalog id is a name a routine must not be able to take.
// The set is derived from the catalog rather than restated, so a fifth
// platform command is protected the day it is added.
func TestStaticSlashCommandIDsCoverTheCatalog(t *testing.T) {
	if len(staticSlashCommandIDs) != len(slashCommandCatalog) {
		t.Fatalf("static id set has %d entries, catalog has %d", len(staticSlashCommandIDs), len(slashCommandCatalog))
	}
	for _, sc := range slashCommandCatalog {
		if !slashRoutineCollidesWithStatic(sc.ID) {
			t.Errorf("a routine slugged %q would shadow the platform command of the same id", sc.ID)
		}
	}
	if slashRoutineCollidesWithStatic("msn-etn-podklady") {
		t.Error("an ordinary routine slug was treated as a collision")
	}
}
