package askforms

// An option whose stored text is not what an answer would be cleaned to.
//
// Every answer reaches this package through coerceValue → cleanValue, which
// trims the edges. The option list did not get the same treatment, so
// `"options": ["Travel ", "Food"]` saved without complaint and then refused
// the only answer it renders: the sheet shows "Travel", the user picks
// "Travel", ValidateAnswers cleans that to "Travel", compares it against the
// RAW "Travel " and reports "Category must be one of its listed options" —
// an error pointing at the user's choice when the defect is in the
// definition. The form was permanently unsubmittable, in the composer and in
// `crewship agent ask-preview` alike. `["Travel", "Travel "]` got past the
// duplicate check the same way and then rendered the same choice twice.
//
// The fix is on the WRITE path — the options are canonicalised exactly as an
// answer will be, so the two can never be spelled differently — with the
// duplicate check reading them the same way, so the second half of the bug is
// refused rather than normalised into a silent collision. The answer path is
// the second line: it compares cleaned against cleaned, which is what keeps a
// row written before this rule answerable instead of failing closed on a
// definition its user cannot fix. lib/ask-validate.ts makes the same
// comparison, pinned by testdata/ask-field-types.json.

import (
	"strings"
	"testing"
)

func formWithOptions(t *testing.T, fieldType string, optionsJSON string) string {
	t.Helper()
	return `[{"id":"r","label":"R","template":"{{category}}","fields":[` +
		`{"name":"category","label":"Category","type":"` + fieldType + `","options":` + optionsJSON + `}]}]`
}

// The write path stores the option text an answer will actually be compared
// against.
func TestNormalizeTrimsOptions(t *testing.T) {
	tests := []struct {
		name        string
		fieldType   string
		optionsJSON string
		want        []string
	}{
		{"a trailing space", "select", `["Travel ","Food"]`, []string{"Travel", "Food"}},
		{"a leading space", "select", `[" Travel","Food"]`, []string{"Travel", "Food"}},
		{"a tab and a newline", "multiselect", `["\tTravel","Food\n"]`, []string{"Travel", "Food"}},
		// Control characters never reach the wire in an answer, so they must
		// not sit in an option either — cleanValue is one rule, not two.
		{"an embedded control character", "select", `["Tra\u0007vel","Food"]`, []string{"Travel", "Food"}},
		{"already canonical is untouched", "select", `["Travel","Food"]`, []string{"Travel", "Food"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored, err := Normalize(formWithOptions(t, tt.fieldType, tt.optionsJSON))
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			forms, err := Parse(stored)
			if err != nil {
				t.Fatalf("Parse(stored): %v", err)
			}
			got := forms[0].Fields[0].Options
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("stored options = %q, want %q\n%s", got, tt.want, stored)
			}

			// The whole point: what was stored can be answered.
			for _, opt := range tt.want {
				if errs := ValidateAnswers(forms[0], Values{"category": opt}); len(errs) != 0 {
					t.Errorf("answering %q against the stored form failed: %v", opt, errs)
				}
			}

			// And the canonical form is a fixed point.
			again, err := Normalize(stored)
			if err != nil {
				t.Fatalf("re-Normalize: %v", err)
			}
			if again != stored {
				t.Errorf("Normalize is not idempotent over options:\nfirst:\n%s\nsecond:\n%s", stored, again)
			}
		})
	}
}

// The other half. Trimming cannot silently merge two options an author wrote
// as different ones — that would store a picker with the same choice in it
// twice, which is the outcome the duplicate check exists to prevent.
func TestValidateRejectsOptionsThatDifferOnlyByWhitespace(t *testing.T) {
	tests := []struct {
		name        string
		optionsJSON string
		wantErr     string
	}{
		{"a trailing space", `["Travel","Travel "]`, `lists the option "Travel" twice`},
		{"a leading space", `[" Travel","Travel"]`, `lists the option "Travel" twice`},
		{"a tab", `["Travel","Travel\t"]`, `lists the option "Travel" twice`},
		// Still the plain duplicate, reported the same way.
		{"an exact duplicate", `["Travel","Travel"]`, `lists the option "Travel" twice`},
		// A whitespace-only option is blank, not a duplicate — the more
		// specific complaint wins, as it did before.
		{"a blank option", `["Travel","  "]`, "has a blank option"},
		{"genuinely different options", `["Travel","Travelling"]`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(formWithOptions(t, "select", tt.optionsJSON))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

// A row that predates the write-path rule — edited straight in the database,
// or saved before this shipped — must still be answerable. Failing closed
// there would hand the user an unfixable form, which is the same call the
// uncompilable-pattern branch already makes.
func TestValidateAnswers_LegacyUntrimmedOptionIsAnswerable(t *testing.T) {
	form := Form{
		ID: "r", Label: "R", Template: "{{category}}",
		Fields: []Field{{Name: "category", Label: "Category", Type: "select",
			Options: []string{"Travel ", " Food"}}},
	}
	// Render shows "Travel"; answering "Travel" must therefore be accepted.
	if got := Render(form, Values{"category": "Travel "}, "chat_1"); got != "Travel" {
		t.Fatalf("Render = %q, want %q", got, "Travel")
	}
	if errs := ValidateAnswers(form, Values{"category": "Travel"}); len(errs) != 0 {
		t.Errorf("the only answer the form renders was refused: %v", errs)
	}
	if errs := ValidateAnswers(form, Values{"category": "Trave"}); len(errs) != 1 {
		t.Errorf("trimming widened the option list: %v", errs)
	}
}
