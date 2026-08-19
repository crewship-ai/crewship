package askforms

// Validation is the whole point of the package existing on the server.
//
// PRD §7 rule 1: a template that names a field which does not exist is
// rejected when the form is SAVED, not when it is rendered. The author finds
// out while authoring; the user must never meet a broken template. Every
// rejection below is that same idea applied to a different way of writing a
// form that cannot work.
//
// Table-driven, and the assertions are on the MESSAGE, not just the error:
// "invalid input" on a JSON blob with four forms in it is a hunt, and the
// person reading it is mid-edit.

import (
	"strings"
	"testing"
)

func TestParseRejections(t *testing.T) {
	longLabel := strings.Repeat("a", MaxLabelRunes+1)
	// Runes, not bytes: 48 two-byte characters must PASS a rune cap and would
	// fail a byte cap. The Czech author must not get a shorter field than the
	// English one.
	wideLabel := strings.Repeat("é", MaxLabelRunes)
	longTemplate := "x: {{a}} " + strings.Repeat("t", MaxTemplateRunes)

	oneField := `"fields":[{"name":"a","label":"A","type":"text"}]`

	tests := []struct {
		name    string
		in      string
		wantErr string // "" means it must be accepted
	}{
		{
			name: "empty is not configured, not an error",
			in:   "",
		},
		{
			name: "an empty array is not configured either",
			in:   "[]",
		},
		{
			name:    "an object is not a list of forms",
			in:      `{"id":"receipt"}`,
			wantErr: "must be a JSON array",
		},
		{
			name:    "malformed JSON names itself",
			in:      `[{"id":`,
			wantErr: "not valid JSON",
		},
		{
			name: "the receipt form from the PRD is accepted verbatim",
			in: `[{"id":"receipt","label":"Add a receipt",
				"template":"Please file this receipt.\n\nSupplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}",
				"attachment":"required",
				"fields":[
					{"name":"supplier","label":"Supplier","type":"text","required":true,"placeholder":"Vodafone"},
					{"name":"amount","label":"Amount","type":"money","required":true,"currency":["CZK","EUR","USD"]}
				]}]`,
		},
		{
			name: "an unknown field type is accepted and falls back to text",
			in: `[{"id":"f","label":"F","template":"{{a}}",
				"fields":[{"name":"a","label":"A","type":"starrating"}]}]`,
		},
		{
			name:    "a misspelled key is refused rather than silently dropped",
			in:      `[{"id":"f","label":"F","template":"{{a}}","attachments":"required",` + oneField + `}]`,
			wantErr: "attachments",
		},

		// ── PRD §7 rule 1 — the placeholder that names nothing ──────────
		{
			name:    "a placeholder naming no field is refused, naming both the placeholder and the form",
			in:      `[{"id":"receipt","label":"Add a receipt","template":"Supplier: {{suplier}}","fields":[{"name":"supplier","label":"Supplier","type":"text"}]}]`,
			wantErr: `form "receipt": template names {{suplier}}`,
		},
		{
			name:    "a placeholder that is not even a name is refused",
			in:      `[{"id":"f","label":"F","template":"{{ not a name }}",` + oneField + `}]`,
			wantErr: "{{not a name}}",
		},
		{
			name:    "an empty placeholder is refused",
			in:      `[{"id":"f","label":"F","template":"x {{}}",` + oneField + `}]`,
			wantErr: "{{}}",
		},
		{
			name: "a money field also answers to <name>_currency",
			in: `[{"id":"f","label":"F","template":"{{amount}} {{amount_currency}}",
				"fields":[{"name":"amount","label":"Amount","type":"money"}]}]`,
		},
		{
			name: "spaces inside the braces are fine",
			in:   `[{"id":"f","label":"F","template":"{{ a }}",` + oneField + `}]`,
		},

		// ── the structural refusals ─────────────────────────────────────
		{
			name:    "a form with no fields is a chip pretending to be a form",
			in:      `[{"id":"receipt","label":"Add a receipt","template":"hello","fields":[]}]`,
			wantErr: `form "receipt": has no fields`,
		},
		{
			name:    "two fields with the same name",
			in:      `[{"id":"receipt","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"},{"name":"a","label":"Again","type":"text"}]}]`,
			wantErr: `form "receipt": two fields are named "a"`,
		},
		{
			name:    "a select with no options",
			in:      `[{"id":"receipt","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"select"}]}]`,
			wantErr: `field "a" is a select with no options`,
		},
		{
			name:    "a multiselect with no options",
			in:      `[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"multiselect","options":[]}]}]`,
			wantErr: `field "a" is a multiselect with no options`,
		},
		{
			name:    "a select with a blank option",
			in:      `[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"select","options":["Telco","  "]}]}]`,
			wantErr: `field "a" has a blank option`,
		},
		{
			name:    "two forms with the same id",
			in:      `[{"id":"receipt","label":"One","template":"{{a}}",` + oneField + `},{"id":"receipt","label":"Two","template":"{{a}}",` + oneField + `}]`,
			wantErr: `two forms share the id "receipt"`,
		},
		{
			name:    "a money field collides with a field named after its currency",
			in:      `[{"id":"r","label":"R","template":"{{amount}}","fields":[{"name":"amount","label":"A","type":"money"},{"name":"amount_currency","label":"C","type":"text"}]}]`,
			wantErr: `"amount_currency" is reserved`,
		},
		{
			name:    "an id that is not a slug",
			in:      `[{"id":"Add Receipt","label":"R","template":"{{a}}",` + oneField + `}]`,
			wantErr: "id",
		},
		{
			name:    "a field name that could never be a placeholder",
			in:      `[{"id":"r","label":"R","template":"hello","fields":[{"name":"Supplier Name","label":"S","type":"text"}]}]`,
			wantErr: `field name "Supplier Name"`,
		},
		{
			name:    "a form with no label",
			in:      `[{"id":"r","label":"","template":"{{a}}",` + oneField + `}]`,
			wantErr: "label is required",
		},
		{
			name:    "a form with an empty template",
			in:      `[{"id":"r","label":"R","template":"   ",` + oneField + `}]`,
			wantErr: "template is required",
		},
		{
			name:    "an attachment policy that is not one of the three",
			in:      `[{"id":"r","label":"R","template":"{{a}}","attachment":"maybe",` + oneField + `}]`,
			wantErr: "attachment must be one of",
		},

		// ── the caps, at the boundary and one past it ───────────────────
		{
			name: "four forms is the cap and passes",
			in: `[{"id":"a","label":"A","template":"{{a}}",` + oneField + `},
			     {"id":"b","label":"B","template":"{{a}}",` + oneField + `},
			     {"id":"c","label":"C","template":"{{a}}",` + oneField + `},
			     {"id":"d","label":"D","template":"{{a}}",` + oneField + `}]`,
		},
		{
			name: "five forms is refused",
			in: `[{"id":"a","label":"A","template":"{{a}}",` + oneField + `},
			     {"id":"b","label":"B","template":"{{a}}",` + oneField + `},
			     {"id":"c","label":"C","template":"{{a}}",` + oneField + `},
			     {"id":"d","label":"D","template":"{{a}}",` + oneField + `},
			     {"id":"e","label":"E","template":"{{a}}",` + oneField + `}]`,
			wantErr: "at most 4 forms",
		},
		{
			name: "six fields is the cap and passes",
			in: `[{"id":"r","label":"R","template":"{{a}}","fields":[
				{"name":"a","label":"A","type":"text"},{"name":"b","label":"B","type":"text"},
				{"name":"c","label":"C","type":"text"},{"name":"d","label":"D","type":"text"},
				{"name":"e","label":"E","type":"text"},{"name":"f","label":"F","type":"text"}]}]`,
		},
		{
			name: "seven fields is refused, and the form is named",
			in: `[{"id":"receipt","label":"R","template":"{{a}}","fields":[
				{"name":"a","label":"A","type":"text"},{"name":"b","label":"B","type":"text"},
				{"name":"c","label":"C","type":"text"},{"name":"d","label":"D","type":"text"},
				{"name":"e","label":"E","type":"text"},{"name":"f","label":"F","type":"text"},
				{"name":"g","label":"G","type":"text"}]}]`,
			wantErr: `form "receipt": at most 6 fields`,
		},
		{
			name: "a 48-character label passes",
			in:   `[{"id":"r","label":"` + strings.Repeat("a", MaxLabelRunes) + `","template":"{{a}}",` + oneField + `}]`,
		},
		{
			name: "48 two-byte characters is 48 characters, not 96",
			in:   `[{"id":"r","label":"` + wideLabel + `","template":"{{a}}",` + oneField + `}]`,
		},
		{
			name:    "a 49-character label is refused",
			in:      `[{"id":"r","label":"` + longLabel + `","template":"{{a}}",` + oneField + `}]`,
			wantErr: "label exceeds 48 characters",
		},
		{
			name:    "a field label over the cap is refused and names the field",
			in:      `[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"` + longLabel + `","type":"text"}]}]`,
			wantErr: `field "a": label exceeds 48 characters`,
		},
		{
			name:    "a template one character over the cap is refused",
			in:      `[{"id":"r","label":"R","template":"` + longTemplate + `",` + oneField + `}]`,
			wantErr: "template exceeds 2000 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse(%.80s) errored: %v", tt.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse(%.80s) was accepted, want %q", tt.in, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q — a message that does not "+
					"name the offending form is not something an author can act on",
					err.Error(), tt.wantErr)
			}
		})
	}
}

// A template of exactly the cap must pass; one rune more must not. Written
// out separately because the two strings have to be built precisely.
func TestTemplateCapBoundary(t *testing.T) {
	body := `{{a}}` + strings.Repeat("t", MaxTemplateRunes-5)
	if n := len([]rune(body)); n != MaxTemplateRunes {
		t.Fatalf("test setup: template is %d runes, want %d", n, MaxTemplateRunes)
	}
	def := `[{"id":"r","label":"R","template":"` + body + `","fields":[{"name":"a","label":"A","type":"text"}]}]`
	if _, err := Parse(def); err != nil {
		t.Fatalf("a template of exactly %d runes was refused: %v", MaxTemplateRunes, err)
	}

	over := `[{"id":"r","label":"R","template":"` + body + `t","fields":[{"name":"a","label":"A","type":"text"}]}]`
	if _, err := Parse(over); err == nil {
		t.Fatalf("a template of %d runes was accepted", MaxTemplateRunes+1)
	}
}

// Normalize is the write path: it returns the canonical JSON to store, or ""
// for "not configured" so the column has one representation of unset — the
// same contract suggested_prompts settled on.
func TestNormalize(t *testing.T) {
	stored, err := Normalize(`[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]}]`)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !strings.Contains(stored, `"attachment": "none"`) {
		t.Errorf("stored form does not spell out the default attachment policy:\n%s", stored)
	}
	// Canonical means re-normalising is a no-op.
	again, err := Normalize(stored)
	if err != nil {
		t.Fatalf("re-Normalize: %v", err)
	}
	if again != stored {
		t.Errorf("Normalize is not idempotent:\nfirst:\n%s\nsecond:\n%s", stored, again)
	}

	for _, unset := range []string{"", "   ", "[]", "\n"} {
		got, err := Normalize(unset)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", unset, err)
		}
		if got != "" {
			t.Errorf("Normalize(%q) = %q, want \"\" so the column stores NULL", unset, got)
		}
	}
}

// A template that would not survive storage must not be stored: the angle
// brackets and ampersands in a template are ordinary prose, not HTML.
func TestNormalizeDoesNotEscapeHTML(t *testing.T) {
	stored, err := Normalize(`[{"id":"r","label":"R","template":"a < b & c > d {{a}}","fields":[{"name":"a","label":"A","type":"text"}]}]`)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	// If json.Marshal's HTML escaping were left on, the stored text would
	// hold < / & escapes instead of the characters the author
	// typed, and the config tab would show them that on the next load.
	if !strings.Contains(stored, "a < b & c > d") {
		t.Errorf("template did not survive the round trip verbatim (HTML escaping "+
			"is on, so the author reads back something they did not type):\n%s", stored)
	}
	if strings.Contains(stored, "u003c") {
		t.Errorf("template was HTML-escaped on write:\n%s", stored)
	}
}
