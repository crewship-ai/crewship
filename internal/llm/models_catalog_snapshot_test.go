package llm

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// Every curated id in config/models.json must be a model the embedded
// models.dev snapshot knows for that provider. The two answer different
// questions — the curated list says "which ids may you type", the snapshot
// says "what does that id cost" — but an id the snapshot has never heard of
// is, in practice, a typo or a model that does not exist, and this is the
// cheapest place to catch it before a picker offers it to a customer.
func TestModelCatalog_CuratedIDsExistInSnapshot(t *testing.T) {
	snapshot := modelcatalog.Default()
	for _, provider := range []string{"anthropic", "openai", "google"} {
		t.Run(provider, func(t *testing.T) {
			snapshotID := provider
			if spec, ok := LookupProvider(provider); ok && spec.CatalogID != "" {
				snapshotID = spec.CatalogID
			}
			known := map[string]bool{}
			for _, m := range snapshot.Models(snapshotID) {
				known[m.ID] = true
			}
			if len(known) == 0 {
				t.Skipf("snapshot carries no models for %q — the trim changed; widen it or drop this provider here", snapshotID)
			}
			for _, m := range CuratedModels(provider) {
				if !known[m.ID] {
					t.Errorf("curated %s model %q is not in the models.dev snapshot — typo, or refresh the snapshot", provider, m.ID)
				}
			}
		})
	}
}
