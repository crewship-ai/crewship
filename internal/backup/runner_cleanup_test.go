package backup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// TestCleanupStalePartials_OwnerSlugScoping is the regression guard for
// the multi-tenant footgun the previous behaviour created: an
// unconditional sweep deleting another tenant's still-active >1h
// `.partial` mid-write. The fix scopes the sweep by slug; this test
// pins both sides of that contract:
//
//  1. Same-slug stale .partials are deleted.
//  2. Other-slug stale .partials are LEFT ALONE — even when they are
//     older than maxAge.
//  3. Same-slug fresh .partials (newer than maxAge) are LEFT ALONE.
//  4. Non-.partial files of any age are LEFT ALONE.
//  5. An empty ownerSlug preserves the old global-sweep behaviour
//     (used by admin-side sweepers that legitimately want to clean
//     every tenant).
//  6. Substring-of-slug guard: "alpha2" must NOT be matched by "alpha".
//  7. Scope-token guard: ownerSlug="workspace" must NOT falsely match
//     `crewship-workspace-anytenant-…` — the slug segment is parsed
//     exactly, not by substring containment.
func TestCleanupStalePartials_OwnerSlugScoping(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(-5 * time.Minute)

	type entry struct {
		name      string
		mtime     time.Time
		shouldDel bool // when ownerSlug = "alpha"
		shouldKey bool // when ownerSlug = "" (global sweep)
	}
	entries := []entry{
		// alpha-owned, old → swept under both modes
		{"crewship-workspace-alpha-20260101T000000Z.tar.zst.partial", old, true, true},
		// beta-owned, old → KEPT under slug="alpha", swept under global
		{"crewship-workspace-beta-20260101T000000Z.tar.zst.partial", old, false, true},
		// alpha-owned, fresh → kept under both
		{"crewship-workspace-alpha-20260514T120000Z.tar.zst.partial", fresh, false, false},
		// non-.partial, old → kept under both (we only target .partial)
		{"crewship-workspace-alpha-20260101T000000Z.tar.zst", old, false, false},
		// substring-of-slug guard: "alpha2" must NOT be matched by "alpha"
		{"crewship-workspace-alpha2-20260101T000000Z.tar.zst.partial", old, false, true},
		// slug with internal dashes — exact-match still works
		{"crewship-crew-team-alpha-data-20260101T000000Z.tar.zst.partial", old, false, true},
	}

	create := func(t *testing.T, dir string) {
		t.Helper()
		for _, e := range entries {
			path := filepath.Join(dir, e.name)
			f, err := os.Create(path)
			if err != nil {
				t.Fatalf("create %s: %v", e.name, err)
			}
			_ = f.Close()
			if err := os.Chtimes(path, e.mtime, e.mtime); err != nil {
				t.Fatalf("chtimes %s: %v", e.name, err)
			}
		}
	}

	listing := func(t *testing.T, dir string) []string {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		out := make([]string, 0, len(es))
		for _, e := range es {
			out = append(out, e.Name())
		}
		sort.Strings(out)
		return out
	}

	t.Run("scoped to alpha", func(t *testing.T) {
		dir := t.TempDir()
		create(t, dir)
		cleanupStalePartials(context.Background(), nil, dir, "alpha", time.Hour)
		got := listing(t, dir)
		for _, e := range entries {
			present := contains(got, e.name)
			wantPresent := !e.shouldDel
			if present != wantPresent {
				t.Errorf("scoped sweep (alpha): %s present=%v want=%v", e.name, present, wantPresent)
			}
		}
	})

	t.Run("global sweep (empty slug)", func(t *testing.T) {
		dir := t.TempDir()
		create(t, dir)
		cleanupStalePartials(context.Background(), nil, dir, "", time.Hour)
		got := listing(t, dir)
		for _, e := range entries {
			present := contains(got, e.name)
			wantPresent := !e.shouldKey
			if present != wantPresent {
				t.Errorf("global sweep: %s present=%v want=%v", e.name, present, wantPresent)
			}
		}
	})

	t.Run("scope token isn't mistaken for a slug", func(t *testing.T) {
		// If ownerSlug == "workspace" (a real but weird tenant slug),
		// the simple substring approach would have matched every
		// workspace-scope bundle because "-workspace-" appears between
		// "crewship-" and the actual tenant slug in BundleFileName's
		// output. The parse-and-compare implementation must reject
		// that match.
		dir := t.TempDir()
		// One bundle owned by tenant "workspace" (legitimately), one
		// owned by tenant "alpha" (definitely not — even though its
		// filename ALSO starts with "crewship-workspace-").
		legit := "crewship-workspace-workspace-20260101T000000Z.tar.zst.partial"
		impostor := "crewship-workspace-alpha-20260101T000000Z.tar.zst.partial"
		for _, name := range []string{legit, impostor} {
			path := filepath.Join(dir, name)
			f, err := os.Create(path)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = f.Close()
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatalf("chtimes: %v", err)
			}
		}
		cleanupStalePartials(context.Background(), nil, dir, "workspace", time.Hour)
		got := listing(t, dir)
		if contains(got, legit) {
			t.Errorf("legit workspace-slug file should have been swept, got: %v", got)
		}
		if !contains(got, impostor) {
			t.Errorf("alpha-slug file got swept under slug=workspace; scope token leaked into slug match. got: %v", got)
		}
	})
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestCleanupStalePartials_MissingDir is a sanity check that a missing
// directory is a silent no-op, not a panic — the sweep runs as a
// best-effort side activity inside CreateBackup and must never abort
// the actual backup if the backups dir was just created seconds ago
// and a transient FS race makes ReadDir fail.
func TestCleanupStalePartials_MissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cleanupStalePartials(context.Background(), nil, missing, "alpha", time.Hour)
}

// TestSlugFromPartialName exercises the parser independently of the
// sweeper, with both happy paths (every scope, slug with dashes) and
// rejection paths (wrong prefix/suffix, missing timestamp, scope-only
// filenames). Pinning this here keeps the slug-extraction contract
// stable even if the sweeper grows additional callers.
func TestSlugFromPartialName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantSlug string
		wantOK   bool
	}{
		{"workspace happy", "crewship-workspace-acme-20260514T120000Z.tar.zst.partial", "acme", true},
		{"crew happy", "crewship-crew-payments-20260514T120000Z.tar.zst.partial", "payments", true},
		{"instance happy", "crewship-instance-prod-20260514T120000Z.tar.zst.partial", "prod", true},
		{"slug with dashes", "crewship-crew-team-alpha-data-20260514T120000Z.tar.zst.partial", "team-alpha-data", true},
		{"slug equals scope token", "crewship-workspace-workspace-20260514T120000Z.tar.zst.partial", "workspace", true},
		{"wrong prefix", "backup-workspace-acme-20260514T120000Z.tar.zst.partial", "", false},
		{"wrong suffix", "crewship-workspace-acme-20260514T120000Z.tar.zst", "", false},
		{"unknown scope", "crewship-pipeline-acme-20260514T120000Z.tar.zst.partial", "", false},
		{"missing timestamp", "crewship-workspace-acme.tar.zst.partial", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSlug, gotOK := slugFromPartialName(tc.input)
			if gotOK != tc.wantOK || gotSlug != tc.wantSlug {
				t.Errorf("slugFromPartialName(%q) = (%q, %v); want (%q, %v)",
					tc.input, gotSlug, gotOK, tc.wantSlug, tc.wantOK)
			}
		})
	}
}
