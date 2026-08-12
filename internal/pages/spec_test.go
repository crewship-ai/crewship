package pages

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The page spec is layer 1 of §6: a human writes YAML, a machine writes JSON,
// both validated, no third DSL. This file pins the half a human writes.
//
// The authoring gate (§10b.1) is deliberately cheap and synchronous — validate
// the shape, then check that every declared producer and owner RESOLVES. This
// package owns the first half; the second needs the database and belongs to the
// handler. What is here is what stops an agent saving a page that is malformed
// before anything has to be looked up.

const validSpec = `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      title: Jede to?
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      span: 8
    - id: zatizeni
      schema: metric.v1
      owner: crew/lookout
      producer: routine/load-check
      sla: 1h
      span: 4
`

func TestParseDocument_AcceptsThePRDExample(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(validSpec))
	if err != nil {
		t.Fatalf("the §6 example was refused: %v", err)
	}
	if doc.Metadata.Slug != "fleet-201" {
		t.Errorf("slug = %q, want %q", doc.Metadata.Slug, "fleet-201")
	}
	if len(doc.Spec.Panels) != 2 {
		t.Fatalf("got %d panels, want 2", len(doc.Spec.Panels))
	}

	p := doc.Spec.Panels[0]
	if p.Schema != SchemaStatus {
		t.Errorf("schema = %q, want %q", p.Schema, SchemaStatus)
	}
	sla, err := p.SLADuration()
	if err != nil {
		t.Fatalf("SLADuration: %v", err)
	}
	if sla != 30*time.Second {
		t.Errorf("sla = %s, want 30s", sla)
	}
	crew, err := p.OwnerCrewSlug()
	if err != nil {
		t.Fatalf("OwnerCrewSlug: %v", err)
	}
	if crew != "lookout" {
		t.Errorf("owner crew = %q, want %q", crew, "lookout")
	}
	kind, ref, err := p.ProducerParts()
	if err != nil {
		t.Fatalf("ProducerParts: %v", err)
	}
	if kind != ProducerScript || ref != "watch-services.sh" {
		t.Errorf("producer = (%q, %q), want (script, watch-services.sh)", kind, ref)
	}

	// The second panel declares no span. It must land on the full-width
	// default rather than on 0, which would render a zero-width panel.
	if got := doc.Spec.Panels[1].Span; got != 4 {
		t.Errorf("panel 2 span = %d, want 4", got)
	}
}

func TestValidateDocument_RefusesMalformedSpecs(t *testing.T) {
	t.Parallel()

	// Each case is the §6 document with one thing wrong, so the failure is
	// unambiguously the thing under test.
	swap := func(old, new string) string { return strings.Replace(validSpec, old, new, 1) }

	cases := []struct {
		name string
		spec string
		why  string
	}{
		{
			name: "a foreign apiVersion",
			spec: swap("apiVersion: crewship/v1", "apiVersion: dashboards/v1"),
			why:  "§2.2/§6: the envelope is the one internal/manifest already parses",
		},
		{
			name: "a foreign kind",
			spec: swap("kind: Page", "kind: Surface"),
			why: "§1: one noun everywhere. The design artefact used three and none of them may " +
				"survive into the implementation",
		},
		{
			name: "no slug",
			spec: swap("  slug: fleet-201\n", ""),
			why:  "obstacle 10: pages are slug-addressable from the first migration",
		},
		{
			name: "a slug that is not a slug",
			spec: swap("slug: fleet-201", "slug: Flotila .201"),
			why:  "it goes in a URL",
		},
		{
			name: "no panels",
			spec: "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Empty\n  slug: empty\nspec:\n  panels: []\n",
			why:  "a page with no panels renders nothing and cannot be pushed to",
		},
		{
			name: "two panels with one id",
			spec: swap("- id: zatizeni", "- id: sluzby"),
			why:  "the panel id is the push address; a duplicate makes one of them unreachable",
		},
		{
			name: "a panel with no id",
			spec: swap("    - id: sluzby\n", "    - \n"),
			why:  "same reason",
		},
		{
			name: "a schema outside the closed set",
			spec: swap("schema: status.v1", "schema: gauge.v1"),
			why:  "§3: a new panel kind is a server release",
		},
		{
			name: "a schema that is reserved but not built",
			spec: swap("schema: status.v1", "schema: embed.v1"),
			why:  "§3.1 places embed at v1.2; the name is reserved in the migration, not usable in a spec",
		},
		{
			name: "no sla",
			spec: swap("      sla: 30s\n", ""),
			why:  "§4 rule 1: a panel without an SLA does not validate. There is no default",
		},
		{
			name: "an sla of zero",
			spec: swap("sla: 30s", "sla: 0s"),
			why:  "zero is that missing default wearing a number",
		},
		{
			name: "an sla that is not a duration",
			spec: swap("sla: 30s", "sla: soon"),
			why:  "the freshness contract is arithmetic; 'soon' has no boundary to cross",
		},
		{
			name: "a span off the grid",
			spec: swap("span: 8", "span: 13"),
			why:  "§9: span maps to col-span-n on a 12-column grid",
		},
		{
			name: "no owner",
			spec: swap("      owner: crew/lookout\n", ""),
			why:  "§7.1 rule 2: owner_crew_id IS the ACL, so a panel without one has no ACL",
		},
		{
			name: "an owner that is not a crew",
			spec: swap("owner: crew/lookout", "owner: user/pavel"),
			why: "§7.1 rule 2: the permission anchor is a crew. A per-user panel owner would be the " +
				"Retool shape — a rule written into each component, discovered at audit time",
		},
		{
			name: "no producer",
			spec: swap("      producer: script/watch-services.sh\n", ""),
			why:  "§7.1 rule 4: only the declared producer may write the payload, so there has to be one",
		},
		{
			name: "a producer that is a query",
			spec: swap("producer: script/watch-services.sh", "producer: sql/SELECT count(*) FROM users"),
			why: "§1: a page holds no query, no datasource, no connection string and no credentials. " +
				"This is the load-bearing property of the whole feature",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDocument([]byte(tc.spec)); err == nil {
				t.Errorf("spec was accepted but must be refused — %s", tc.why)
			}
		})
	}
}

func TestValidateDocument_RefusesTooManyPanels(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Big\n  slug: big\nspec:\n  panels:\n")
	for i := 0; i <= MaxPanelsPerPage; i++ {
		b.WriteString("    - id: p")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("x", i/26+1))
		b.WriteString("\n      schema: metric.v1\n      owner: crew/lookout\n" +
			"      producer: routine/r\n      sla: 1m\n      span: 3\n")
	}

	_, err := ParseDocument([]byte(b.String()))
	if err == nil {
		t.Fatalf("a page with %d panels was accepted; §10b.3 caps it at %d — beyond this nobody reads it anyway",
			MaxPanelsPerPage+1, MaxPanelsPerPage)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	if ve.Code != CodeInvalidSpec {
		t.Errorf("code = %q, want %q", ve.Code, CodeInvalidSpec)
	}
}

func TestValidateDocument_RefusesAnOversizeSpec(t *testing.T) {
	t.Parallel()

	spec := validSpec + "\n# " + strings.Repeat("padding ", MaxSpecBytes/8)

	_, err := ParseDocument([]byte(spec))
	if err == nil {
		t.Fatal("an oversized spec was accepted; §10 caps it at 256 KiB")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	if ve.Code != CodeTooLarge {
		t.Errorf("code = %q, want %q", ve.Code, CodeTooLarge)
	}
}

// Public is opt-in per panel and defaults to closed (§7.3.2 rule 2). Publishing
// must never be a bulk action over panels the author has not looked at.
func TestParseDocument_PanelsAreNotPublicByDefault(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(validSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, p := range doc.Spec.Panels {
		if p.Public {
			t.Errorf("panel %q is public without saying so; default deny (§7.3.2 rule 2)", p.ID)
		}
	}
}
