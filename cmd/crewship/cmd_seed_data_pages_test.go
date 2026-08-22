package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/pages"
	"gopkg.in/yaml.v3"
)

// Every demo payload must satisfy the schema its panel declares.
//
// This is the guard that keeps a broken seed from being discovered by whoever
// runs `dev.sh seed` and opens an empty page. The payloads are hand-written
// YAML against five JSON Schemas; nothing else in the build would notice a
// renamed key or a string where a number belongs, because the seeder learns
// about it only at runtime, from a 400 it prints to stderr and steps over.
func TestSeedPages_EveryDemoPayloadSatisfiesItsSchema(t *testing.T) {
	t.Parallel()

	if len(seeddata.Pages) == 0 {
		t.Fatal("no demo pages — the catalogue decoded to nothing")
	}
	for _, page := range seeddata.Pages {
		for _, panel := range page.Panels {
			// `routine` and `agent` are the two kinds the seeder cannot push to:
			// mayProduce (pages_authz.go) admits an owner push for `script` and
			// `webhook` and refuses every other kind. So they must NOT carry a
			// demo payload — one authored here would be marshalled, sent, and
			// answered with a 403 the seeder prints and steps over, leaving a
			// catalogue that looks like it fills a panel it cannot fill.
			//
			// The two are not filled the same way afterwards. A routine panel is
			// filled by firing its producer, which seedPageProducerRoutines does
			// and TestSeedPages_EveryRoutineProducedPanelIsWrittenByItsRoutine
			// checks. An AGENT panel is filled by that agent pushing from inside
			// its crew container, which no seed can make happen — so it opens on
			// never_produced, deliberately, and the page says so in its own
			// description.
			if kind, _ := seedPanelProducer(panel); kind == pages.ProducerRoutine || kind == pages.ProducerAgent {
				if panel.Demo != nil {
					t.Errorf("%s/%s is %s-produced but carries a demo payload — the seeder "+
						"pushes as the page owner and that push is refused, so this payload can "+
						"only ever become a 403", page.Slug, panel.ID, kind)
				}
				continue
			}
			if panel.Demo == nil {
				t.Errorf("%s/%s has no demo payload — a seeded panel with no data reads as "+
					"never_produced, which is how the app says nobody wired it up",
					page.Slug, panel.ID)
				continue
			}
			raw, err := json.Marshal(panel.Demo)
			if err != nil {
				t.Errorf("%s/%s: demo payload is not JSON-encodable: %v", page.Slug, panel.ID, err)
				continue
			}
			if _, err := pages.ValidatePayload(pages.PanelSchema(panel.Schema), raw); err != nil {
				t.Errorf("%s/%s: demo payload does not satisfy %s: %v",
					page.Slug, panel.ID, panel.Schema, err)
			}
		}
	}
}

// The spec half has to be valid too — an owner crew that the seed does not
// create, or an SLA the parser refuses, fails at seed time as a 400 nobody
// reads.
func TestSeedPages_EverySpecFieldIsWellFormed(t *testing.T) {
	t.Parallel()

	crews := map[string]bool{}
	for _, c := range seeddata.Crews {
		crews["crew/"+c.Slug] = true
	}
	routines := map[string]bool{}
	for _, r := range seeddata.Routines {
		routines[r.Slug] = true
	}
	agents := map[string]bool{}
	for _, a := range seeddata.Agents {
		agents[a.Slug] = true
	}

	for _, page := range seeddata.Pages {
		if page.Slug == "" || page.Name == "" {
			t.Errorf("page %+v has no slug or no name", page)
		}
		if len(page.Panels) == 0 {
			t.Errorf("page %s has no panels", page.Slug)
		}
		for _, panel := range page.Panels {
			if _, err := seedPageSLASeconds(panel.SLA); err != nil {
				t.Errorf("%s/%s: %v", page.Slug, panel.ID, err)
			}
			if !pages.PanelSchema(panel.Schema).Producible() {
				t.Errorf("%s/%s declares schema %q, which this build cannot produce",
					page.Slug, panel.ID, panel.Schema)
			}
			// The owner must be a crew the seed itself creates, or the page
			// create fails with an unresolvable reference.
			if !crews[panel.Owner] {
				t.Errorf("%s/%s is owned by %q, which crews.yaml does not create",
					page.Slug, panel.ID, panel.Owner)
			}
			// Every seeded panel has to have a path to holding data, and there
			// are exactly two. A script- or webhook-produced panel is filled by
			// the seeder's own push, which §7.1 rule 4 permits because the
			// seeder owns the page. A routine-produced panel is filled by its
			// producer, which seedPageProducerRoutines fires — so the routine it
			// names must be one the seed actually creates. Any other kind is a
			// panel the seed creates and nothing can write.
			kindStr, ref, ok := strings.Cut(strings.TrimSpace(panel.Producer), "/")
			if !ok {
				t.Errorf("%s/%s: producer %q is not <kind>/<ref>", page.Slug, panel.ID, panel.Producer)
				continue
			}
			switch kind := pages.ProducerKind(kindStr); kind {
			case pages.ProducerScript, pages.ProducerWebhook:
			case pages.ProducerRoutine:
				if !routines[ref] {
					t.Errorf("%s/%s names producer routine %q, which routines.go does not seed — "+
						"§7.1 rule 4 admits only the declared producer, so nothing could ever "+
						"write this panel", page.Slug, panel.ID, ref)
				}
			case pages.ProducerAgent:
				// An agent producer is never filled by the seed — it is filled
				// from inside that agent's crew container. What has to hold is
				// that the agent EXISTS: the server refuses a page naming one it
				// cannot resolve, so a typo here fails the whole page create and
				// takes the demo with it.
				if !agents[ref] {
					t.Errorf("%s/%s names producer agent %q, which agents.yaml does not seed — "+
						"the page create is refused outright for an unresolvable agent",
						page.Slug, panel.ID, ref)
				}
			default:
				t.Errorf("%s/%s is produced by a %s; the seed can fill only a script- or "+
					"webhook-produced panel (its own push) or a routine-produced one "+
					"(seedPageProducerRoutines), and may declare an agent-produced one that "+
					"fills from inside a container", page.Slug, panel.ID, kind)
			}
			if panel.Icon != "" && !pages.PanelIcon(panel.Icon).Known() {
				t.Errorf("%s/%s declares icon %q, which is outside the closed set",
					page.Slug, panel.ID, panel.Icon)
			}
		}
	}
}

// Every routine-produced panel must actually be written by the routine it
// names, with a payload its own schema accepts.
//
// This is the routine half of TestSeedPages_EveryDemoPayloadSatisfiesItsSchema,
// and it is needed for the same reason: nothing else in the build compares the
// two halves. A panel declaring `routine/page-watch` and a routine that writes
// `watch/wiring` are both individually valid — the page saves, the routine
// saves, the seed runs clean, and the panel sits on the never-produced em dash
// while the run reports a 403 in a journal nobody opened. The failure mode is
// silence, so the check has to be static.
//
// What it does NOT prove is that a rendered payload validates. `data` may carry
// `{{ … }}` templates (runner_crewship.go renders strings before dispatch), so
// what is checked here is the authored document — which is what catches a
// renamed key, a state outside the enum, or a number where the schema wants a
// string. A template that renders to something illegal is refused by the server
// at run time with the sentence naming the field.
func TestSeedPages_EveryRoutineProducedPanelIsWrittenByItsRoutine(t *testing.T) {
	t.Parallel()

	// (page slug, panel id) → the panel, for every routine-produced panel.
	type target struct{ page, panel string }
	want := map[target]seeddata.PagePanelDef{}
	for _, page := range seeddata.Pages {
		for _, panel := range page.Panels {
			if kind, _ := seedPanelProducer(panel); kind == pages.ProducerRoutine {
				want[target{page.Slug, panel.ID}] = panel
			}
		}
	}
	if len(want) == 0 {
		t.Skip("no routine-produced panels in the catalogue")
	}

	written := map[target]bool{}
	for _, routine := range seeddata.Routines {
		steps, ok := routine.Definition["steps"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, step := range steps {
			if s, _ := step["type"].(string); s != "crewship" {
				continue
			}
			if a, _ := step["action"].(string); a != "page.write" {
				continue
			}
			args, _ := step["args"].(map[string]interface{})
			pageSlug, _ := args["page"].(string)
			panelID, _ := args["panel"].(string)
			tgt := target{pageSlug, panelID}

			panel, declared := want[tgt]
			if !declared {
				t.Errorf("routine %s writes %s/%s, which the page catalogue does not declare "+
					"as routine-produced — the push will be refused by §7.1 rule 4",
					routine.Slug, pageSlug, panelID)
				continue
			}
			// The panel names a producer; this routine must BE it, or the push
			// is a 403 no matter how good the payload is.
			if _, ref := seedPanelProducer(panel); ref != routine.Slug {
				t.Errorf("routine %s writes %s/%s, but that panel names producer %q",
					routine.Slug, pageSlug, panelID, panel.Producer)
				continue
			}
			written[tgt] = true

			raw, err := json.Marshal(args["data"])
			if err != nil {
				t.Errorf("routine %s, %s/%s: data is not JSON-encodable: %v",
					routine.Slug, pageSlug, panelID, err)
				continue
			}
			if _, err := pages.ValidatePayload(pages.PanelSchema(panel.Schema), raw); err != nil {
				t.Errorf("routine %s, %s/%s: data does not satisfy %s: %v",
					routine.Slug, pageSlug, panelID, panel.Schema, err)
			}
		}
	}

	for tgt, panel := range want {
		if !written[tgt] {
			t.Errorf("%s/%s is produced by %s, but no page.write step in that routine targets it — "+
				"the panel would open on the never-produced em dash and stay there",
				tgt.page, tgt.panel, panel.Producer)
		}
	}
}

// A duration the seeder converts must round-trip to the integer the wire wants.
func TestSeedPageSLASeconds(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]int{"30s": 30, "60s": 60, "2m": 120, "1h": 3600, "12h": 43200} {
		got, err := seedPageSLASeconds(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "soon", "0s", "-5m"} {
		if _, err := seedPageSLASeconds(bad); err == nil {
			t.Errorf("%q was accepted as an SLA", bad)
		}
	}
	_ = time.Second
}

// pageProducerRoutineSlugs is what decides which routines the seeder fires, so
// its edge cases are the ones that decide whether a panel gets data at all.
func TestPageProducerRoutineSlugs(t *testing.T) {
	t.Parallel()

	panel := func(id, producer string) seeddata.PagePanelDef {
		return seeddata.PagePanelDef{ID: id, Producer: producer}
	}
	tests := []struct {
		name  string
		pages []seeddata.PageDef
		want  []string
	}{
		{
			name:  "no pages",
			pages: nil,
			want:  nil,
		},
		{
			name: "script and webhook producers are the seeder's own push",
			pages: []seeddata.PageDef{{Slug: "p", Panels: []seeddata.PagePanelDef{
				panel("a", "script/watch.sh"),
				panel("b", "webhook/inbound"),
			}}},
			want: nil,
		},
		{
			// The shape page-watch has: one routine, both panels. Firing it
			// twice would bill the same work twice and race two runs at one
			// page.
			name: "one routine writing several panels is fired once",
			pages: []seeddata.PageDef{{Slug: "p", Panels: []seeddata.PagePanelDef{
				panel("a", "routine/page-watch"),
				panel("b", "routine/page-watch"),
			}}},
			want: []string{"page-watch"},
		},
		{
			name: "dedup spans pages, and authored order is kept",
			pages: []seeddata.PageDef{
				{Slug: "p1", Panels: []seeddata.PagePanelDef{panel("a", "routine/beta")}},
				{Slug: "p2", Panels: []seeddata.PagePanelDef{
					panel("b", "routine/alpha"),
					panel("c", "routine/beta"),
				}},
			},
			want: []string{"beta", "alpha"},
		},
		{
			// A producer the seeder cannot act on must not become a run of a
			// routine called "" — the endpoint would 404 on a path with an
			// empty segment and the message would name nothing.
			name: "malformed and empty refs are skipped, not guessed at",
			pages: []seeddata.PageDef{{Slug: "p", Panels: []seeddata.PagePanelDef{
				panel("a", "routine"),
				panel("b", "routine/"),
				panel("c", "routine/   "),
				panel("d", ""),
			}}},
			want: nil,
		},
		{
			// An agent-produced panel is written from inside a container
			// through the sidecar; there is no run for the seeder to start.
			name: "agent producers are not the seeder's to fire",
			pages: []seeddata.PageDef{{Slug: "p", Panels: []seeddata.PagePanelDef{
				panel("a", "agent/riley"),
			}}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pageProducerRoutineSlugs(tt.pages)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// The seeder must reach the same endpoint the UI's Run button does, and must
// not abandon the remaining routines when one of them refuses to start.
func TestSeedPageProducerRoutines_FiresEachRoutineAndSurvivesAFailure(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()

	const ws = covWorkspaceIDCli10
	// Every routine the catalogue names, so the stub answers whatever the
	// real seed data asks for and the test does not have to restate it.
	slugs := pageProducerRoutineSlugs(seeddata.Pages)
	if len(slugs) == 0 {
		t.Skip("no routine-produced panels in the catalogue")
	}
	for i, slug := range slugs {
		path := "/api/v1/workspaces/" + ws + "/pipelines/" + slug + "/run"
		if i == 0 {
			// The first one refuses. A seed that stopped here would leave every
			// later page empty for a reason that has nothing to do with them.
			s.OnPost(path, clitest.JSONResponse(409, map[string]string{"error": "routine is not active"}))
			continue
		}
		s.OnPost(path, clitest.JSONResponse(202, map[string]string{"run_id": "r1"}))
	}

	client := cli.NewClient(s.URL(), "tok", ws)
	stderr, err := captureStderrCov(t, func() error {
		return seedPageProducerRoutines(context.Background(), client, ws)
	})
	if err != nil {
		t.Fatalf("seedPageProducerRoutines: %v", err)
	}

	for _, slug := range slugs {
		path := "/api/v1/workspaces/" + ws + "/pipelines/" + slug + "/run"
		if got := len(s.CallsFor("POST", path)); got != 1 {
			t.Errorf("routine %s: %d run POSTs, want exactly 1", slug, got)
		}
	}
	// The server's own sentence, not a shorter one this code invented.
	if !strings.Contains(stderr, "routine is not active") {
		t.Errorf("the refusal was not reported verbatim: %q", stderr)
	}
	if !strings.Contains(stderr, "1 failed") {
		t.Errorf("the failure was not counted: %q", stderr)
	}
}

// The whole seeded catalogue must survive the real spec parser.
//
// TestSeedPages_EverySpecFieldIsWellFormed checks the handful of fields the
// seeder itself depends on. This one is the opposite move: it rebuilds each
// seeded page as the document a human would author and runs ParseDocument over
// it, so every rule the parser owns applies here without being restated —
// notably the tab bar (name length, control characters, two names differing
// only in case, the cap of eight), the panel-count and span rules, and the slug
// vocabulary. A rule added to internal/pages later guards the seed for free.
//
// The `demo` payload is dropped in the conversion because the parser decodes
// with KnownFields(true) and a page document has no such key — the payloads are
// the seeder's, not the spec's, and they are judged against their schemas by
// TestSeedPages_EveryDemoPayloadSatisfiesItsSchema.
func TestSeedPages_EveryPageParsesAsAnAuthoredDocument(t *testing.T) {
	t.Parallel()

	for _, page := range seeddata.Pages {
		doc := pages.Document{
			APIVersion: pages.DocumentAPIVersion,
			Kind:       "Page",
			Metadata: pages.Metadata{
				Name:        page.Name,
				Slug:        page.Slug,
				Description: page.Description,
			},
		}
		for _, p := range page.Panels {
			spec := pages.PanelSpec{
				ID:       p.ID,
				Schema:   pages.PanelSchema(p.Schema),
				Title:    p.Title,
				Icon:     pages.PanelIcon(p.Icon),
				Tab:      p.Tab,
				Owner:    p.Owner,
				Producer: p.Producer,
				SLA:      p.SLA,
				Span:     p.Span,
				Public:   p.Public,
			}
			// The authored half has to travel too, and for a while it did not:
			// this loop copied the display fields only, so ValidateGates and
			// validatePageActions — the two validators this test exists to
			// reach — ran over panels that declared no gate and no action.
			// A malformed `when:`, an `agent:` that is not a crew, an unknown
			// action kind or a `for:` the parser refuses would all have passed
			// here in silence, which is the exact opposite of what the test's
			// name promises.
			//
			// Round-tripped through YAML rather than type-asserted: the
			// catalogue holds these as `any` on purpose (the server owns the
			// grammar), and re-decoding them into the real types is what turns
			// "some map the seeder will post" into "the struct the parser
			// judges".
			decodeAuthored(t, page.Slug, p.ID, "wake", p.Wake, &spec.Wake)
			decodeAuthored(t, page.Slug, p.ID, "on_failure", p.OnFailure, &spec.OnFailure)
			decodeAuthored(t, page.Slug, p.ID, "actions", p.Actions, &spec.Actions)
			doc.Spec.Panels = append(doc.Spec.Panels, spec)
		}
		raw, err := yaml.Marshal(doc)
		if err != nil {
			t.Errorf("page %s: cannot render as a document: %v", page.Slug, err)
			continue
		}
		if _, err := pages.ParseDocument(raw); err != nil {
			t.Errorf("page %s would be refused if a human authored it: %v", page.Slug, err)
		}
	}
}

// The tab bar the catalogue page renders, asserted as a whole.
//
// Tabs() is the one authority for bar order and membership, so reading it back
// is how this test avoids re-deriving either. What it pins is the editorial
// decision rather than the mechanism: the catalogue gives each payload shape
// its own screen, so a reader learning the format looks at exactly one at a
// time. A panel added without a `tab` would silently land on the first tab and
// put two shapes on one screen; this notices.
func TestSeedPages_CatalogueGivesEverySchemaItsOwnTab(t *testing.T) {
	t.Parallel()

	var panels []pages.PanelSpec
	var page seeddata.PageDef
	for _, p := range seeddata.Pages {
		if p.Slug == "operations" {
			page = p
			break
		}
	}
	if page.Slug == "" {
		t.Fatal("the catalogue page operations is gone — this test is about that page")
	}
	seen := map[string]bool{}
	for _, p := range page.Panels {
		panels = append(panels, pages.PanelSpec{ID: p.ID, Tab: p.Tab})
		if seen[p.Schema] {
			t.Errorf("panel %s repeats schema %s; the catalogue shows each shape once",
				p.ID, p.Schema)
		}
		seen[p.Schema] = true
	}

	tabs := pages.Tabs(panels)
	if len(tabs) != len(page.Panels) {
		t.Fatalf("%d tabs for %d panels — the catalogue is one panel per tab",
			len(tabs), len(page.Panels))
	}
	for _, tab := range tabs {
		if len(tab.PanelIDs) != 1 {
			t.Errorf("tab %q carries %d panels, want 1: %v", tab.Name, len(tab.PanelIDs), tab.PanelIDs)
		}
	}
}

// A page that already exists must be RE-APPLIED, not skipped.
//
// The 409 arm used to return nil, which made the seed silently useless against
// any workspace that had been seeded before: a panel added to the catalogue, a
// tab declared, a producer changed — none of it ever landed, and the seed
// printed the same success line either way. That is the failure mode this
// pins, because it is invisible from the outside.
func TestSeedOnePage_ExistingPageIsUpdatedRatherThanSkipped(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()

	const ws = covWorkspaceIDCli10
	// Already there: every create answers 409.
	s.OnPost("/api/v1/pages", clitest.JSONResponse(409, map[string]string{
		"error": `a page with slug "operations" already exists in this workspace`,
	}))
	for _, page := range seeddata.Pages {
		s.OnPatch("/api/v1/pages/"+page.Slug, clitest.JSONResponse(200, map[string]any{"slug": page.Slug}))
		for _, panel := range page.Panels {
			if panel.Demo == nil {
				continue
			}
			s.OnPut("/api/v1/pages/"+page.Slug+"/panels/"+panel.ID+"/data",
				clitest.JSONResponse(200, map[string]any{"accepted": true}))
		}
	}
	for _, slug := range pageProducerRoutineSlugs(seeddata.Pages) {
		s.OnPost("/api/v1/workspaces/"+ws+"/pipelines/"+slug+"/run",
			clitest.JSONResponse(202, map[string]string{"run_id": "r1"}))
	}

	client := cli.NewClient(s.URL(), "tok", ws)
	if _, err := captureStderrCov(t, func() error {
		return seedPages(context.Background(), client)
	}); err != nil {
		t.Fatalf("seedPages: %v", err)
	}

	for _, page := range seeddata.Pages {
		calls := s.CallsFor("PATCH", "/api/v1/pages/"+page.Slug)
		if len(calls) != 1 {
			t.Errorf("page %s: %d PATCH calls, want exactly 1", page.Slug, len(calls))
			continue
		}
		// Ownership is a create-time decision. Re-sending it here would let a
		// re-seed reassign a page somebody had since handed to another crew.
		var body map[string]any
		if err := json.Unmarshal(calls[0].Body, &body); err != nil {
			t.Errorf("page %s: PATCH body is not JSON: %v", page.Slug, err)
			continue
		}
		if _, ok := body["owner"]; ok {
			t.Errorf("page %s: the re-apply carried an owner (%v) — a re-seed must not transfer a page",
				page.Slug, body["owner"])
		}
		if _, ok := body["panels"]; !ok {
			t.Errorf("page %s: the re-apply carried no panels, so it would change nothing", page.Slug)
		}
	}
}

// The authored half has to reach the wire, and this is the test that would
// have caught it not doing so.
//
// seedOnePage builds the panel body field by field rather than marshalling the
// catalogue entry, which is the right shape — the wire is `sla_seconds`, the
// document is `sla` — but it means a field added to the YAML is silently
// dropped until somebody adds a line here too. A demo page whose gate never
// arrived would look completely correct: the page saves, the panels render,
// and the sensor half that PRD §0 calls the whole point of the feature simply
// does not exist in the workspace.
func TestSeedPages_TheAuthoredHalfReachesTheWire(t *testing.T) {
	t.Parallel()

	// Whatever the catalogue declares, the body must carry — so the assertion
	// is derived from the data rather than restated, and a gate added to a
	// second page is covered the day it is written.
	var checked int
	for _, page := range seeddata.Pages {
		for _, panel := range page.Panels {
			body, err := seedPagePanelBody(panel)
			if err != nil {
				t.Errorf("%s/%s: %v", page.Slug, panel.ID, err)
				continue
			}
			for _, f := range []struct {
				key      string
				declared bool
			}{
				{"wake", panel.Wake != nil},
				{"on_failure", panel.OnFailure != nil},
				{"actions", panel.Actions != nil},
				{"public", panel.Public},
			} {
				_, sent := body[f.key]
				if f.declared && !sent {
					t.Errorf("%s/%s declares %s and the seeder does not send it",
						page.Slug, panel.ID, f.key)
				}
				// The negative half matters as much: `wake: []` and no wake are
				// different documents, and posting an empty one would author a
				// gate nobody wrote.
				if !f.declared && sent {
					t.Errorf("%s/%s does not declare %s, but the seeder sends one",
						page.Slug, panel.ID, f.key)
				}
				if f.declared {
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no seeded panel declares a gate, an action or a public flag — " +
			"the demo shows only the display half of the feature")
	}
}

// decodeAuthored re-decodes one `any`-typed authored field into the typed field
// the parser validates.
//
// A decode failure is a test failure rather than a skip: the catalogue is YAML
// that came out of a YAML decoder, so anything that will not go back in is a
// shape the server could never have accepted either.
func decodeAuthored(t *testing.T, page, panel, field string, from any, into any) {
	t.Helper()
	if from == nil {
		return
	}
	raw, err := yaml.Marshal(from)
	if err != nil {
		t.Errorf("%s/%s: %s cannot be re-encoded: %v", page, panel, field, err)
		return
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		t.Errorf("%s/%s: %s is not the shape the parser expects: %v", page, panel, field, err)
	}
}
