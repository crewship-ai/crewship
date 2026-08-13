package kinds

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/crewship-ai/crewship/internal/pages"
	"gopkg.in/yaml.v3"
)

// ── Test client ────────────────────────────────────────────────────────────

// pageFakeClient is the in-memory internalapi.Client stub for the Page
// tests. Same shape as savedViewFakeClient, prefixed per the package
// convention (kinds is one flat package).
type pageFakeClient struct {
	GetResponses map[string]string
	GetStatus    map[string]int
	Calls        []pageFakeCall
	PostStatus   int
	PatchStatus  int
}

type pageFakeCall struct {
	Method string
	Path   string
	Body   any
}

func newPageFakeClient() *pageFakeClient {
	return &pageFakeClient{
		GetResponses: map[string]string{},
		GetStatus:    map[string]int{},
		PostStatus:   201,
		PatchStatus:  200,
	}
}

func (f *pageFakeClient) WorkspaceID() string { return "ws_test" }

func (f *pageFakeClient) record(method, path string, body any) {
	f.Calls = append(f.Calls, pageFakeCall{Method: method, Path: path, Body: body})
}

func (f *pageFakeClient) respond(status int, body string) *internalapi.Response {
	return &internalapi.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func (f *pageFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	if status, ok := f.GetStatus[path]; ok {
		return f.respond(status, f.GetResponses[path]), nil
	}
	if body, ok := f.GetResponses[path]; ok {
		return f.respond(200, body), nil
	}
	return f.respond(404, `{"error":"page not found"}`), nil
}

func (f *pageFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	return f.respond(f.PostStatus, `{"id":"pg_new"}`), nil
}

func (f *pageFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.respond(f.PatchStatus, `{}`), nil
}

func (f *pageFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.respond(200, `{}`), nil
}

func (f *pageFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return f.respond(204, ``), nil
}

// ── Fixtures ───────────────────────────────────────────────────────────────

// pageTestDoc is a minimal valid Page document: one status panel owned
// by crew/lookout and produced by routine/watch.
func pageTestDoc() *PageDocument {
	return &PageDocument{
		APIVersion: pageAPIVersion,
		Kind:       pageDocKind,
		Metadata: internalapi.Metadata{
			Name: "Fleet 201",
			Slug: "fleet-201",
		},
		Spec: PageSpec{Panels: []pages.PanelSpec{{
			ID:       "services",
			Schema:   pages.SchemaStatus,
			Title:    "Jede to?",
			Owner:    "crew/lookout",
			Producer: "routine/watch",
			SLA:      "30s",
			Span:     8,
		}}},
	}
}

// pageTestCtx knows about the crew, agent and routine the fixture names.
func pageTestCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews:    []internalapi.SlugLookup{{Slug: "lookout"}, {Slug: "devops"}},
		DeclaredAgents:   []internalapi.SlugLookup{{Slug: "herald"}},
		DeclaredRoutines: []internalapi.SlugLookup{{Slug: "watch"}},
	}
}

// ── Validate ───────────────────────────────────────────────────────────────

func TestPageDocument_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(d *PageDocument)
		ctx     internalapi.WorkspaceContext
		wantErr string
	}{
		{name: "valid", mutate: func(*PageDocument) {}, ctx: pageTestCtx()},
		{
			name:    "wrong apiVersion",
			mutate:  func(d *PageDocument) { d.APIVersion = "crewship/v9" },
			ctx:     pageTestCtx(),
			wantErr: "unsupported apiVersion",
		},
		{
			name:    "wrong kind",
			mutate:  func(d *PageDocument) { d.Kind = "Pages" },
			ctx:     pageTestCtx(),
			wantErr: `kind must be "Page"`,
		},
		{
			name:    "missing slug",
			mutate:  func(d *PageDocument) { d.Metadata.Slug = "" },
			ctx:     pageTestCtx(),
			wantErr: "is not a slug",
		},
		{
			name:    "missing name",
			mutate:  func(d *PageDocument) { d.Metadata.Name = "" },
			ctx:     pageTestCtx(),
			wantErr: "metadata.name is required",
		},
		{
			name:    "no panels",
			mutate:  func(d *PageDocument) { d.Spec.Panels = nil },
			ctx:     pageTestCtx(),
			wantErr: "at least one panel",
		},
		{
			name: "duplicate panel id",
			mutate: func(d *PageDocument) {
				d.Spec.Panels = append(d.Spec.Panels, d.Spec.Panels[0])
			},
			ctx:     pageTestCtx(),
			wantErr: "appears twice",
		},
		{
			name:    "unknown schema",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Schema = "gauge.v1" },
			ctx:     pageTestCtx(),
			wantErr: "unknown schema",
		},
		{
			name:    "owner must be a crew",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Owner = "user/pavel" },
			ctx:     pageTestCtx(),
			wantErr: "must be crew/<slug>",
		},
		{
			name:    "producer kind is closed",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Producer = "sql/select-1" },
			ctx:     pageTestCtx(),
			wantErr: "is not one of routine, script, agent, webhook",
		},
		{
			name:    "sla required",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].SLA = "" },
			ctx:     pageTestCtx(),
			wantErr: "is not a duration",
		},
		{
			name:    "zero sla refused",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].SLA = "0s" },
			ctx:     pageTestCtx(),
			wantErr: "sla",
		},
		{
			name:    "span off the grid",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Span = 13 },
			ctx:     pageTestCtx(),
			wantErr: "the grid has 12 columns",
		},
		{
			name:    "unknown owner crew",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Owner = "crew/ghost" },
			ctx:     pageTestCtx(),
			wantErr: "owner crew/ghost does not reference any declared or remote crew",
		},
		{
			name:    "unknown producer routine",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Producer = "routine/ghost" },
			ctx:     pageTestCtx(),
			wantErr: "producer routine/ghost does not reference any declared or remote routine",
		},
		{
			name:    "unknown producer agent",
			mutate:  func(d *PageDocument) { d.Spec.Panels[0].Producer = "agent/ghost" },
			ctx:     pageTestCtx(),
			wantErr: "producer agent/ghost does not reference any declared or remote agent",
		},
		{
			name:   "script producer is not resolvable and is not checked",
			mutate: func(d *PageDocument) { d.Spec.Panels[0].Producer = "script/watch-services.sh" },
			ctx:    pageTestCtx(),
		},
		{
			name:   "webhook producer is not resolvable and is not checked",
			mutate: func(d *PageDocument) { d.Spec.Panels[0].Producer = "webhook/vendor" },
			ctx:    pageTestCtx(),
		},
		{
			// An empty context cannot prove anything is missing — a
			// page-only manifest applied against a live workspace must
			// not fail because this process never fetched its crews.
			name:   "empty context skips FK checks",
			mutate: func(d *PageDocument) { d.Spec.Panels[0].Owner = "crew/ghost" },
			ctx:    internalapi.WorkspaceContext{},
		},
		{
			name: "every bad reference is reported, not just the first",
			mutate: func(d *PageDocument) {
				d.Spec.Panels[0].Owner = "crew/ghost"
				d.Spec.Panels = append(d.Spec.Panels, pages.PanelSpec{
					ID: "second", Schema: pages.SchemaMetric, Owner: "crew/nowhere",
					Producer: "routine/watch", SLA: "1h",
				})
			},
			ctx:     pageTestCtx(),
			wantErr: "crew/nowhere",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := pageTestDoc()
			tc.mutate(d)
			err := d.Validate(tc.ctx)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate: want error containing %q, got %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), "page ") {
				t.Errorf("error should name the kind and slug: %v", err)
			}
		})
	}
}

// TestPageDocument_Validate_RejectsReservedSchema pins the DELEGATION,
// not the membership: which names are producible is internal/pages'
// list to change (it moves as renderers land), so the case picks
// whichever schema is known-but-not-yet-producible at run time and
// skips if the set is momentarily empty. Asserting on a specific
// reserved schema here would break this package every time the Pages
// team shipped a panel.
func TestPageDocument_Validate_RejectsReservedSchema(t *testing.T) {
	var reserved pages.PanelSchema
	for _, s := range []pages.PanelSchema{
		pages.SchemaEmbed, pages.SchemaSeries, pages.SchemaNarrative,
		pages.SchemaTable, pages.SchemaStatus, pages.SchemaMetric,
	} {
		if s.Known() && !s.Producible() {
			reserved = s
			break
		}
	}
	if reserved == "" {
		t.Skip("every known schema is producible today; nothing reserved to refuse")
	}
	d := pageTestDoc()
	d.Spec.Panels[0].Schema = reserved
	err := d.Validate(pageTestCtx())
	if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("want a reserved-schema refusal for %s, got %v", reserved, err)
	}
}

// TestPageDocument_Validate_ReportsBothBadReferencesOnOnePanel pins the
// aggregation: a manifest with three bad references reports three.
func TestPageDocument_Validate_ReportsEveryBadReference(t *testing.T) {
	d := pageTestDoc()
	d.Spec.Panels[0].Owner = "crew/ghost"
	d.Spec.Panels = append(d.Spec.Panels, pages.PanelSpec{
		ID: "second", Schema: pages.SchemaMetric, Owner: "crew/lookout",
		Producer: "routine/nope", SLA: "1h",
	})
	err := d.Validate(pageTestCtx())
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"crew/ghost", "routine/nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestPageDocument_Validate_LeavesSpanAlone is the regression guard for
// the copy in pagesDocument: pages.Document.Validate defaults span in
// place, and if it did so on the manifest's own slice then Plan would
// report drift differently depending on whether Validate ran first.
func TestPageDocument_Validate_LeavesSpanAlone(t *testing.T) {
	d := pageTestDoc()
	d.Spec.Panels[0].Span = 0
	if err := d.Validate(pageTestCtx()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if d.Spec.Panels[0].Span != 0 {
		t.Fatalf("Validate mutated the declared span: got %d, want it untouched", d.Spec.Panels[0].Span)
	}
	if got := pageSpanOrDefault(d.Spec.Panels[0].Span); got != pages.DefaultSpan {
		t.Errorf("pageSpanOrDefault = %d, want %d", got, pages.DefaultSpan)
	}
}

// TestPageDocument_YAMLRoundTrip pins the promise the kind is built on:
// the manifest document and the `crewship page create --file` document
// are the same YAML, sla included ("30s", not 30).
func TestPageDocument_YAMLRoundTrip(t *testing.T) {
	src := `
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
`
	var doc PageDocument
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if doc.Kind != pageDocKind || doc.Metadata.Slug != "fleet-201" {
		t.Fatalf("envelope not decoded: %+v", doc)
	}
	if len(doc.Spec.Panels) != 1 {
		t.Fatalf("panels = %d, want 1", len(doc.Spec.Panels))
	}
	p := doc.Spec.Panels[0]
	if p.ID != "sluzby" || p.Schema != pages.SchemaStatus || p.SLA != "30s" || p.Span != 8 {
		t.Errorf("panel decoded wrong: %+v", p)
	}
	// The same bytes must parse through the CLI's door too.
	if _, err := pages.ParseDocument([]byte(src)); err != nil {
		t.Errorf("the same document must parse via pages.ParseDocument: %v", err)
	}
}

// ── Plan ───────────────────────────────────────────────────────────────────

func TestPageDocument_Plan_Create(t *testing.T) {
	d := pageTestDoc()
	c := newPageFakeClient()

	items, err := d.Plan(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want one create item, got %+v", items)
	}
	if items[0].Kind != pagePlanKind {
		t.Errorf("PlanItem.Kind = %q, want %q", items[0].Kind, pagePlanKind)
	}
	if err := items[0].Exec(context.Background(), c); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := c.Calls[len(c.Calls)-1]
	if call.Method != "POST" || call.Path != "/api/v1/pages" {
		t.Fatalf("want POST /api/v1/pages, got %s %s", call.Method, call.Path)
	}
	body, _ := call.Body.(map[string]any)
	if body["slug"] != "fleet-201" || body["name"] != "Fleet 201" {
		t.Errorf("identity missing from body: %+v", body)
	}
	panels, _ := body["panels"].([]map[string]any)
	if len(panels) != 1 {
		t.Fatalf("panels = %+v", body["panels"])
	}
	// §11b decision 3: sla crosses the wire as an integer of seconds.
	if panels[0]["sla_seconds"] != 30 {
		t.Errorf("sla_seconds = %v, want 30", panels[0]["sla_seconds"])
	}
	if panels[0]["span"] != 8 {
		t.Errorf("span = %v, want 8", panels[0]["span"])
	}
	if _, present := panels[0]["public"]; present {
		t.Errorf("public must be omitted when false, got %+v", panels[0])
	}
}

func TestPageDocument_Plan_CreateDefaultsSpan(t *testing.T) {
	d := pageTestDoc()
	d.Spec.Panels[0].Span = 0
	items, err := d.Plan(context.Background(), newPageFakeClient(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c := newPageFakeClient()
	if err := items[0].Exec(context.Background(), c); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	body := c.Calls[0].Body.(map[string]any)
	panels := body["panels"].([]map[string]any)
	if panels[0]["span"] != pages.DefaultSpan {
		t.Errorf("span = %v, want the %d-column default", panels[0]["span"], pages.DefaultSpan)
	}
}

func pageRemoteMatching(d *PageDocument) *PageRemote {
	return &PageRemote{
		ID:    "pg_1",
		Slug:  d.Metadata.Slug,
		Name:  d.Metadata.Name,
		Owner: "user/u1",
		Panels: []PagePanelRemote{{
			ID:         "services",
			Schema:     "status.v1",
			Title:      "Jede to?",
			Owner:      "crew/lookout",
			Producer:   "routine/watch",
			SLASeconds: 30,
			Span:       8,
		}},
	}
}

func TestPageDocument_Plan_Unchanged(t *testing.T) {
	d := pageTestDoc()
	items, err := d.Plan(context.Background(), newPageFakeClient(), pageRemoteMatching(d))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("want unchanged, got %+v", items)
	}
	if items[0].Exec != nil {
		t.Error("an unchanged item must carry no Exec")
	}
}

func TestPageDocument_Plan_Update(t *testing.T) {
	d := pageTestDoc()
	remote := pageRemoteMatching(d)
	remote.Name = "Old name"

	items, err := d.Plan(context.Background(), newPageFakeClient(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want update, got %+v", items)
	}
	if !strings.Contains(items[0].Description, "name") {
		t.Errorf("description should name what drifted: %q", items[0].Description)
	}
	c := newPageFakeClient()
	if err := items[0].Exec(context.Background(), c); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := c.Calls[0]
	if call.Method != "PATCH" || call.Path != "/api/v1/pages/fleet-201" {
		t.Fatalf("want PATCH /api/v1/pages/fleet-201, got %s %s", call.Method, call.Path)
	}
	body := call.Body.(map[string]any)
	// The server refuses a PATCH that changes the slug ("a page's slug
	// is its address"), so sending it can only turn a fix into a 400.
	if _, present := body["slug"]; present {
		t.Errorf("update body must not carry slug: %+v", body)
	}
	if body["name"] != "Fleet 201" {
		t.Errorf("name not converged: %+v", body)
	}
	if _, present := body["panels"]; !present {
		t.Error("update must send the whole panel list: PATCH replaces it wholesale")
	}
}

func TestPageDocument_Plan_UpdateSurfacesServerError(t *testing.T) {
	d := pageTestDoc()
	remote := pageRemoteMatching(d)
	remote.Name = "Old name"
	items, err := d.Plan(context.Background(), newPageFakeClient(), remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	c := newPageFakeClient()
	c.PatchStatus = 422
	if err := items[0].Exec(context.Background(), c); err == nil {
		t.Fatal("a non-2xx PATCH must fail the apply, not be reported as converged")
	}
}

func TestPagePanelsDiffer(t *testing.T) {
	base := pageTestDoc().Spec.Panels
	remote := pageRemoteMatching(pageTestDoc()).Panels

	cases := []struct {
		name     string
		declared []pages.PanelSpec
		remote   []PagePanelRemote
		want     bool
	}{
		{name: "identical", declared: base, remote: remote, want: false},
		{
			name:     "count differs",
			declared: base,
			remote:   nil,
			want:     true,
		},
		{
			name: "order is the layout",
			declared: []pages.PanelSpec{
				{ID: "b", Schema: pages.SchemaMetric, Owner: "crew/x", Producer: "script/s", SLA: "1m", Span: 6},
				{ID: "a", Schema: pages.SchemaMetric, Owner: "crew/x", Producer: "script/s", SLA: "1m", Span: 6},
			},
			remote: []PagePanelRemote{
				{ID: "a", Schema: "metric.v1", Owner: "crew/x", Producer: "script/s", SLASeconds: 60, Span: 6},
				{ID: "b", Schema: "metric.v1", Owner: "crew/x", Producer: "script/s", SLASeconds: 60, Span: 6},
			},
			want: true,
		},
		{
			name:     "sla drift",
			declared: base,
			remote: []PagePanelRemote{{
				ID: "services", Schema: "status.v1", Title: "Jede to?", Owner: "crew/lookout",
				Producer: "routine/watch", SLASeconds: 60, Span: 8,
			}},
			want: true,
		},
		{
			name:     "producer drift",
			declared: base,
			remote: []PagePanelRemote{{
				ID: "services", Schema: "status.v1", Title: "Jede to?", Owner: "crew/lookout",
				Producer: "routine/other", SLASeconds: 30, Span: 8,
			}},
			want: true,
		},
		{
			// A sealed placeholder carries only {panel_id, span,
			// sealed, owner_crew_name}. Comparing what is there and
			// no more is what keeps a re-apply from minting a
			// page_versions row on every run.
			name:     "sealed panel compares on id and span only",
			declared: base,
			remote: []PagePanelRemote{{
				PanelID: "services", Sealed: true, Span: 8, OwnerCrewName: "Lookout",
			}},
			want: false,
		},
		{
			name:     "sealed panel with a different span is drift",
			declared: base,
			remote: []PagePanelRemote{{
				PanelID: "services", Sealed: true, Span: 12, OwnerCrewName: "Lookout",
			}},
			want: true,
		},
		{
			name:     "sealed panel under a different id is drift",
			declared: base,
			remote: []PagePanelRemote{{
				PanelID: "other", Sealed: true, Span: 8, OwnerCrewName: "Lookout",
			}},
			want: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := pagePanelsDiffer(tc.declared, tc.remote); got != tc.want {
				t.Fatalf("pagePanelsDiffer = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPagePanelsDiffer_PublicIsNotDiffed pins the deliberate blind spot:
// the read path does not serialise `public`, so diffing it would report
// drift forever on any page that declares a public panel.
func TestPagePanelsDiffer_PublicIsNotDiffed(t *testing.T) {
	declared := pageTestDoc().Spec.Panels
	declared[0].Public = true
	if pagePanelsDiffer(declared, pageRemoteMatching(pageTestDoc()).Panels) {
		t.Fatal("public must not drive drift: the server never sends it back")
	}
}

// ── Lookup ─────────────────────────────────────────────────────────────────

func TestLookupPageRemoteBySlug(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		c := newPageFakeClient()
		c.GetResponses["/api/v1/pages/fleet-201"] = `{
			"id":"pg_1","slug":"fleet-201","name":"Fleet 201","owner":"crew/lookout",
			"panels":[{"id":"services","schema":"status.v1","owner":"crew/lookout",
			           "producer":"routine/watch","sla_seconds":30,"span":8,"state":"fresh"}]}`
		remote, err := LookupPageRemoteBySlug(context.Background(), c, "fleet-201")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if remote == nil || remote.Slug != "fleet-201" || len(remote.Panels) != 1 {
			t.Fatalf("remote = %+v", remote)
		}
		if remote.Panels[0].SLASeconds != 30 {
			t.Errorf("sla_seconds not decoded: %+v", remote.Panels[0])
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		remote, err := LookupPageRemoteBySlug(context.Background(), newPageFakeClient(), "nope")
		if err != nil || remote != nil {
			t.Fatalf("want (nil, nil) for 404, got (%+v, %v)", remote, err)
		}
	})

	t.Run("server failure propagates", func(t *testing.T) {
		c := newPageFakeClient()
		c.GetStatus["/api/v1/pages/fleet-201"] = 500
		c.GetResponses["/api/v1/pages/fleet-201"] = `{"error":"boom"}`
		// A lookup that cannot tell "not there" from "could not look"
		// plans a create for a page that already exists.
		if _, err := LookupPageRemoteBySlug(context.Background(), c, "fleet-201"); err == nil {
			t.Fatal("a 500 must not be folded into 'page absent'")
		}
	})

	t.Run("sealed panels decode", func(t *testing.T) {
		c := newPageFakeClient()
		c.GetResponses["/api/v1/pages/fleet-201"] = `{"id":"pg_1","slug":"fleet-201","name":"F",
			"panels":[{"panel_id":"secret","span":6,"sealed":true,"owner_crew_name":"Devops"}]}`
		remote, err := LookupPageRemoteBySlug(context.Background(), c, "fleet-201")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		p := remote.Panels[0]
		if !p.Sealed || p.panelID() != "secret" || p.Span != 6 {
			t.Fatalf("sealed placeholder not decoded: %+v", p)
		}
	})
}

// ── Export ─────────────────────────────────────────────────────────────────

func TestExportPages(t *testing.T) {
	c := newPageFakeClient()
	c.GetResponses["/api/v1/pages"] = `[{"slug":"zulu","name":"Zulu"},{"slug":"alpha","name":"Alpha"}]`
	c.GetResponses["/api/v1/pages/zulu"] = `{"id":"pg_z","slug":"zulu","name":"Zulu",
		"panels":[{"id":"p","schema":"metric.v1","owner":"crew/x","producer":"script/s","sla_seconds":3600,"span":12}]}`
	c.GetResponses["/api/v1/pages/alpha"] = `{"id":"pg_a","slug":"alpha","name":"Alpha","description":"first",
		"panels":[{"id":"q","schema":"table.v1","owner":"crew/y","producer":"routine/r","sla_seconds":90,"span":6}]}`

	docs, err := ExportPages(context.Background(), c)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2", len(docs))
	}
	// Deterministic order by slug — the index is sorted by updated_at,
	// which nobody wants their exported YAML ordered by.
	if docs[0].Metadata.Slug != "alpha" || docs[1].Metadata.Slug != "zulu" {
		t.Fatalf("not sorted by slug: %q, %q", docs[0].Metadata.Slug, docs[1].Metadata.Slug)
	}
	if docs[0].APIVersion != pageAPIVersion || docs[0].Kind != pageDocKind {
		t.Errorf("envelope missing: %+v", docs[0])
	}
	if docs[0].Metadata.Description != "first" {
		t.Errorf("description dropped: %+v", docs[0].Metadata)
	}
	if got := docs[0].Spec.Panels[0].SLA; got != "90s" {
		t.Errorf("sla = %q, want %q", got, "90s")
	}
	if got := docs[1].Spec.Panels[0].SLA; got != "1h" {
		t.Errorf("sla = %q, want %q", got, "1h")
	}
	// The round trip has to survive its own validator.
	if err := docs[1].Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Errorf("exported document does not re-validate: %v", err)
	}
}

// TestExportPages_SealedPanelRefuses pins the rule that an incomplete
// export is worse than none: emitting the document without the sealed
// panel would produce YAML that DELETES it on the next apply.
func TestExportPages_SealedPanelRefuses(t *testing.T) {
	c := newPageFakeClient()
	c.GetResponses["/api/v1/pages"] = `[{"slug":"fleet","name":"Fleet"}]`
	c.GetResponses["/api/v1/pages/fleet"] = `{"id":"pg","slug":"fleet","name":"Fleet",
		"panels":[{"panel_id":"secret","span":6,"sealed":true,"owner_crew_name":"Devops"}]}`
	_, err := ExportPages(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("want a sealed-panel refusal, got %v", err)
	}
}

func TestExportPages_ListFailurePropagates(t *testing.T) {
	c := newPageFakeClient()
	c.GetStatus["/api/v1/pages"] = 503
	if _, err := ExportPages(context.Background(), c); err == nil {
		t.Fatal("a failed list must not export as an empty workspace")
	}
}

func TestExportPages_SkipsPageDeletedMidExport(t *testing.T) {
	c := newPageFakeClient()
	c.GetResponses["/api/v1/pages"] = `[{"slug":"gone","name":"Gone"}]`
	// No canned document → the fake answers 404, as the server would
	// for a page deleted between the index and the fetch.
	docs, err := ExportPages(context.Background(), c)
	if err != nil {
		t.Fatalf("ExportPages: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("docs = %+v, want none", docs)
	}
}

func TestPageFormatSLA(t *testing.T) {
	cases := map[int]string{
		30:   "30s",
		90:   "90s",
		120:  "2m",
		3600: "1h",
		7200: "2h",
		0:    "0s",
	}
	for in, want := range cases {
		if got := pageFormatSLA(in); got != want {
			t.Errorf("pageFormatSLA(%d) = %q, want %q", in, got, want)
		}
	}
}

// ── The authored half survives the write path ──────────────────────────────

// pageSensorYAML is a page that is a SENSOR and an OPERATOR console, not
// just a display: the status panel carries a wake gate (§5), a failure
// route (§4 rule 4) and two buttons (§8b.1), and it is written at the
// v1 feature level so `pages.ParseDocument` — KnownFields(true) — accepts
// every byte of it.
const pageSensorYAML = `
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
      public: true
      wake:
        - when: any(state == "critical")
          for: 5m
          agent: crew/devops
          writes: incident
      on_failure:
        issue: crew/lookout
      actions:
        - id: restart-api
          kind: call
          label: Restart API
          style: danger
          routine: watch
          params:
            cluster: prod
          confirm:
            title: Restart the API?
            body: In-flight requests are dropped.
          inputs:
            - name: reason
              type: text
              required: true
        - id: collapse
          kind: toggle
          label: Collapse
          target: [incident]
    - id: incident
      schema: narrative.v1
      owner: crew/devops
      producer: routine/incident-rozbor
      sla: 1h
      span: 12
`

// pageSensorDoc parses pageSensorYAML through BOTH doors and fails the
// test if either refuses it. `crewship apply` decodes into PageDocument;
// `crewship page create --file` decodes into pages.Document. §6 says
// they are the same document, so a fixture only one of them accepts
// would be testing a shape nobody can author.
func pageSensorDoc(t *testing.T) *PageDocument {
	t.Helper()
	if _, err := pages.ParseDocument([]byte(pageSensorYAML)); err != nil {
		t.Fatalf("pages.ParseDocument refused the fixture: %v", err)
	}
	var d PageDocument
	if err := yaml.Unmarshal([]byte(pageSensorYAML), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ctx := pageTestCtx()
	ctx.DeclaredRoutines = append(ctx.DeclaredRoutines, internalapi.SlugLookup{Slug: "incident-rozbor"})
	if err := d.Validate(ctx); err != nil {
		t.Fatalf("Validate refused the fixture: %v", err)
	}
	return &d
}

// TestPageWriteBody_CarriesEveryDeclaredKey is the regression test for a
// manifest that validated clean, applied with exit 0 and produced a page
// with no buttons.
//
// It is deliberately written as a comparison of KEY SETS rather than as a
// list of the fields writeBody happens to send. A test that asserted
// `id`, `schema`, `owner`, `producer`, `sla_seconds` and `span` were
// present would have passed on the broken code — the dropped field was
// one nobody had listed. So: every key the author wrote in the YAML has
// to appear on the wire, and the only permitted rename is the one §11b
// decision 3 mandates.
func TestPageWriteBody_CarriesEveryDeclaredKey(t *testing.T) {
	d := pageSensorDoc(t)

	// The panels exactly as authored, read straight out of the source
	// bytes so the expectation comes from the DOCUMENT and not from the
	// struct that might be missing a field.
	var authored struct {
		Spec struct {
			Panels []map[string]any `yaml:"panels"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(pageSensorYAML), &authored); err != nil {
		t.Fatalf("yaml.Unmarshal (authored): %v", err)
	}

	body, err := d.writeBody()
	if err != nil {
		t.Fatalf("writeBody: %v", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var sent struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sent.Panels) != len(authored.Spec.Panels) {
		t.Fatalf("panels sent = %d, authored = %d", len(sent.Panels), len(authored.Spec.Panels))
	}

	for i, want := range authored.Spec.Panels {
		got := sent.Panels[i]
		for key := range want {
			// §11b decision 3: `sla: 30s` is the only key that crosses the
			// wire under another name.
			if key == "sla" {
				if _, ok := got["sla_seconds"]; !ok {
					t.Errorf("panel %d: sla declared, sla_seconds not sent: %+v", i, got)
				}
				continue
			}
			if _, ok := got[key]; !ok {
				t.Errorf("panel %d: %q is declared in the document and is not on the wire — "+
					"PATCH replaces spec.panels wholesale, so a key the applier drops is a key it DELETES; got %+v",
					i, key, got)
			}
		}
	}
}

// TestPageWriteBody_AuthoredHalfRidesVerbatim pins the CONTENT, not just
// the presence, of the three pass-through keys: a button that arrived
// with its routine or its confirm step stripped is a different button,
// and a gate that lost its `for` window wakes somebody on one bad scrape.
func TestPageWriteBody_AuthoredHalfRidesVerbatim(t *testing.T) {
	d := pageSensorDoc(t)
	body, err := d.writeBody()
	if err != nil {
		t.Fatalf("writeBody: %v", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var sent struct {
		Panels []struct {
			ID        string                `json:"id"`
			Actions   []pages.PanelAction   `json:"actions"`
			Wake      []pages.PanelWake     `json:"wake"`
			OnFailure *pages.PanelOnFailure `json:"on_failure"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := sent.Panels[0]
	declared := d.Spec.Panels[0]

	if len(p.Actions) != len(declared.Actions) {
		t.Fatalf("actions sent = %d, declared = %d — a page that applied with exit 0 and has no buttons",
			len(p.Actions), len(declared.Actions))
	}
	if !reflect.DeepEqual(p.Actions, declared.Actions) {
		t.Errorf("actions did not ride verbatim:\n sent = %+v\n want = %+v", p.Actions, declared.Actions)
	}
	if !reflect.DeepEqual(p.Wake, declared.Wake) {
		t.Errorf("wake did not ride verbatim:\n sent = %+v\n want = %+v", p.Wake, declared.Wake)
	}
	if !reflect.DeepEqual(p.OnFailure, declared.OnFailure) {
		t.Errorf("on_failure did not ride verbatim:\n sent = %+v\n want = %+v", p.OnFailure, declared.OnFailure)
	}
	// The second panel declares none of the three, and an empty list is not
	// the same statement as an absent key: `actions: []` would be "this
	// panel's buttons are gone", which is a thing to say and not a thing to
	// say by accident.
	var bare struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(raw, &bare); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"actions", "wake", "on_failure"} {
		if _, present := bare.Panels[1][key]; present {
			t.Errorf("panel 2 declares no %s; it must be absent, not empty: %+v", key, bare.Panels[1])
		}
	}
}

// TestPageDocument_Plan_SendsActionsOnBothVerbs walks the two paths that
// actually reach the server. writeBody being right is necessary; Plan
// putting its output in the create body AND in the PATCH body is what
// makes it true of an apply.
func TestPageDocument_Plan_SendsActionsOnBothVerbs(t *testing.T) {
	panelsOf := func(t *testing.T, body any) []map[string]any {
		t.Helper()
		m, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("body is %T, want map", body)
		}
		panels, ok := m["panels"].([]map[string]any)
		if !ok {
			t.Fatalf("panels is %T", m["panels"])
		}
		return panels
	}

	t.Run("create", func(t *testing.T) {
		d := pageSensorDoc(t)
		items, err := d.Plan(context.Background(), newPageFakeClient(), nil)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		c := newPageFakeClient()
		if err := items[0].Exec(context.Background(), c); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		panels := panelsOf(t, c.Calls[0].Body)
		for _, key := range []string{"actions", "wake", "on_failure"} {
			if _, present := panels[0][key]; !present {
				t.Errorf("POST body drops %q: %+v", key, panels[0])
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		d := pageSensorDoc(t)
		remote := &PageRemote{
			Slug: "fleet-201",
			Name: "Old name",
			Panels: []PagePanelRemote{
				{ID: "sluzby", Schema: "status.v1", Title: "Jede to?", Owner: "crew/lookout",
					Producer: "script/watch-services.sh", SLASeconds: 30, Span: 8},
				{ID: "incident", Schema: "narrative.v1", Owner: "crew/devops",
					Producer: "routine/incident-rozbor", SLASeconds: 3600, Span: 12},
			},
		}
		items, err := d.Plan(context.Background(), newPageFakeClient(), remote)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("want update, got %+v", items[0])
		}
		c := newPageFakeClient()
		if err := items[0].Exec(context.Background(), c); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		panels := panelsOf(t, c.Calls[0].Body)
		// PATCH replaces spec.panels wholesale and reconciles the page's
		// automations rows against the result, so a panel sent without its
		// gate is a gate deleted — by the update that was only meant to fix
		// the page's name.
		for _, key := range []string{"actions", "wake", "on_failure"} {
			if _, present := panels[0][key]; !present {
				t.Errorf("PATCH body drops %q: %+v", key, panels[0])
			}
		}
	})
}

// TestPageWriteBody_JSONShape checks the body marshals to the shape the
// handler decodes (pageWriteRequest) — flat panels, integer sla.
func TestPageWriteBody_JSONShape(t *testing.T) {
	d := pageTestDoc()
	d.Metadata.Description = "the fleet"
	body, err := d.writeBody()
	if err != nil {
		t.Fatalf("writeBody: %v", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Panels      []struct {
			ID         string `json:"id"`
			Schema     string `json:"schema"`
			Owner      string `json:"owner"`
			Producer   string `json:"producer"`
			SLASeconds int    `json:"sla_seconds"`
			Span       int    `json:"span"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Slug != "fleet-201" || decoded.Description != "the fleet" || len(decoded.Panels) != 1 {
		t.Fatalf("body = %s", raw)
	}
	p := decoded.Panels[0]
	if p.ID != "services" || p.Schema != "status.v1" || p.Owner != "crew/lookout" ||
		p.Producer != "routine/watch" || p.SLASeconds != 30 || p.Span != 8 {
		t.Errorf("panel = %+v", p)
	}
}
