package pages

import (
	"strings"
	"testing"
)

// `refresh:` is a TRIGGER, not a hint (PRD §12 v1.1, §5). These tests pin the
// four things the server has to refuse, because each of them is a declaration
// that would otherwise be stored, believed and never act.

// prdRefreshExample is the PRD's own worked example (§6, docs/prd/pages.md:422)
// at the v1.1 feature level: a status panel whose gate wakes devops, and a
// narrative panel produced by a routine that runs when it does.
const prdRefreshExample = `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      span: 8
      wake:
        - when: any(state == "critical")
          agent: crew/devops
          writes: incident
    - id: incident
      schema: narrative.v1
      owner: crew/devops
      producer: routine/incident-rozbor
      refresh: on:wake
      sla: 1h
      span: 12
`

func TestRefresh_AcceptsThePRDExample(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(prdRefreshExample))
	if err != nil {
		t.Fatalf("the §6 worked example was refused: %v", err)
	}
	triggers := RefreshTriggers(doc)
	if len(triggers) != 1 {
		t.Fatalf("got %d refresh triggers, want 1: %+v", len(triggers), triggers)
	}
	got := triggers[0]
	if got.PanelID != "incident" || got.On != RefreshOnWake || got.RoutineSlug != "incident-rozbor" {
		t.Errorf("trigger = %+v, want incident / on:wake / incident-rozbor", got)
	}
}

func TestRefresh_VocabularyIsClosed(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "on:push")
	err := doc.Validate()
	if err == nil {
		t.Fatal("refresh: on:push was accepted; the set is closed")
	}
	// A closed set whose refusal does not name its members is a set the author
	// has to go and look up.
	for _, want := range []string{"on:wake", "on:panels-changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

func TestRefresh_RefusesABlankDeclaration(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "   ")
	if err := doc.Validate(); err == nil {
		t.Fatal("a whitespace-only refresh was accepted; it would be stored and never fire")
	}
}

// A script producer is the case the PRD's own example puts on the OTHER panel:
// Crewship cannot run somebody's shell script, so a refresh declared on one
// would be a trigger with nothing to pull.
func TestRefresh_RefusesAProducerTheServerCannotRun(t *testing.T) {
	t.Parallel()

	for _, producer := range []string{"script/watch-services.sh", "webhook/inbound", "agent/analyst"} {
		doc := refreshDoc(t, "on:wake")
		doc.Spec.Panels[1].Producer = producer
		err := doc.Validate()
		if err == nil {
			t.Fatalf("refresh on a %s producer was accepted; nothing can run it", producer)
		}
		if !strings.Contains(err.Error(), "routine/") {
			t.Errorf("refusal for %s does not say what a refreshable producer is: %v", producer, err)
		}
	}
}

func TestRefresh_OnWakeRefusesAPageWithNoGate(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "on:wake")
	doc.Spec.Panels[0].Wake = nil
	err := doc.Validate()
	if err == nil {
		t.Fatal("refresh: on:wake was accepted on a page with no wake gate; it could never fire")
	}
	if !strings.Contains(err.Error(), "wake") {
		t.Errorf("refusal does not name the missing gate: %v", err)
	}
}

// The loop the brief names: a producer that writes a panel whose refresh
// re-triggers it. A panel that is both the sensor and the thing the sensor
// refreshes is that loop, and it is statically visible.
func TestRefresh_OnWakeRefusesAPanelThatIsItsOwnSensor(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "on:wake")
	doc.Spec.Panels[1].Schema = SchemaStatus
	doc.Spec.Panels[1].Wake = []PanelWake{{
		When:  `any(state == "critical")`,
		Agent: "crew/devops",
	}}
	err := doc.Validate()
	if err == nil {
		t.Fatal("a panel that refreshes on its own wake gate was accepted; that is the loop")
	}
	if !strings.Contains(err.Error(), "loop") && !strings.Contains(err.Error(), "own") {
		t.Errorf("refusal does not explain the cycle: %v", err)
	}
}

// on:panels-changed is armed by an edit, not by a gate, so it needs no wake
// gate anywhere — and refusing it for the absence of one would be wrong.
func TestRefresh_OnPanelsChangedNeedsNoGate(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "on:panels-changed")
	doc.Spec.Panels[0].Wake = nil
	if err := doc.Validate(); err != nil {
		t.Fatalf("refresh: on:panels-changed was refused on a gateless page: %v", err)
	}
}

// A panel may declare its own gate AND refresh on an edit: an edit is not a
// push, so that arrangement has no cycle in it.
func TestRefresh_OnPanelsChangedAllowsAPanelWithItsOwnGate(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "on:panels-changed")
	doc.Spec.Panels[1].Schema = SchemaStatus
	doc.Spec.Panels[1].Wake = []PanelWake{{
		When:  `any(state == "critical")`,
		Agent: "crew/devops",
	}}
	if err := doc.Validate(); err != nil {
		t.Fatalf("a gated panel refreshing on an edit was refused: %v", err)
	}
}

func TestRefresh_IsNormalisedBeforeItIsChecked(t *testing.T) {
	t.Parallel()

	doc := refreshDoc(t, "  on:wake\t")
	if err := doc.Validate(); err != nil {
		t.Fatalf("a padded refresh was refused: %v", err)
	}
	if got := doc.Spec.Panels[1].Refresh; got != RefreshOnWake {
		t.Errorf("stored refresh = %q, want %q — what validates must be what is stored", got, RefreshOnWake)
	}
}

// KnownFields(true) means a v1 parser refuses `refresh:` outright, so the
// authoring door and the manifest door have to agree it is a field now.
func TestRefresh_ParsesFromYAML(t *testing.T) {
	t.Parallel()

	if _, err := ParseDocument([]byte(prdRefreshExample)); err != nil {
		t.Fatalf("`refresh:` is not a known field on the YAML door: %v", err)
	}
}

func TestRefresh_TriggersAreEmptyWhenNobodyDeclaresOne(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(validSpec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := RefreshTriggers(doc); len(got) != 0 {
		t.Errorf("got %d triggers on a page that declares none: %+v", len(got), got)
	}
}

// refreshDoc builds the PRD example with the second panel's `refresh:` set to
// whatever the case under test needs. It returns the PARSED-equivalent
// document rather than YAML so a test can set a value ParseDocument's own
// decoder would reject for shape reasons.
func refreshDoc(t *testing.T, refresh string) *Document {
	t.Helper()
	return &Document{
		APIVersion: DocumentAPIVersion,
		Kind:       DocumentKind,
		Metadata:   Metadata{Name: "Flotila .201", Slug: "fleet-201"},
		Spec: Spec{Panels: []PanelSpec{
			{
				ID: "sluzby", Schema: SchemaStatus, Owner: "crew/lookout",
				Producer: "script/watch-services.sh", SLA: "30s", Span: 8,
				Wake: []PanelWake{{
					When:   `any(state == "critical")`,
					Agent:  "crew/devops",
					Writes: "incident",
				}},
			},
			{
				ID: "incident", Schema: SchemaNarrative, Owner: "crew/devops",
				Producer: "routine/incident-rozbor", SLA: "1h", Span: 12,
				Refresh: PanelRefresh(refresh),
			},
		}},
	}
}
