package kinds

// kind: Page and `refresh:` (docs/prd/pages.md §12 v1.1).
//
// The manifest door has to agree with the CLI door about what a page document
// says — that is §6's promise, and it is why PageSpec carries
// []pages.PanelSpec verbatim rather than a copy. These pin the two halves of
// that for the refresh trigger: a manifest declaring one must VALIDATE (the
// field exists on the type), and writeBody must SEND it (a key the applier
// drops is a key the PATCH deletes, and here what is deleted is the compiled
// automations row).

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/crewship-ai/crewship/internal/pages"
)

// pageRefreshYAML is the PRD's §6 worked example at the v1.1 feature level.
const pageRefreshYAML = `
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

func pageRefreshDoc(t *testing.T, src string) (*PageDocument, error) {
	t.Helper()
	var d PageDocument
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ctx := pageTestCtx()
	ctx.DeclaredRoutines = append(ctx.DeclaredRoutines, internalapi.SlugLookup{Slug: "incident-rozbor"})
	return &d, d.Validate(ctx)
}

func TestPageManifest_AcceptsRefresh(t *testing.T) {
	if _, err := pages.ParseDocument([]byte(pageRefreshYAML)); err != nil {
		t.Fatalf("pages.ParseDocument refused the fixture: %v", err)
	}
	if _, err := pageRefreshDoc(t, pageRefreshYAML); err != nil {
		t.Fatalf("kind: Page refused a document `crewship page create --file` accepts: %v", err)
	}
}

// The refusals are the pages package's and are delegated, not reimplemented —
// this asserts they actually arrive through the manifest door, because a
// manifest that validated clean and applied a trigger the server then refused
// would be the same document valid in one door and invalid in the next.
func TestPageManifest_RefusesABadRefresh(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     string
		mustSay string
	}{
		{
			name:    "outside the closed set",
			src:     strings.Replace(pageRefreshYAML, "refresh: on:wake", "refresh: on:push", 1),
			mustSay: "on:panels-changed",
		},
		{
			name:    "a producer the server cannot run",
			src:     strings.Replace(pageRefreshYAML, "producer: routine/incident-rozbor", "producer: script/rozbor.sh", 1),
			mustSay: "routine/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pageRefreshDoc(t, tc.src)
			if err == nil {
				t.Fatal("the manifest door accepted a refresh the server refuses")
			}
			if !strings.Contains(err.Error(), tc.mustSay) {
				t.Errorf("refusal does not name %q: %v", tc.mustSay, err)
			}
		})
	}
}

// The bug this file exists for: a manifest that validated clean, applied with
// exit 0, and produced a page whose refresh trigger was never compiled.
func TestPageManifest_WriteBodyCarriesRefresh(t *testing.T) {
	d, err := pageRefreshDoc(t, pageRefreshYAML)
	if err != nil {
		t.Fatalf("Validate: %v", err)
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
		Panels []struct {
			ID      string `json:"id"`
			Refresh string `json:"refresh"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sent.Panels) != 2 {
		t.Fatalf("panels sent = %d, want 2", len(sent.Panels))
	}
	if sent.Panels[1].Refresh != "on:wake" {
		t.Errorf("refresh = %q on the wire, want on:wake — a key the applier drops is a key the "+
			"PATCH deletes, and here that is the automations row: %s", sent.Panels[1].Refresh, string(raw))
	}
	// A panel declaring none sends none, rather than an empty string the
	// server would have to tell apart from an omission.
	if sent.Panels[0].Refresh != "" {
		t.Errorf("a panel declaring no refresh sent %q", sent.Panels[0].Refresh)
	}
}
