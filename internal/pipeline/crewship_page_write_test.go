package pipeline

import (
	"strings"
	"testing"
)

// #1945 — `page.write` is the verb the Pages feature turns on: a routine
// writing its analysis back onto the page a script pushed to
// (docs/prd/pages.md §0). Everything below is a SAVE-time property, which is
// where a routine author learns things; the run-time half lives in
// internal/api/pages_internal_test.go.

// The verb saves, and every one of its required args is required. One table
// because the two claims are the same claim seen from both sides: a registry
// entry that accepts a push with no payload is a routine that saves clean and
// 400s at 03:00, which is the failure this registry exists to prevent.
func TestValidate_CrewshipPageWriteArgs(t *testing.T) {
	full := map[string]any{
		"page":  "flotila-201",
		"panel": "sluzby",
		"data":  map[string]any{"value": 42},
	}
	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr string // "" = must save
	}{
		{"complete", full, ""},
		{"no page", map[string]any{"panel": "sluzby", "data": map[string]any{"value": 1}}, "page"},
		{"no panel", map[string]any{"page": "flotila-201", "data": map[string]any{"value": 1}}, "panel"},
		{"no data", map[string]any{"page": "flotila-201", "panel": "sluzby"}, "data"},
		{"empty page", map[string]any{"page": " ", "panel": "sluzby", "data": map[string]any{"value": 1}}, "page"},
		// state is optional: absent means "ok", because a producer that ran and
		// said nothing about itself worked (§4 rule 2).
		{"no state", full, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(crewshipDSL("page.write", tc.args), nil, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("page.write with complete args must save: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a save-time refusal naming %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the error must name the missing arg %q, got %q", tc.wantErr, err)
			}
		})
	}
}

// The verb is GOVERNED, which is the whole of #1945: without a policy action it
// is refused at save with ErrCrewshipVerbUngoverned and no routine can ever
// write a panel. The action string is asserted literally because it is a plain
// string on this side of the seam (pipeline must not import policy), so a typo
// compiles — internal/api's TestCrewshipVerbs_EveryPolicyActionIsDeclared is
// the other half, and it is the one that would notice.
func TestCrewshipVerbs_PageWriteIsGoverned(t *testing.T) {
	if got := CrewshipVerbPolicyAction("page.write"); got != "page_write" {
		t.Fatalf("page.write is gated on %q, want %q", got, "page_write")
	}
	var listed bool
	for _, v := range EnabledCrewshipVerbs() {
		if v == "page.write" {
			listed = true
		}
	}
	if !listed {
		t.Error("page.write is not in EnabledCrewshipVerbs — the author-facing list of what can be saved")
	}
}

// The route, pinned. One placeholder, not two: crewshipRoutePath fills exactly
// one {arg}, so a template naming the panel in the path as well would render
// `/panels//data` and 404 in a way nobody could read. That is why `panel` is a
// required ARG rather than a path segment, and the two facts have to be checked
// together or the next person "fixes" one of them.
func TestCrewshipVerbs_PageWriteRoute(t *testing.T) {
	method, path, ok := CrewshipVerbRoute("page.write")
	if !ok {
		t.Fatal("page.write has no route")
	}
	if method != "PUT" {
		t.Errorf("method = %q, want PUT (the public panel-data route's verb)", method)
	}
	if path != "/api/v1/internal/pages/{page}/data" {
		t.Errorf("path = %q, want /api/v1/internal/pages/{page}/data", path)
	}
	if strings.Count(path, "{") != 1 {
		t.Errorf("path %q has %d placeholders; the dispatcher fills exactly one",
			path, strings.Count(path, "{"))
	}
	var requiresPanel bool
	for _, a := range crewshipVerbs["page.write"].RequiredArgs {
		if a == "panel" {
			requiresPanel = true
		}
	}
	if !requiresPanel {
		t.Error("the panel travels in the body, so it must be a required arg — " +
			"without it the route cannot tell which panel to write")
	}
}

// A routine with NO author agent must be able to write its own panel. The
// acting-agent gate refuses verbs whose route has no no-agent fallback
// (issue.comment); this one has a stronger identity than an agent — the RUN,
// which resolves to the routine whose slug the panel names as its producer
// (§7.1 rule 4). Requiring an agent here would make the PRD's own example — a
// cheap script's routine keeping a panel fresh — unsaveable.
func TestValidate_CrewshipPageWriteNeedsNoActingAgent(t *testing.T) {
	if crewshipVerbs["page.write"].RequiresActingAgent {
		t.Fatal("page.write must not require an acting agent — the run is the identity")
	}
	dsl := crewshipDSL("page.write", map[string]any{
		"page": "flotila-201", "panel": "sluzby", "data": map[string]any{"value": 1},
	})
	if err := ValidateCrewshipActingAgent(dsl, false); err != nil {
		t.Fatalf("a routine with no author agent must still be able to write its own panel: %v", err)
	}
	// …and it is offered as the remedy when a verb that DOES need one is used.
	var offered bool
	for _, v := range crewshipVerbsNotNeedingActingAgent() {
		if v == "page.write" {
			offered = true
		}
	}
	if !offered {
		t.Error("page.write should appear in the 'verbs that can act unattended' remedy list")
	}
}
