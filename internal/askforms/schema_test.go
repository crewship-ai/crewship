package askforms

// schemas/ask-form.v1.json is an authoring aid — an editor completing and
// checking a forms.json before `crewship agent update --ask-forms @forms.json`
// sends it. It is deliberately NOT a second validator: this package is the
// single write path, and two validators that can disagree about the same
// document is a worse failure than no schema at all (the routine schema is
// kept honest the same way, by tests rather than by runtime validation — see
// internal/pipeline/schema_test.go).
//
// What makes "not a second validator" safe is this file. Every number and
// pattern the schema states is asserted against the constant that actually
// enforces it, so a cap moved in Go and not in the schema is a red run rather
// than an editor quietly approving a form the server will refuse.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"unicode/utf8"
)

func loadAskFormSchema(t *testing.T) map[string]any {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "ask-form.v1.json"))
	if err != nil {
		t.Fatalf("read ask-form.v1.json: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("ask-form.v1.json is not valid JSON: %v", err)
	}
	return schema
}

func TestSchemaMatchesConstants(t *testing.T) {
	schema := loadAskFormSchema(t)

	form, _ := schema["items"].(map[string]any)
	if form == nil {
		t.Fatal("schema has no items — it should describe an array of forms")
	}
	formProps, _ := form["properties"].(map[string]any)
	if formProps == nil {
		t.Fatal("schema's form has no properties")
	}

	num := func(node any, key string) int {
		t.Helper()
		m, _ := node.(map[string]any)
		if m == nil {
			t.Fatalf("expected an object while looking for %q", key)
		}
		v, ok := m[key].(float64)
		if !ok {
			t.Fatalf("%q is missing from the schema", key)
		}
		return int(v)
	}

	if got := num(schema, "maxItems"); got != MaxForms {
		t.Errorf("schema caps the list at %d forms, MaxForms is %d", got, MaxForms)
	}
	if got := num(formProps["template"], "maxLength"); got != MaxTemplateRunes {
		t.Errorf("schema caps a template at %d, MaxTemplateRunes is %d", got, MaxTemplateRunes)
	}
	if got := num(formProps["label"], "maxLength"); got != MaxLabelRunes {
		t.Errorf("schema caps a form label at %d, MaxLabelRunes is %d", got, MaxLabelRunes)
	}
	if got := num(formProps["id"], "maxLength"); got != MaxIDRunes {
		t.Errorf("schema caps an id at %d, MaxIDRunes is %d", got, MaxIDRunes)
	}

	fields, _ := formProps["fields"].(map[string]any)
	if got := num(fields, "maxItems"); got != MaxFieldsPerForm {
		t.Errorf("schema caps fields at %d, MaxFieldsPerForm is %d", got, MaxFieldsPerForm)
	}

	fieldProps, _ := fields["items"].(map[string]any)["properties"].(map[string]any)
	if fieldProps == nil {
		t.Fatal("schema's field has no properties")
	}
	if got := num(fieldProps["label"], "maxLength"); got != MaxLabelRunes {
		t.Errorf("schema caps a field label at %d, MaxLabelRunes is %d", got, MaxLabelRunes)
	}

	str := func(node any, key string) string {
		t.Helper()
		m, _ := node.(map[string]any)
		s, _ := m[key].(string)
		return s
	}
	if got := str(formProps["id"], "pattern"); got != formIDRE.String() {
		t.Errorf("schema's id pattern is %q, formIDRE is %q", got, formIDRE.String())
	}
	if got := str(fieldProps["name"], "pattern"); got != fieldNameRE.String() {
		t.Errorf("schema's field name pattern is %q, fieldNameRE is %q", got, fieldNameRE.String())
	}

	// The attachment policies are an enum in the schema and three constants
	// here; a fourth added on one side only would be a form that saves and
	// then fails to open, or vice versa.
	enum, _ := formProps["attachment"].(map[string]any)["enum"].([]any)
	if len(enum) != len(attachmentPolicies) {
		t.Fatalf("schema lists %d attachment policies, the package has %d", len(enum), len(attachmentPolicies))
	}
	for i, want := range attachmentPolicies {
		if got, _ := enum[i].(string); got != want {
			t.Errorf("attachment policy %d: schema says %q, package says %q", i, got, want)
		}
	}

	// The field `type` must NOT be an enum. An unrecognised type falls back
	// to a text input, and that fallback is what lets a new field type ship
	// without a coordinated frontend release; a schema that enumerates the
	// types would have an editor rejecting the new one the day it lands.
	if _, isEnum := fieldProps["type"].(map[string]any)["enum"]; isEnum {
		t.Error("the schema enumerates field types — it must not, or the " +
			"unknown-type-to-text fallback stops being usable")
	}

	if got := num(fieldProps["type"], "maxLength"); got != MaxTypeRunes {
		t.Errorf("schema caps a type at %d, MaxTypeRunes is %d", got, MaxTypeRunes)
	}
	if got := str(fieldProps["type"], "pattern"); got != fieldTypeRE.String() {
		t.Errorf("schema's type pattern is %q, fieldTypeRE is %q", got, fieldTypeRE.String())
	}
	if got := num(fieldProps["pattern"], "maxLength"); got != MaxPatternRunes {
		t.Errorf("schema caps a field pattern at %d, MaxPatternRunes is %d", got, MaxPatternRunes)
	}
}

// The schema's deny pattern for `type` is the one rule in this file that
// cannot be pinned to a constant: it is an ECMA-262 regular expression
// standing in for a substring-and-token test written in Go. So it is pinned to
// the BEHAVIOUR instead — against the same fixture the frontend reads, every
// case of which must be refused by the editor exactly when it is refused by
// the server.
//
// An editor that accepts what the API refuses is only annoying. An editor that
// green-lights `"type": "api_key"` is a person confidently writing a form that
// asks a user for a credential.
func TestSchemaTypeDenyPatternAgreesWithTheClassifier(t *testing.T) {
	schema := loadAskFormSchema(t)

	fieldProps := schema["items"].(map[string]any)["properties"].(map[string]any)["fields"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	typeSchema, _ := fieldProps["type"].(map[string]any)
	notNode, _ := typeSchema["not"].(map[string]any)
	denySrc, _ := notNode["pattern"].(string)
	if denySrc == "" {
		t.Fatal("the schema states no `not` pattern for a field type — an editor would " +
			"complete `\"type\": \"password\"` for a definition the API refuses")
	}
	deny, err := regexp.Compile(denySrc)
	if err != nil {
		t.Fatalf("the schema's deny pattern does not compile: %v", err)
	}
	shape := regexp.MustCompile(typeSchema["pattern"].(string))
	maxLen := int(typeSchema["maxLength"].(float64))

	for _, tc := range loadFieldTypeFixture(t).Types {
		schemaRefuses := tc.Type == "" ||
			utf8.RuneCountInString(tc.Type) > maxLen ||
			!shape.MatchString(tc.Type) ||
			deny.MatchString(tc.Type)
		verdict, _ := ClassifyFieldType(tc.Type)
		if want := verdict == TypeUnsafe; schemaRefuses != want {
			t.Errorf("type %q: the schema %s it, the server %s it",
				tc.Type, accepts(!schemaRefuses), accepts(verdict != TypeUnsafe))
		}
	}
}

func accepts(ok bool) string {
	if ok {
		return "accepts"
	}
	return "refuses"
}

// The schema has to accept the definition the API accepts. This is the cheap
// half of that: the example every doc page shows must parse here.
func TestSchemaExampleParses(t *testing.T) {
	loadAskFormSchema(t) // fails the test if the schema itself is broken

	if _, err := Parse(`[{
		"id": "receipt",
		"label": "Add a receipt",
		"attachment": "required",
		"template": "Please file this receipt.\n\nSupplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}\nPeriod: {{month}}\nCategory: {{category}}\nDocument: {{document}}",
		"fields": [
			{"name":"supplier","label":"Supplier","type":"text","required":true,"placeholder":"Vodafone"},
			{"name":"amount","label":"Amount","type":"money","required":true,"currency":["CZK","EUR","USD"]},
			{"name":"month","label":"Period","type":"month","default":"2026-08"},
			{"name":"category","label":"Category","type":"select","options":["Telco","Hosting"]},
			{"name":"document","label":"Document","type":"file"}
		]
	}]`); err != nil {
		t.Fatalf("the documented example was refused: %v", err)
	}
}
