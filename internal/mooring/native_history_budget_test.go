package mooring

import (
	"testing"
)

func TestNativeStoreDefaultHistoryBudgetIsVisibleAndSupportsLargeNativeHistory(t *testing.T) {
	fixture, _, db, _, scope, p := nativeStoreFixture(t)
	cfg := fixture.config
	cfg.MaxHistoryBytes = 0
	store := NewNativeStore(db, cfg)
	if _, _, err := store.Initialize(t.Context(), scope, p, []byte(`{"body":"initial"}`)); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	// Two thousand different 4MiB snapshots require byte headroom. This checks
	// policy only; it does not substitute for a changing-document soak test.
	if state.Quota.LimitBytes < 16<<30 || state.Quota.Retention == "" {
		t.Fatalf("native history policy is not visible or adequate: %+v", state.Quota)
	}
}
