package askforms

// The Go half of the shared golden fixture.
//
// testdata/ask-templates.json at the REPO ROOT (not this package's testdata/)
// is read here and by lib/__tests__/ask-template.test.ts. Both suites must
// agree on every case in it, because both renderers produce the message the
// user actually sends: the composer renders the preview and the outgoing
// text, the server and `crewship agent ask-preview` render the same template
// for anyone without a browser. A rule implemented in one and not the other
// is exactly the defect the fixture exists to catch.
//
// The repo-root walk copies internal/pipeline/schema_test.go:125-136, which
// reads schemas/routine.v1.json the same way. A bare
// filepath.Join("testdata", ...) would resolve to internal/askforms/testdata
// and quietly give the two languages two different files — the failure this
// whole arrangement is meant to make impossible.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fixtureFile struct {
	Limits struct {
		MaxForms         int `json:"maxForms"`
		MaxFieldsPerForm int `json:"maxFieldsPerForm"`
		MaxLabelRunes    int `json:"maxLabelRunes"`
		MaxTemplateRunes int `json:"maxTemplateRunes"`
		MaxValueRunes    int `json:"maxValueRunes"`
		MaxMessageRunes  int `json:"maxMessageRunes"`
	} `json:"limits"`
	Cases []json.RawMessage `json:"cases"`
}

type fixtureCase struct {
	Name   string         `json:"name"`
	Note   string         `json:"note"`
	ChatID string         `json:"chatId"`
	Form   map[string]any `json:"form"`
	Values map[string]any `json:"values"`
	Want   any            `json:"want"`
}

// fixturePath resolves testdata/ask-templates.json from the repo root.
func fixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "testdata", "ask-templates.json")
}

func loadFixture(t *testing.T) fixtureFile {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatalf("read shared fixture: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse shared fixture: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("shared fixture has no cases — a vacuous pass is worse than a red run")
	}
	return f
}

// expandFixture resolves the two size directives the fixture uses so a
// 32 000-rune expectation does not have to be typed out. Mirrored exactly in
// lib/__tests__/ask-template.test.ts.
func expandFixture(node any) any {
	switch v := node.(type) {
	case map[string]any:
		if rep, ok := v["$repeat"]; ok {
			s, _ := rep.(string)
			n, _ := v["$count"].(float64)
			return strings.Repeat(s, int(n))
		}
		if parts, ok := v["$concat"].([]any); ok {
			var b strings.Builder
			for _, p := range parts {
				s, _ := expandFixture(p).(string)
				b.WriteString(s)
			}
			return b.String()
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = expandFixture(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = expandFixture(val)
		}
		return out
	}
	return node
}

func TestRenderAgainstSharedFixture(t *testing.T) {
	f := loadFixture(t)

	for _, raw := range f.Cases {
		var c fixtureCase
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("decode case: %v", err)
		}
		t.Run(c.Name, func(t *testing.T) {
			// The form is expanded as a generic tree first (a directive can
			// sit inside `template`), then re-marshalled into the real type,
			// so the test drives exactly the struct the API stores.
			expandedForm, _ := expandFixture(c.Form).(map[string]any)
			formJSON, err := json.Marshal(expandedForm)
			if err != nil {
				t.Fatalf("re-marshal form: %v", err)
			}
			var form Form
			if err := json.Unmarshal(formJSON, &form); err != nil {
				t.Fatalf("decode form: %v", err)
			}

			values := Values{}
			if expandedValues, ok := expandFixture(c.Values).(map[string]any); ok {
				for k, v := range expandedValues {
					values[k] = v
				}
			}

			want, _ := expandFixture(c.Want).(string)
			got := Render(form, values, c.ChatID)
			if got != want {
				t.Fatalf("Render mismatch\n got %q\nwant %q\nnote: %s", got, want, c.Note)
			}
		})
	}
}

// The caps are a cross-language contract too: a value cap that moves in Go
// and not in TypeScript is a message that differs between the preview the
// user approved and the text that was sent.
func TestCapsMatchSharedFixture(t *testing.T) {
	f := loadFixture(t)

	for _, tt := range []struct {
		name string
		got  int
		want int
	}{
		{"MaxForms", MaxForms, f.Limits.MaxForms},
		{"MaxFieldsPerForm", MaxFieldsPerForm, f.Limits.MaxFieldsPerForm},
		{"MaxLabelRunes", MaxLabelRunes, f.Limits.MaxLabelRunes},
		{"MaxTemplateRunes", MaxTemplateRunes, f.Limits.MaxTemplateRunes},
		{"MaxValueRunes", MaxValueRunes, f.Limits.MaxValueRunes},
		{"MaxMessageRunes", MaxMessageRunes, f.Limits.MaxMessageRunes},
	} {
		if tt.want == 0 {
			t.Fatalf("%s missing from the fixture's limits block", tt.name)
		}
		if tt.got != tt.want {
			t.Errorf("%s = %d, fixture says %d — change both, or the two renderers disagree",
				tt.name, tt.got, tt.want)
		}
	}
}

// RenderByID is what the CLI preview and any server-side render go through.
func TestRenderByID(t *testing.T) {
	forms, err := Parse(`[
		{"id":"receipt","label":"Add a receipt","template":"Supplier: {{supplier}}",
		 "fields":[{"name":"supplier","label":"Supplier","type":"text"}]}
	]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := RenderByID(forms, "receipt", Values{"supplier": "Vodafone"}, "chat_1")
	if err != nil {
		t.Fatalf("RenderByID: %v", err)
	}
	if got != "Supplier: Vodafone" {
		t.Fatalf("RenderByID = %q", got)
	}

	if _, err := RenderByID(forms, "nope", nil, "chat_1"); err == nil ||
		!strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown form id error = %v, want it to name the id", err)
	}
}
