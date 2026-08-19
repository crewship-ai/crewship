package cli

// The repl's half of running a routine from a slash command:
//
//	crewship › /msn-etn-podklady obdobi=2026-07
//
// What has to be true is that the command is registered under the bare
// slug, that it addresses the run endpoint, and that the body carries
// `inputs` with each value restored to the JSON type the routine
// declared — a `code` step sees inputs with their original types, so an
// integer arriving as the string "42" fails the run at that step.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordingSlashClient captures what went out. The shared
// fakeSlashClient in slash_server_cover_test.go replays canned
// responses but keeps no record of the request, and the request is the
// whole subject here — which path was addressed and what body reached
// it.
type recordingSlashClient struct {
	wsID         string
	catalog      string
	lastPostPath string
	lastPostBody any
}

func newRecordingSlashClient(wsID, catalog string) *recordingSlashClient {
	return &recordingSlashClient{wsID: wsID, catalog: catalog}
}

func (c *recordingSlashClient) Get(string) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewBufferString(c.catalog)),
	}, nil
}

func (c *recordingSlashClient) Post(path string, body any) (*http.Response, error) {
	c.lastPostPath = path
	c.lastPostBody = body
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Body:       io.NopCloser(bytes.NewBufferString(`{"run_id":"run_1","status":"COMPLETED"}`)),
	}, nil
}

func (c *recordingSlashClient) GetWorkspaceID() string { return c.wsID }

// msnCommand is the catalog entry for the routine this feature was
// built against: three string inputs, two with defaults.
func msnCommand() ServerSlashCommand {
	return ServerSlashCommand{
		ID:         "routine.run:msn-etn-podklady",
		Label:      "Monthly accounting pack",
		Capability: "routine.run",
		FormSchema: []ServerSlashField{
			{Name: "obdobi", Type: "text", ValueType: "string"},
			{Name: "ucetnictvi_root", Type: "text", ValueType: "string", Default: "Unify - Účetnictví"},
			{Name: "vypis_odesilatel", Type: "text", ValueType: "string", Default: "info@rb.cz"},
		},
	}
}

func TestSlashCommandName(t *testing.T) {
	cases := map[string]string{
		// The platform catalog types as it is named.
		"issue":                        "issue",
		"credential":                   "credential",
		"routine":                      "routine",
		"routine.run:msn-etn-podklady": "msn-etn-podklady",
		// A prefix with nothing after it is not a routine entry, so it
		// is left alone rather than becoming the empty command.
		"routine.run:": "routine.run:",
	}
	for id, want := range cases {
		if got := slashCommandName(id); got != want {
			t.Errorf("slashCommandName(%q) = %q, want %q", id, got, want)
		}
	}
}

// `/routine` is a platform command that SCHEDULES a routine. A catalog
// entry that RUNS one is `routine.run:<slug>`. The prefix test must not
// mistake the first for the second — they differ by a character that a
// naive strings.Contains would have matched.
func TestRoutineSlugFromSlashID(t *testing.T) {
	if _, ok := routineSlugFromSlashID("routine"); ok {
		t.Error("the platform /routine command was read as a run entry")
	}
	slug, ok := routineSlugFromSlashID("routine.run:msn-etn-podklady")
	if !ok || slug != "msn-etn-podklady" {
		t.Errorf("got (%q, %v), want (msn-etn-podklady, true)", slug, ok)
	}
}

func TestSlashCommandEndpoint_Routine(t *testing.T) {
	got, err := slashCommandEndpoint("routine.run:msn-etn-podklady", "ws-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "/api/v1/workspaces/ws-1/pipelines/msn-etn-podklady/run"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A slug is a path segment. It is server-supplied and slug-shaped today,
// but the escaping is what keeps it a segment rather than a way to
// address a different route.
func TestSlashCommandEndpoint_RoutineEscapesSlug(t *testing.T) {
	got, err := slashCommandEndpoint("routine.run:../../admin/backups", "ws-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(got, "/admin/backups") {
		t.Errorf("slug escaped its path segment: %q", got)
	}
}

// The headline case: /msn-etn-podklady obdobi=2026-07 builds the right
// body. The two defaulted fields ride along; the one the user set wins.
func TestSlashCommandPayload_RoutineRun(t *testing.T) {
	got, err := slashCommandPayload(msnCommand(), map[string]string{
		"obdobi":           "2026-07",
		"ucetnictvi_root":  "Unify - Účetnictví",
		"vypis_odesilatel": "info@rb.cz",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	inputs, ok := got["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("body has no inputs map: %#v", got)
	}
	if len(got) != 1 {
		t.Errorf("body carries keys beside inputs: %#v", got)
	}
	want := map[string]any{
		"obdobi":           "2026-07",
		"ucetnictvi_root":  "Unify - Účetnictví",
		"vypis_odesilatel": "info@rb.cz",
	}
	for k, v := range want {
		if inputs[k] != v {
			t.Errorf("inputs[%q] = %#v, want %#v", k, inputs[k], v)
		}
	}
}

// An empty field is omitted rather than sent as "". The routine's own
// default then applies server-side, which is the only place that knows
// what it is: for msn-etn-podklady an empty `obdobi` means "the previous
// month", and shipping "" would override that with a blank period.
func TestSlashCommandPayload_RoutineOmitsEmptyValues(t *testing.T) {
	got, err := slashCommandPayload(msnCommand(), map[string]string{
		"obdobi":           "",
		"ucetnictvi_root":  "Unify - Účetnictví",
		"vypis_odesilatel": "",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	inputs := got["inputs"].(map[string]any)
	if _, present := inputs["obdobi"]; present {
		t.Errorf("an empty value was sent instead of omitted: %#v", inputs)
	}
	if _, present := inputs["vypis_odesilatel"]; present {
		t.Errorf("an empty value was sent instead of omitted: %#v", inputs)
	}
	if inputs["ucetnictvi_root"] != "Unify - Účetnictví" {
		t.Errorf("a set value was dropped: %#v", inputs)
	}
}

// The type round trip. A form holds strings; the run endpoint validates
// JSON types. Every declared type has to arrive as itself.
func TestSlashCommandPayload_RoutineTypeRoundTrip(t *testing.T) {
	cmd := ServerSlashCommand{
		ID: "routine.run:typed",
		FormSchema: []ServerSlashField{
			{Name: "text", ValueType: "string"},
			{Name: "count", ValueType: "integer"},
			{Name: "rate", ValueType: "number"},
			{Name: "flag", ValueType: "boolean"},
			{Name: "tags", ValueType: "array"},
			{Name: "opts", ValueType: "object"},
			{Name: "untyped", ValueType: ""},
		},
	}
	got, err := slashCommandPayload(cmd, map[string]string{
		"text":    "hello",
		"count":   "42",
		"rate":    "0.5",
		"flag":    "true",
		"tags":    `["a","b"]`,
		"opts":    `{"k":"v"}`,
		"untyped": "7",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	inputs := got["inputs"].(map[string]any)

	if inputs["text"] != "hello" {
		t.Errorf("text = %#v", inputs["text"])
	}
	// An integer must not arrive as the string "42" — the whole point.
	if n, ok := inputs["count"].(int64); !ok || n != 42 {
		t.Errorf("count = %#v (%T), want int64(42)", inputs["count"], inputs["count"])
	}
	if n, ok := inputs["rate"].(float64); !ok || n != 0.5 {
		t.Errorf("rate = %#v (%T), want float64(0.5)", inputs["rate"], inputs["rate"])
	}
	if b, ok := inputs["flag"].(bool); !ok || !b {
		t.Errorf("flag = %#v (%T), want true", inputs["flag"], inputs["flag"])
	}
	if arr, ok := inputs["tags"].([]any); !ok || len(arr) != 2 || arr[0] != "a" {
		t.Errorf("tags = %#v (%T), want []any{a,b}", inputs["tags"], inputs["tags"])
	}
	if obj, ok := inputs["opts"].(map[string]any); !ok || obj["k"] != "v" {
		t.Errorf("opts = %#v (%T), want map{k:v}", inputs["opts"], inputs["opts"])
	}
	// No declared type means string, which is every field the static
	// catalog ships and every field a server older than this build sends.
	if inputs["untyped"] != "7" {
		t.Errorf("untyped = %#v (%T), want the string \"7\"", inputs["untyped"], inputs["untyped"])
	}

	// And it has to survive the wire, not just the map: int64 marshals
	// as 42, never as "42".
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"count":42`, `"rate":0.5`, `"flag":true`, `"tags":["a","b"]`, `"untyped":"7"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("marshalled body is missing %s: %s", want, raw)
		}
	}
}

// A value that can't be restored to its declared type is refused at the
// prompt, with the field named. Better than posting a string and reading
// back whatever the server's validator says about it.
func TestSlashCommandPayload_RoutineRejectsUncoercibleValues(t *testing.T) {
	cases := []struct {
		name      string
		valueType string
		raw       string
		wantErr   string
	}{
		{"integer", "integer", "banana", "not a whole number"},
		{"integer given a float", "integer", "4.5", "not a whole number"},
		{"number", "number", "lots", "not a number"},
		{"boolean", "boolean", "maybe", "not true or false"},
		{"array with broken JSON", "array", `["a",`, "not valid JSON"},
		{"object with broken JSON", "object", `{`, "not valid JSON"},
		// Valid JSON of the wrong shape. json.Unmarshal accepts it, so
		// without the shape check it would sail through to the server.
		{"array given an object", "array", `{"k":"v"}`, "not an array"},
		{"object given a number", "object", `42`, "not an object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := ServerSlashCommand{
				ID:         "routine.run:typed",
				FormSchema: []ServerSlashField{{Name: "field", ValueType: c.valueType}},
			}
			_, err := slashCommandPayload(cmd, map[string]string{"field": c.raw})
			if err == nil {
				t.Fatalf("want an error for %s=%q", c.valueType, c.raw)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), c.wantErr)
			}
			// The message has to name the field — a form with six inputs
			// and an error that says only "not a number" is not usable.
			if !strings.Contains(err.Error(), "field") {
				t.Errorf("error = %q, want it to name the field", err.Error())
			}
		})
	}
}

// A boolean always sends, including when it is false.
//
// The general rule is that an empty value is omitted so the routine's
// own default applies. A checkbox breaks it: its unticked value IS the
// empty string, and it has no third state to mean "leave this alone", so
// omitting it would let a `default: true` overrule somebody who had just
// unticked the box. The browser and the repl must agree on this — the
// same command has to mean the same thing on both — so the rule is
// pinned on each side (lib/__tests__/routine-inputs.test.ts is the
// other).
func TestSlashCommandPayload_RoutineAlwaysSendsBooleans(t *testing.T) {
	cmd := ServerSlashCommand{
		ID: "routine.run:typed",
		FormSchema: []ServerSlashField{
			{Name: "dry_run", ValueType: "boolean"},
			{Name: "note", ValueType: "string"},
		},
	}
	got, err := slashCommandPayload(cmd, map[string]string{"dry_run": "", "note": ""})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	inputs := got["inputs"].(map[string]any)
	v, present := inputs["dry_run"]
	if !present {
		t.Fatalf("an unticked boolean was omitted, so a default:true would win: %#v", inputs)
	}
	if v != false {
		t.Errorf("dry_run = %#v (%T), want false", v, v)
	}
	// A string keeps the ordinary rule.
	if _, present := inputs["note"]; present {
		t.Errorf("an empty string was sent instead of omitted: %#v", inputs)
	}
}

// A value for something the schema doesn't declare goes through as a
// string. The user typed it deliberately, and the server rejecting an
// undeclared input says so better than a silent client-side drop.
func TestSlashCommandPayload_RoutinePassesUndeclaredValues(t *testing.T) {
	got, err := slashCommandPayload(msnCommand(), map[string]string{"typo_field": "x"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got["inputs"].(map[string]any)["typo_field"] != "x" {
		t.Errorf("undeclared value was dropped: %#v", got)
	}
}

// End to end through the REPL: the catalog registers /msn-etn-podklady,
// typing it POSTs the run endpoint, and the body is what the routine
// expects.
func TestLoadServerSlashCommands_RegistersRoutineUnderItsSlug(t *testing.T) {
	catalog := `[{
		"id":"routine.run:msn-etn-podklady",
		"label":"Monthly accounting pack",
		"capability":"routine.run",
		"form_schema":[
			{"name":"obdobi","type":"text","value_type":"string"},
			{"name":"ucetnictvi_root","type":"text","value_type":"string","default":"Unify - Účetnictví"}
		]
	}]`
	client := newRecordingSlashClient("ws-1", catalog)
	repl := newTestREPL()
	if n := LoadServerSlashCommands(context.Background(), repl, client); n != 1 {
		t.Fatalf("loaded %d commands, want 1", n)
	}
	handler, ok := repl.Slash["msn-etn-podklady"]
	if !ok {
		t.Fatalf("command not registered under its slug; registered: %v", replCommandNames(repl))
	}
	if _, wrong := repl.Slash["routine.run:msn-etn-podklady"]; wrong {
		t.Error("command was also registered under its raw catalog id")
	}

	if _, err := handler(context.Background(), []string{"obdobi=2026-07"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if client.lastPostPath != "/api/v1/workspaces/ws-1/pipelines/msn-etn-podklady/run" {
		t.Errorf("posted to %q", client.lastPostPath)
	}
	body, err := json.Marshal(client.lastPostBody)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if !strings.Contains(string(body), `"obdobi":"2026-07"`) {
		t.Errorf("body missing the typed input: %s", body)
	}
	// The default the user didn't type is applied, matching what the
	// dashboard's prefilled form would have sent.
	if !strings.Contains(string(body), `"ucetnictvi_root":"Unify - Účetnictví"`) {
		t.Errorf("body missing the defaulted input: %s", body)
	}
}

// A required field with no default is caught at the prompt, and the hint
// names the command the way the user types it — not by its catalog id.
func TestSlashRoutineHandler_MissingRequiredNamesTheTypedCommand(t *testing.T) {
	cmd := ServerSlashCommand{
		ID:         "routine.run:msn-etn-podklady",
		FormSchema: []ServerSlashField{{Name: "obdobi", ValueType: "string", Required: true}},
	}
	client := newRecordingSlashClient("ws-1", `[]`)
	handler := buildSlashHandler(cmd, client, io.Discard)
	_, err := handler(context.Background(), nil)
	if err == nil {
		t.Fatal("want an error for a missing required field")
	}
	if !strings.Contains(err.Error(), "/msn-etn-podklady") {
		t.Errorf("error = %q, want it to name /msn-etn-podklady", err.Error())
	}
	if strings.Contains(err.Error(), "routine.run:") {
		t.Errorf("error leaked the dispatch prefix: %q", err.Error())
	}
	if client.lastPostPath != "" {
		t.Error("a request went out despite the missing required field")
	}
}

func replCommandNames(r *REPL) []string {
	out := make([]string, 0, len(r.Slash))
	for k := range r.Slash {
		out = append(out, k)
	}
	return out
}

// A routine must never take a built-in's name.
//
// REPL.Register is a silent map overwrite, and routine slugs match
// `^[a-z0-9][a-z0-9_-]{0,63}$` — which `exit`, `help`, `clear` and
// `agent` all satisfy. Without a guard, a workspace could publish a
// routine slugged `exit` and every user of `crewship shell` would run
// somebody's accounting pack while trying to leave.
func TestLoadServerSlashCommands_BuiltinsWinCollisions(t *testing.T) {
	catalog := `[
		{"id":"routine.run:exit","label":"Sneaky","capability":"routine.run"},
		{"id":"routine.run:msn-etn-podklady","label":"Accounting pack","capability":"routine.run"}
	]`
	client := newRecordingSlashClient("ws-1", catalog)
	repl := newTestREPL()
	errBuf := &bytes.Buffer{}
	repl.Err = errBuf

	builtinRan := false
	repl.Register("exit", func(context.Context, []string) (bool, error) {
		builtinRan = true
		return false, nil
	})

	// Only the non-colliding entry counts as loaded — the banner must
	// not promise a command the shell does not have.
	if n := LoadServerSlashCommands(context.Background(), repl, client); n != 1 {
		t.Fatalf("loaded %d commands, want 1 (the collision is skipped)", n)
	}
	if _, err := repl.Slash["exit"](context.Background(), nil); err != nil {
		t.Fatalf("/exit: %v", err)
	}
	if !builtinRan {
		t.Error("a routine took /exit from the built-in")
	}
	if client.lastPostPath != "" {
		t.Errorf("the shadowing routine was invoked: %q", client.lastPostPath)
	}
	// Skipping silently would leave an author wondering why their
	// routine never appears.
	if !strings.Contains(errBuf.String(), "exit shadows a built-in") {
		t.Errorf("no warning was emitted: %q", errBuf.String())
	}
	// The routine that did not collide is unaffected.
	if _, ok := repl.Slash["msn-etn-podklady"]; !ok {
		t.Error("the non-colliding routine was not registered")
	}
}

// The repl and the browser must accept the same words for a boolean. A
// user is told one command; `/pack dry=yes` cannot work in chat and
// error at this prompt. The mirror of this table is in
// lib/__tests__/routine-inputs.test.ts.
func TestCoerceRoutineInput_BooleanVocabularyMatchesTheBrowser(t *testing.T) {
	truthy := []string{"true", "TRUE", "True", "1", "yes", "YES", "on", " true "}
	falsy := []string{"false", "0", "no", "off", "", "  "}
	for _, raw := range truthy {
		got, err := coerceRoutineInput("boolean", raw)
		if err != nil || got != true {
			t.Errorf("coerceRoutineInput(boolean, %q) = (%v, %v), want (true, nil)", raw, got, err)
		}
	}
	for _, raw := range falsy {
		got, err := coerceRoutineInput("boolean", raw)
		if err != nil || got != false {
			t.Errorf("coerceRoutineInput(boolean, %q) = (%v, %v), want (false, nil)", raw, got, err)
		}
	}
	// strconv.ParseBool accepts "t"/"T"; the browser does not, so neither
	// does this. Rejecting it in both places beats accepting it in one.
	for _, raw := range []string{"t", "f", "maybe"} {
		if _, err := coerceRoutineInput("boolean", raw); err == nil {
			t.Errorf("coerceRoutineInput(boolean, %q) was accepted; the browser rejects it", raw)
		}
	}
}

// ParseFloat accepts NaN and Inf. Neither survives json.Marshal, so
// letting them through trades a clear message at the prompt for an
// opaque marshalling failure at POST time — and the browser rejects
// them, so accepting them here would be a second divergence.
func TestCoerceRoutineInput_RejectsNonFiniteNumbers(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-Inf", "+Inf", "infinity"} {
		if _, err := coerceRoutineInput("number", raw); err == nil {
			t.Errorf("coerceRoutineInput(number, %q) was accepted", raw)
		}
	}
	// Ordinary numbers, including padded ones, still parse.
	for _, raw := range []string{"0.5", " 42 ", "-3"} {
		if _, err := coerceRoutineInput("number", raw); err != nil {
			t.Errorf("coerceRoutineInput(number, %q) = %v, want it accepted", raw, err)
		}
	}
	if _, err := coerceRoutineInput("integer", " 42 "); err != nil {
		t.Errorf("a padded integer was rejected: %v", err)
	}
}
