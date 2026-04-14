package devcontainer

import (
	"strings"
	"testing"
)

func TestCatalogRefsAreValid(t *testing.T) {
	for _, entry := range Catalog {
		_, _, _, err := ParseFeatureRef(entry.Ref)
		if err != nil {
			t.Errorf("catalog entry %q has invalid ref %q: %v", entry.Name, entry.Ref, err)
		}
	}
}

func TestCatalogEntriesHaveRequiredFields(t *testing.T) {
	for _, entry := range Catalog {
		if entry.Name == "" {
			t.Errorf("catalog entry with ref %q has empty Name", entry.Ref)
		}
		if entry.Description == "" {
			t.Errorf("catalog entry %q has empty Description", entry.Name)
		}
		if entry.Category == "" {
			t.Errorf("catalog entry %q has empty Category", entry.Name)
		}
		validCategories := map[string]bool{
			"languages": true, "tools": true, "cloud": true, "databases": true,
		}
		if !validCategories[entry.Category] {
			t.Errorf("catalog entry %q has invalid category %q", entry.Name, entry.Category)
		}
		if entry.Icon == "" {
			t.Errorf("catalog entry %q has empty Icon", entry.Name)
		}
		if entry.SizeHint == "" {
			t.Errorf("catalog entry %q has empty SizeHint", entry.Name)
		}
	}
}

func TestCatalogHasMinimumEntries(t *testing.T) {
	if len(Catalog) < 12 {
		t.Errorf("expected at least 12 catalog entries, got %d", len(Catalog))
	}
}

func TestSearchCatalogByName(t *testing.T) {
	results := SearchCatalog("python")
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'python'")
	}
	found := false
	for _, r := range results {
		if strings.Contains(strings.ToLower(r.Name), "python") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a result with 'python' in the name")
	}
}

func TestSearchCatalogByCategory(t *testing.T) {
	results := SearchCatalog("cloud")
	if len(results) == 0 {
		t.Fatal("expected results for category 'cloud'")
	}
}

func TestSearchCatalogCaseInsensitive(t *testing.T) {
	lower := SearchCatalog("node")
	upper := SearchCatalog("NODE")
	mixed := SearchCatalog("Node")

	if len(lower) == 0 {
		t.Fatal("expected results for 'node'")
	}
	if len(lower) != len(upper) || len(lower) != len(mixed) {
		t.Errorf("case-insensitive search should return same count: lower=%d upper=%d mixed=%d",
			len(lower), len(upper), len(mixed))
	}
}

func TestSearchCatalogNoMatch(t *testing.T) {
	results := SearchCatalog("zzz_nonexistent_zzz")
	if len(results) != 0 {
		t.Errorf("expected empty results for nonsense query, got %d", len(results))
	}
}

func TestSearchCatalogEmptyQuery(t *testing.T) {
	results := SearchCatalog("")
	if len(results) != len(Catalog) {
		t.Errorf("empty query should return all entries: got %d, want %d", len(results), len(Catalog))
	}
}
