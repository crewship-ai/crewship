package kinds

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The `slash` block has to survive the manifest, in both directions.
//
// RoutineSpec is a closed struct with no inline catch-all — only
// RoutineStep has one — and the manifest decoder runs with
// KnownFields(false). So an unmodelled top-level key is not an error and
// not a warning: it is silently dropped. Two ways that bites, and the
// second is the worse one:
//
//   - `crewship apply -f routine.yaml` reports success and the routine
//     never appears in any palette;
//   - `crewship export` → edit something unrelated → `crewship apply`
//     STRIPS a working slash block off a routine that was authored
//     through the dashboard, taking its command away from everyone.
//
// docs/manifest/routine.md documents `spec.slash`, so these tests are
// what make the documentation true.

const slashRoutineYAML = `
apiVersion: crewship/v1
kind: Routine
metadata:
  name: Accounting pack
  slug: msn-etn-podklady
  labels:
    crew: uctarna
spec:
  dsl_version: "1.0"
  inputs:
    - name: obdobi
      type: string
  slash:
    enabled: true
    label: Monthly accounting pack
    label_cs: Účetní podklady za měsíc
    icon: receipt
  steps:
    - id: a
      type: transform
`

func decodeSlashRoutine(t *testing.T, src string) RoutineDocument {
	t.Helper()
	var doc RoutineDocument
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}

func TestRoutineManifest_SlashSurvivesApply(t *testing.T) {
	doc := decodeSlashRoutine(t, slashRoutineYAML)
	if doc.Spec.Slash == nil {
		t.Fatal("slash block was dropped by the YAML decoder")
	}
	if !doc.Spec.Slash.Enabled || doc.Spec.Slash.Label != "Monthly accounting pack" {
		t.Errorf("slash decoded wrong: %+v", doc.Spec.Slash)
	}
	if doc.Spec.Slash.LabelCS != "Účetní podklady za měsíc" || doc.Spec.Slash.Icon != "receipt" {
		t.Errorf("slash decoded wrong: %+v", doc.Spec.Slash)
	}

	// The body that actually reaches the server. definitionJSONShape is
	// an explicit allowlist, so a field modelled on the struct but not
	// emitted here still never arrives.
	body, err := json.Marshal(doc.buildSaveBody())
	if err != nil {
		t.Fatalf("marshal save body: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		`"slash"`,
		`"enabled":true`,
		`"label":"Monthly accounting pack"`,
		`"icon":"receipt"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("apply body is missing %s:\n%s", want, got)
		}
	}
}

// A routine with no slash block must not grow one — the wire shape for
// every routine written before this feature is unchanged, and
// pipeline.Parse would be handed a key the author never wrote.
func TestRoutineManifest_NoSlashBlockStaysAbsent(t *testing.T) {
	doc := decodeSlashRoutine(t, strings.Replace(slashRoutineYAML, `  slash:
    enabled: true
    label: Monthly accounting pack
    label_cs: Účetní podklady za měsíc
    icon: receipt
`, "", 1))
	if doc.Spec.Slash != nil {
		t.Fatalf("Slash = %+v, want nil", doc.Spec.Slash)
	}
	body, _ := json.Marshal(doc.buildSaveBody())
	if strings.Contains(string(body), "slash") {
		t.Errorf("apply body grew a slash key: %s", body)
	}
}

// Export decodes the stored definition back into RoutineSpec. Without
// the field, a routine authored through the dashboard came back from
// `crewship export` with its slash block gone — and the next apply
// removed the command from the palette for everyone in the workspace.
func TestRoutineManifest_SlashSurvivesExportDecode(t *testing.T) {
	const stored = `{
		"dsl_version":"1.0",
		"name":"msn-etn-podklady",
		"inputs":[{"name":"obdobi","type":"string"}],
		"slash":{"enabled":true,"label":"Monthly accounting pack","icon":"receipt"},
		"steps":[{"id":"a","type":"transform"}]
	}`
	var spec RoutineSpec
	if err := json.Unmarshal([]byte(stored), &spec); err != nil {
		t.Fatalf("decode stored definition: %v", err)
	}
	if spec.Slash == nil {
		t.Fatal("export decode dropped the slash block")
	}
	if spec.Slash.Label != "Monthly accounting pack" || spec.Slash.Icon != "receipt" {
		t.Errorf("export decode mangled the block: %+v", spec.Slash)
	}

	// And round-trips out again through the apply path, so
	// export → apply is a no-op rather than a silent removal.
	doc := RoutineDocument{Spec: spec}
	doc.Metadata.Slug = "msn-etn-podklady"
	body, _ := json.Marshal(doc.buildSaveBody())
	if !strings.Contains(string(body), `"slash"`) {
		t.Errorf("export → apply lost the slash block: %s", body)
	}
}

// A YAML typo in a field name must be visible to the author. Modelling
// the block (rather than accepting `any`) is what turns
// `enabeld: true` into something `crewship plan` can be checked against
// — it decodes to Enabled:false, so the routine is simply not offered,
// which is the same outcome as writing enabled:false deliberately.
func TestRoutineManifest_SlashTypoDoesNotEnable(t *testing.T) {
	doc := decodeSlashRoutine(t, strings.Replace(slashRoutineYAML, "enabled: true", "enabeld: true", 1))
	if doc.Spec.Slash == nil {
		t.Fatal("slash block missing")
	}
	if doc.Spec.Slash.Enabled {
		t.Error("a misspelled key enabled the palette entry")
	}
}
