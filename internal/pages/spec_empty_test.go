package pages

import (
	"strings"
	"testing"
)

const stagedPageSpec = `apiVersion: crewship/v1
kind: Page
metadata:
  name: Staged document
  slug: staged-document
spec:
  panels: []
`

func TestParseDocumentAllowEmptyPreservesValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"staged page", stagedPageSpec, true},
		{"normal panels", validSpec, true},
		{"leading separator", "---\n" + stagedPageSpec, true},
		{"trailing separator", stagedPageSpec + "---\n", true},
		{"unknown field", stagedPageSpec + "unexpected: true\n", false},
		{"two pages", stagedPageSpec + "---\n" + stagedPageSpec, false},
		{"malformed tail", stagedPageSpec + "---\n[", false},
		{"oversize", strings.Repeat(" ", MaxSpecBytes+1), false},
		{"missing name", strings.Replace(stagedPageSpec, "name: Staged document", "name: ''", 1), false},
		{"reserved slug", strings.Replace(stagedPageSpec, "slug: staged-document", "slug: access", 1), false},
		{"invalid slug", strings.Replace(stagedPageSpec, "slug: staged-document", "slug: Bad/Slug", 1), false},
		{"wrong kind", strings.Replace(stagedPageSpec, "kind: Page", "kind: Crew", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := ParseDocumentAllowEmpty([]byte(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if err == nil {
				if err := doc.ValidateEmpty(); err != nil {
					t.Fatalf("parsed document fails ValidateEmpty: %v", err)
				}
			}
		})
	}
}

func TestStagedPageDoesNotRelaxDefaultValidation(t *testing.T) {
	t.Parallel()
	if _, err := ParseDocument([]byte(stagedPageSpec)); err == nil {
		t.Fatal("default parser accepted panel-free Page")
	}
	doc, err := ParseDocumentAllowEmpty([]byte(stagedPageSpec))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(); err == nil {
		t.Fatal("default Validate accepted panel-free Page")
	}
	if err := doc.ValidateEmpty(); err != nil {
		t.Fatal(err)
	}
}
