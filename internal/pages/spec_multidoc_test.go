package pages

import (
	"strings"
	"testing"
)

// A YAML stream carrying more than one document must be REFUSED, not silently
// truncated to its first.
//
// This is reachable and it is our own doing: `crewship export page` with no
// slug emits every page in the workspace as one `---`-separated stream, and
// `crewship page create --file` is the documented way back in. Decoding one
// document and ignoring the rest means exporting four pages and importing one,
// with an exit code of zero and nothing on stderr — the failure mode a person
// discovers weeks later when they look for the other three.
//
// Refusing costs nothing a caller wanted: nobody hands `page create` four pages
// and means one.
func TestParseDocument_RefusesAStreamOfMoreThanOneDocument(t *testing.T) {
	one := `apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila
  slug: flotila
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/lookout
      producer: script/watch.sh
      sla: 30s
      span: 12
`

	t.Run("one document parses", func(t *testing.T) {
		if _, err := ParseDocument([]byte(one)); err != nil {
			t.Fatalf("ParseDocument: %v", err)
		}
	})

	// A leading separator is still ONE document. yaml treats it as the start of
	// the first, and `crewship export page` writes it on every document it
	// emits — including a single-page export, which must keep working.
	t.Run("a leading separator is still one document", func(t *testing.T) {
		if _, err := ParseDocument([]byte("---\n" + one)); err != nil {
			t.Fatalf("ParseDocument: %v", err)
		}
	})

	t.Run("two documents are refused", func(t *testing.T) {
		_, err := ParseDocument([]byte(one + "---\n" + strings.Replace(one, "flotila", "sit", 1)))
		if err == nil {
			t.Fatal("two pages parsed as one — the second was silently dropped")
		}
		// The message has to say what to do, because the caller's file is
		// probably the output of `crewship export page` and splitting it is the
		// answer, not editing it.
		if !strings.Contains(err.Error(), "one page") {
			t.Errorf("the refusal does not name the rule: %v", err)
		}
	})

	t.Run("trailing whitespace after a document is not a document", func(t *testing.T) {
		if _, err := ParseDocument([]byte(one + "\n\n   \n")); err != nil {
			t.Fatalf("trailing blank lines were read as a second document: %v", err)
		}
	})
}

// A trailing separator is not a second document.
//
// yaml.v3 returns a ZERO Document with a nil error for the nothing after a
// final `---`, so a naive "did a second Decode succeed" check refused ordinary
// one-page files — including the ones `crewship export page` writes, since it
// ends every document it emits with its own separator.
func TestParseDocument_ATrailingSeparatorIsNotASecondDocument(t *testing.T) {
	one := `apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila
  slug: flotila
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/lookout
      producer: script/watch.sh
      sla: 30s
      span: 12
`
	for name, doc := range map[string]string{
		"trailing separator":             one + "---\n",
		"leading and trailing separator": "---\n" + one + "---\n",
		"trailing separator and blanks":  one + "---\n\n  \n",
	} {
		if _, err := ParseDocument([]byte(doc)); err != nil {
			t.Errorf("%s: refused a one-page file: %v", name, err)
		}
	}
}
