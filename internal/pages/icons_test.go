package pages

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The panel icon vocabulary (icons.go).
//
// Two properties are load-bearing and this file pins both:
//
//  1. THE SET IS CLOSED AND THE REFUSAL NAMES IT. An open string would be
//     accepted here and undrawable on the client, and a header with no glyph
//     reads as a design decision rather than as an error. A refusal that does
//     not list the allowed names sends the author back to the docs, so the
//     list is part of the contract and is asserted as such.
//
//  2. NO ICON IS STILL A VALID PANEL. The field is optional and every page
//     authored before it existed must go on validating unchanged, keeping the
//     icon its schema implies.
//
// The client half of the parity — every name below resolving to a real glyph —
// is asserted from the other side, in
// components/features/pages/panels/__tests__/panel-icon.test.tsx, which reads
// icons.go and fails when the two lists disagree.

// iconSpec is the §6 example with an icon on its first panel.
const iconSpec = `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: pamet
      schema: metric.v1
      title: Volná paměť
      icon: memory
      owner: crew/lookout
      producer: script/free.sh
      sla: 30s
      span: 4
    - id: sluzby
      schema: status.v1
      title: Jede to?
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      span: 8
`

func TestParseDocument_AcceptsEveryIconInTheVocabulary(t *testing.T) {
	t.Parallel()

	for _, icon := range PanelIcons {
		t.Run(string(icon), func(t *testing.T) {
			spec := strings.Replace(iconSpec, "icon: memory", "icon: "+string(icon), 1)
			doc, err := ParseDocument([]byte(spec))
			if err != nil {
				t.Fatalf("icon %q is in the vocabulary but was refused: %v", icon, err)
			}
			got := doc.Spec.Panels[0].Icon
			if got != icon {
				t.Errorf("icon = %q, want %q", got, icon)
			}
			if !got.Known() {
				t.Errorf("icon %q parsed but does not report Known()", got)
			}
		})
	}
}

func TestParseDocument_RefusesAnIconOutsideTheSet(t *testing.T) {
	t.Parallel()

	// Each of these is a name a producer would plausibly reach for, and every
	// one of them is a blank header if the server lets it through.
	cases := []struct {
		name string
		icon string
		why  string
	}{
		{
			name: "a lucide name we do not admit",
			icon: "MemoryStick",
			why: "the vocabulary is OURS, not the icon library's: a spec that names a lucide export " +
				"pins us to that library and breaks when it renames one",
		},
		{
			name: "a plausible synonym",
			icon: "ram",
			why:  "one name per concept, or two pages mean the same thing and do not look it",
		},
		{
			name: "the concept spelled with capitals",
			icon: "Memory",
			why: "case is not folded on purpose — accepting it would teach a spelling that is not " +
				"the vocabulary, and the next guess would not be forgiven",
		},
		{
			name: "a verdict rather than a subject",
			icon: "check",
			why: "the panel already renders ✓ / ! / ✕ per item and a freshness word; a tick in the " +
				"header is a second verdict on the same card",
		},
		{
			name: "an inherited object key",
			icon: "constructor",
			why:  "the lookup is a set, and no name that is on every object may ever resolve",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := strings.Replace(iconSpec, "icon: memory", "icon: "+tc.icon, 1)
			_, err := ParseDocument([]byte(spec))
			if err == nil {
				t.Fatalf("icon %q was accepted but must be refused — %s", tc.icon, tc.why)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %T: %v", err, err)
			}
			if ve.Code != CodeInvalidSpec {
				t.Errorf("code = %q, want %q", ve.Code, CodeInvalidSpec)
			}

			// The refusal has to name the panel, the bad value, and — the part
			// that saves the author a trip to the docs — every value it would
			// have taken instead.
			detail := ve.Detail
			if !strings.Contains(detail, tc.icon) {
				t.Errorf("refusal does not quote the rejected icon %q: %s", tc.icon, detail)
			}
			if !strings.Contains(detail, `"pamet"`) {
				t.Errorf("refusal does not name the panel: %s", detail)
			}
			for _, allowed := range PanelIcons {
				if !strings.Contains(detail, string(allowed)) {
					t.Fatalf("refusal does not list %q; a closed set whose error hides its members "+
						"is a set the author has to go and look up: %s", allowed, detail)
				}
			}
		})
	}
}

func TestParseDocument_APanelNeedsNoIcon(t *testing.T) {
	t.Parallel()

	// The second panel of the fixture declares none. It must validate and come
	// back empty — that is the signal the client reads as "use the icon this
	// panel's schema implies", and a default invented here would take that
	// choice away from the renderer.
	doc, err := ParseDocument([]byte(iconSpec))
	if err != nil {
		t.Fatalf("a panel without an icon was refused: %v", err)
	}
	if got := doc.Spec.Panels[1].Icon; got != "" {
		t.Errorf("panel 2 icon = %q, want empty — no icon means the schema's own", got)
	}
	if PanelIcon("").Known() {
		t.Error(`PanelIcon("").Known() is true; the empty string is the absence of a value, not a member`)
	}
}

func TestParseDocument_IconSurvivesAYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	// YAML is the authoring format and the stored spec is re-read on every
	// export, plan and rollback. A field that parses but does not marshal back
	// is a field the next `page export | page import` silently drops.
	first, err := ParseDocument([]byte(iconSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	out, err := yaml.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "icon: memory") {
		t.Fatalf("the re-emitted document carries no icon:\n%s", out)
	}
	// The panel that declared none must not gain one: `omitempty` is what
	// keeps "no icon" from becoming `icon: ""`, which is a third state.
	if strings.Contains(string(out), `icon: ""`) {
		t.Errorf("an undeclared icon was emitted as the empty string:\n%s", out)
	}

	second, err := ParseDocument(out)
	if err != nil {
		t.Fatalf("the document we emitted no longer parses: %v\n%s", err, out)
	}
	if second.Spec.Panels[0].Icon != IconMemory {
		t.Errorf("round-tripped icon = %q, want %q", second.Spec.Panels[0].Icon, IconMemory)
	}
	if second.Spec.Panels[1].Icon != "" {
		t.Errorf("round-tripped panel 2 icon = %q, want empty", second.Spec.Panels[1].Icon)
	}
}

func TestParseDocument_TrimsButDoesNotInventAnIcon(t *testing.T) {
	t.Parallel()

	// Quoted YAML can carry trailing space; what is VALIDATED must be exactly
	// what is STORED, or the client is handed a name the server never checked.
	doc, err := ParseDocument([]byte(strings.Replace(iconSpec, "icon: memory", `icon: "memory "`, 1)))
	if err != nil {
		t.Fatalf("a padded icon was refused: %v", err)
	}
	if got := doc.Spec.Panels[0].Icon; got != IconMemory {
		t.Errorf("icon = %q, want %q — the stored value must be the checked value", got, IconMemory)
	}
}

func TestPanelIconVocabulary_IsSmallAndInternallyConsistent(t *testing.T) {
	t.Parallel()

	// A list a human reads in one go. The cap is not arithmetic — it is the
	// point of the feature: a vocabulary nobody can hold in their head is one
	// an author picks from by grepping, which is the open-string failure with
	// extra steps.
	if len(PanelIcons) > 16 {
		t.Errorf("%d icons; the set is meant to be readable in one go", len(PanelIcons))
	}

	seen := map[PanelIcon]bool{}
	for _, i := range PanelIcons {
		if seen[i] {
			t.Errorf("icon %q is listed twice", i)
		}
		seen[i] = true
		if !i.Known() {
			t.Errorf("icon %q is in PanelIcons but Known() says otherwise", i)
		}
		if strings.TrimSpace(string(i)) != string(i) || strings.ToLower(string(i)) != string(i) {
			t.Errorf("icon %q is not a bare lowercase name; the client mirrors these verbatim", i)
		}
	}

	// The rendered list is what an author reads in a refusal, so it carries
	// every member and nothing else.
	list := PanelIconList()
	if got := strings.Count(list, ", ") + 1; got != len(PanelIcons) {
		t.Errorf("PanelIconList() names %d icons, want %d: %s", got, len(PanelIcons), list)
	}
}
