package kinds

// Tests for ExportPage — the single-slug half of the export door
// `crewship export page <slug>` drives. ExportPages (page_test.go) covers
// the whole-workspace walk; what is different here is that a page which is
// not there is an ERROR rather than a skipped row, and that the fetch is one
// GET against the slug route rather than a list plus a filter.

import (
	"context"
	"strings"
	"testing"
)

func TestExportPage_BySlug(t *testing.T) {
	c := newPageFakeClient()
	c.GetResponses["/api/v1/pages/alpha"] = `{"id":"pg_a","slug":"alpha","name":"Alpha","description":"first",
		"panels":[{"id":"q","schema":"table.v1","owner":"crew/y","producer":"routine/r","sla_seconds":90,"span":6}]}`

	doc, err := ExportPage(context.Background(), c, "alpha")
	if err != nil {
		t.Fatalf("ExportPage: %v", err)
	}
	if doc.Metadata.Slug != "alpha" || doc.Metadata.Description != "first" {
		t.Errorf("metadata lost: %+v", doc.Metadata)
	}
	if doc.APIVersion != pageAPIVersion || doc.Kind != pageDocKind {
		t.Errorf("envelope missing: %+v", doc)
	}
	if got := doc.Spec.Panels[0].SLA; got != "90s" {
		t.Errorf("sla = %q, want 90s", got)
	}
	// One GET, against the slug route: the page's address is the slug, so
	// there is no reason to pull the index to find one page.
	if len(c.Calls) != 1 || c.Calls[0].Path != "/api/v1/pages/alpha" {
		t.Errorf("calls = %+v, want a single GET on the slug route", c.Calls)
	}
}

// TestExportPage_MissingIsAnError is the difference from ExportPages, which
// SKIPS a page that vanished mid-walk. Exporting a named page and printing
// nothing would truncate whatever the operator redirected the output into.
func TestExportPage_MissingIsAnError(t *testing.T) {
	c := newPageFakeClient() // every GET 404s
	if _, err := ExportPage(context.Background(), c, "ghost"); err == nil {
		t.Fatal("exporting a page that does not exist must fail")
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the slug: %v", err)
	}
}

// TestExportPage_SealedPanelRefuses mirrors TestExportPages_SealedPanelRefuses
// for the single-page path: the same document would silently DELETE a panel
// the exporter could not see.
func TestExportPage_SealedPanelRefuses(t *testing.T) {
	c := newPageFakeClient()
	c.GetResponses["/api/v1/pages/fleet"] = `{"id":"pg","slug":"fleet","name":"Fleet",
		"panels":[{"panel_id":"secret","span":6,"sealed":true,"owner_crew_name":"Devops"}]}`
	_, err := ExportPage(context.Background(), c, "fleet")
	if err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("want a sealed-panel refusal, got %v", err)
	}
}
