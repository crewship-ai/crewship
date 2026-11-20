package kinds

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	skillspkg "github.com/crewship-ai/crewship/internal/skills"
)

// storedRowFor is the GET /skills/{id} body a server holds after importing
// body: the row values the importer derives, in the response's JSON shape.
func storedRowFor(t *testing.T, body string) map[string]any {
	t.Helper()
	f, err := skillspkg.ContentImportFields(body)
	if err != nil {
		t.Fatal(err)
	}
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return map[string]any{
		"id": "sk_existing", "slug": f.Slug, "name": f.DisplayName, "display_name": f.DisplayName,
		"description": null(f.Description), "version": f.Version, "author": null(f.Author),
		"category": f.Category, "icon": null(f.Icon), "credential_requirements": f.CredentialRequirements,
		"tags": f.Tags, "content": f.Content, "vendor": f.Vendor, "spdx_license": null(f.SPDXLicense),
		"license": null(f.License), "scan_status": f.ScanStatus, "description_quality": null(f.DescriptionQuality),
		"source": "CUSTOM",
	}
}

// #5334: a skill whose stored row already holds what importing its body would
// write plans unchanged, with no write. A changed byte, a URL source, or a
// server that cannot confirm the row keeps the Update.
func TestSkill_Plan_UnchangedBodyIsNoop(t *testing.T) {
	base := makeSkillDoc()
	remote := &SkillRemote{
		ID: "sk_existing", Name: "Network Probe", Slug: "network-probe", DisplayName: "Network Probe",
		Description: "TCP/UDP probes against allow-listed hosts", Category: "networking", Icon: "network", Source: "CUSTOM",
	}
	changed := strings.Replace(base.Spec.Inline, "investigating reachability.", "investigating reachability!", 1)
	cases := []struct {
		name    string
		mutate  func(d *SkillDocument)
		stored  any // nil: GET /skills/{id} answers 404 (older server, unreadable row)
		decor   func(r *SkillRemote)
		want    internalapi.PlanAction
		wantGET bool
	}{
		{name: "identical body", stored: storedRowFor(t, base.Spec.Inline), want: internalapi.ActionUnchanged, wantGET: true},
		{name: "one body byte changed", mutate: func(d *SkillDocument) { d.Spec.Inline = changed }, stored: storedRowFor(t, base.Spec.Inline), want: internalapi.ActionUpdate, wantGET: true},
		{name: "row unreadable", stored: nil, want: internalapi.ActionUpdate, wantGET: true},
		// Decoration is not sent by the import, so it cannot keep a matching
		// body from converging.
		{name: "decoration differs, body matches", stored: storedRowFor(t, base.Spec.Inline), decor: func(r *SkillRemote) { r.Icon = "other"; r.DisplayName = "network-probe" }, want: internalapi.ActionUnchanged, wantGET: true},
		{name: "url source is never confirmed", mutate: func(d *SkillDocument) { d.Spec.Inline = ""; d.Spec.Source = "https://example.com/SKILL.md" }, stored: storedRowFor(t, base.Spec.Inline), want: internalapi.ActionUpdate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeSkillDoc()
			if tc.mutate != nil {
				tc.mutate(doc)
			}
			if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			client := newSkillFake()
			if tc.stored != nil {
				client.details = map[string]any{"sk_existing": tc.stored}
			}
			r := *remote
			if tc.decor != nil {
				tc.decor(&r)
			}
			items, err := doc.Plan(context.Background(), client, &r)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(items) != 1 || items[0].Action != tc.want {
				t.Fatalf("want %v, got %+v", tc.want, items)
			}
			if tc.want == internalapi.ActionUnchanged && items[0].Exec != nil {
				t.Fatal("an unchanged skill must not write")
			}
			if tc.want == internalapi.ActionUpdate && items[0].Exec == nil {
				t.Fatal("an update must write")
			}
			read := false
			for _, c := range client.calls {
				read = read || c == "GET /api/v1/skills/sk_existing"
			}
			if read != tc.wantGET {
				t.Fatalf("read the stored row = %v, want %v (calls %v)", read, tc.wantGET, client.calls)
			}
		})
	}
}
