package askforms

// The field-type verdict and the answer constraints, read from the fixture
// that lib/__tests__/ask-validate.test.ts also reads.
//
// Why a fixture rather than a table in each language: the two sides are not
// belt-and-braces here, they are one rule enforced at two different moments.
// The Go half decides what may be SAVED; the TypeScript half decides what a
// user may SUBMIT. If they disagree, the disagreement is exactly the defect
// P0.7 describes — a definition the server accepted and the sheet then
// mishandled. testdata/ask-field-types.json is the only place the rule is
// stated, and both suites fail when it moves.
//
// It is deliberately NOT testdata/ask-templates.json. That fixture pins the
// two RENDERERS to each other and nothing in here is a rendering rule; adding
// to it would mean every constraint change touched the file whose whole job is
// to keep Render and renderAskTemplate byte-identical.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type typeCase struct {
	Type    string `json:"type"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type answerError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type answerCase struct {
	Name   string        `json:"name"`
	Field  Field         `json:"field"`
	Value  any           `json:"value"`
	Errors []answerError `json:"errors"`
}

type definitionCase struct {
	Name  string `json:"name"`
	Field Field  `json:"field"`
	Want  string `json:"want"`
}

type fieldTypeFixture struct {
	TypeShape           string           `json:"type_shape"`
	MaxTypeRunes        int              `json:"max_type_runes"`
	Types               []typeCase       `json:"types"`
	Answers             []answerCase     `json:"answers"`
	DefinitionRejection []definitionCase `json:"definition_rejections"`
}

func loadFieldTypeFixture(t *testing.T) fieldTypeFixture {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "ask-field-types.json"))
	if err != nil {
		t.Fatalf("read ask-field-types.json: %v", err)
	}
	var fx fieldTypeFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("ask-field-types.json is not valid JSON: %v", err)
	}
	if len(fx.Types) == 0 || len(fx.Answers) == 0 {
		t.Fatal("the fixture is empty — both suites would pass while proving nothing")
	}
	return fx
}

func TestFieldTypeVerdictMatchesSharedFixture(t *testing.T) {
	fx := loadFieldTypeFixture(t)

	if fx.TypeShape != fieldTypeRE.String() {
		t.Errorf("fixture states the type shape %q, fieldTypeRE is %q", fx.TypeShape, fieldTypeRE.String())
	}
	if fx.MaxTypeRunes != MaxTypeRunes {
		t.Errorf("fixture caps a type at %d runes, MaxTypeRunes is %d", fx.MaxTypeRunes, MaxTypeRunes)
	}

	for _, tc := range fx.Types {
		t.Run(tc.Type, func(t *testing.T) {
			verdict, reason := ClassifyFieldType(tc.Type)
			if string(verdict) != tc.Verdict {
				t.Errorf("type %q: verdict %q, fixture says %q", tc.Type, verdict, tc.Verdict)
			}
			if reason != tc.Reason {
				t.Errorf("type %q: reason %q, fixture says %q", tc.Type, reason, tc.Reason)
			}
		})
	}
}

// Every known type must be one the two renderers actually implement. A type
// listed as `known` that Render treats as plain text would be a control the
// user fills in and a message that ignores how they filled it.
func TestKnownTypesAreTheRenderedOnes(t *testing.T) {
	fx := loadFieldTypeFixture(t)
	for _, tc := range fx.Types {
		if tc.Verdict != "known" {
			continue
		}
		if !KnownFieldTypes[tc.Type] {
			t.Errorf("the fixture calls %q known, KnownFieldTypes does not list it", tc.Type)
		}
	}
	for name := range KnownFieldTypes {
		found := false
		for _, tc := range fx.Types {
			if tc.Type == name && tc.Verdict == "known" {
				found = true
			}
		}
		if !found {
			t.Errorf("KnownFieldTypes lists %q, the fixture does not — one side grew a type alone", name)
		}
	}
}

func TestValidateAnswersMatchesSharedFixture(t *testing.T) {
	fx := loadFieldTypeFixture(t)

	for _, tc := range fx.Answers {
		t.Run(tc.Name, func(t *testing.T) {
			form := Form{
				ID:       "fixture",
				Label:    "Fixture",
				Template: "{{" + tc.Field.Name + "}}",
				Fields:   []Field{tc.Field},
			}
			got := ValidateAnswers(form, Values{tc.Field.Name: tc.Value})

			if len(got) != len(tc.Errors) {
				t.Fatalf("got %d errors %v, fixture wants %d %v", len(got), got, len(tc.Errors), tc.Errors)
			}
			for i, want := range tc.Errors {
				if got[i].Code != want.Code {
					t.Errorf("error %d: code %q, fixture says %q", i, got[i].Code, want.Code)
				}
				if got[i].Message != want.Message {
					t.Errorf("error %d:\n got %q\nwant %q", i, got[i].Message, want.Message)
				}
				// A message that does not name the field is a message the user
				// cannot act on — four fields on screen and "must be at least
				// 3 characters" names none of them.
				if !strings.Contains(got[i].Message, tc.Field.Label) {
					t.Errorf("error %d does not name the field %q: %q", i, tc.Field.Label, got[i].Message)
				}
				if got[i].Field != tc.Field.Name {
					t.Errorf("error %d is attributed to field %q, want %q", i, got[i].Field, tc.Field.Name)
				}
			}
		})
	}
}

// The definition validator is where the guarantee lives: if the server may not
// ship a type the client would mishandle, saving it is what has to fail.
func TestDefinitionRejectionsMatchSharedFixture(t *testing.T) {
	fx := loadFieldTypeFixture(t)

	for _, tc := range fx.DefinitionRejection {
		t.Run(tc.Name, func(t *testing.T) {
			form := Form{
				ID:       "fixture",
				Label:    "Fixture",
				Template: "{{" + tc.Field.Name + "}}",
				Fields:   []Field{tc.Field},
			}
			err := Validate([]Form{form})
			if err == nil {
				t.Fatalf("the definition was accepted; it must be refused (%s)", tc.Want)
			}
			if !strings.Contains(err.Error(), tc.Want) {
				t.Errorf("refusal does not explain itself:\n got %q\nwant it to contain %q", err.Error(), tc.Want)
			}
		})
	}
}

// A secret-typed value must not reach the message, and the server half of that
// is SanitizeValues: whatever a CLI or an older row hands over, the value of a
// field that fails closed is not something Render can put on the wire.
func TestSanitizeValuesDropsUnsafeFields(t *testing.T) {
	form := Form{
		ID:       "legacy",
		Label:    "Legacy",
		Template: "Supplier: {{supplier}}\nKey: {{api}}",
		Fields: []Field{
			{Name: "supplier", Label: "Supplier", Type: "text"},
			{Name: "api", Label: "API key", Type: "api_key"},
		},
	}
	values := Values{"supplier": "Vodafone", "api": "sk-live-DO-NOT-SEND"}

	clean := SanitizeValues(form, values)
	if _, present := clean["api"]; present {
		t.Error("the secret-typed answer survived sanitisation")
	}
	if clean["supplier"] != "Vodafone" {
		t.Errorf("sanitisation dropped an ordinary answer: %v", clean["supplier"])
	}
	// The input map is the caller's; sanitising must not reach back into it.
	if values["api"] != "sk-live-DO-NOT-SEND" {
		t.Error("SanitizeValues mutated its argument")
	}

	msg := Render(form, clean, "chat_1")
	if strings.Contains(msg, "sk-live") {
		t.Fatalf("the rendered message carries the secret: %q", msg)
	}
	if !strings.Contains(msg, "Vodafone") {
		t.Fatalf("the rendered message lost the ordinary answer: %q", msg)
	}
}
