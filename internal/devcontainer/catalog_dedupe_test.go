package devcontainer

import "testing"

func TestDedupeCatalogByID(t *testing.T) {
	ce := func(ref string) CatalogEntry { return CatalogEntry{Ref: ref, Name: catalogShortID(ref)} }
	entries := []CatalogEntry{
		ce("ghcr.io/sliekens/devcontainer-features/ansible:1"),  // fork
		ce("ghcr.io/devcontainers-extra/features/ansible:2"),    // official-extra (should win)
		ce("ghcr.io/devcontainers/features/python:1"),           // official, unique
		ce("ghcr.io/shyim/devcontainers-features/bun:1"),        // fork-only id
		ce("ghcr.io/devcontainers-community/features/direnv:1"), // community
		ce("ghcr.io/esimkowitz/devcontainer-features/direnv:1"), // fork (loses to community)
	}
	out := dedupeCatalogByID(entries)

	byID := map[string]string{}
	for _, e := range out {
		id := catalogShortID(e.Ref)
		if _, dup := byID[id]; dup {
			t.Fatalf("duplicate id %q survived dedup: %v", id, out)
		}
		byID[id] = e.Ref
	}
	if got := byID["ansible"]; got != "ghcr.io/devcontainers-extra/features/ansible:2" {
		t.Errorf("ansible kept %q, want devcontainers-extra", got)
	}
	if got := byID["direnv"]; got != "ghcr.io/devcontainers-community/features/direnv:1" {
		t.Errorf("direnv kept %q, want devcontainers-community", got)
	}
	if _, ok := byID["bun"]; !ok {
		t.Error("fork-only id 'bun' was dropped; should be kept")
	}
	if _, ok := byID["python"]; !ok {
		t.Error("unique id 'python' was dropped")
	}
	if len(out) != 4 {
		t.Errorf("got %d entries, want 4 (ansible, python, bun, direnv)", len(out))
	}
}

func TestPublisherRank(t *testing.T) {
	cases := map[string]int{
		"ghcr.io/devcontainers/features/go:1":          0,
		"ghcr.io/devcontainers-extra/features/bun:1":   1,
		"ghcr.io/devcontainers-community/features/x:1": 2,
		"ghcr.io/devcontainer-community/features/x:1":  2,
		"ghcr.io/randomfork/devcontainer-features/x:1": 9,
	}
	for ref, want := range cases {
		if got := publisherRank(ref); got != want {
			t.Errorf("publisherRank(%q) = %d, want %d", ref, got, want)
		}
	}
}
