package backup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
//     (used by admin-side `crewship backup rotate --purge-partials`
//     style sweepers that legitimately want to clean every tenant).
func TestCleanupStalePartials_OwnerSlugScoping(t *testing.T) {
	dir := t.TempDir()

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
	}

	create := func() {
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

	listing := func() []string {
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

	// --- 1) Scoped sweep: ownerSlug = "alpha" ---
	create()
	cleanupStalePartials(context.Background(), nil, dir, "alpha", time.Hour)
	got := listing()
	for _, e := range entries {
		present := contains(got, e.name)
		wantPresent := !e.shouldDel
		if present != wantPresent {
			t.Errorf("scoped sweep (alpha): %s present=%v want=%v", e.name, present, wantPresent)
		}
	}

	// Wipe and restart for the global sweep arm.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// --- 2) Global sweep: ownerSlug = "" (old behaviour) ---
	create()
	cleanupStalePartials(context.Background(), nil, dir, "", time.Hour)
	got = listing()
	for _, e := range entries {
		present := contains(got, e.name)
		// "shouldKey" = should the file still be there under global sweep?
		wantPresent := !e.shouldKey
		if present != wantPresent {
			t.Errorf("global sweep: %s present=%v want=%v", e.name, present, wantPresent)
		}
	}
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
	// Function returns nothing; assertion is "no panic, no error
	// surfaced". strings.Contains kept around so the linter doesn't
	// complain about the import sneaking in on import-list rewrites.
	_ = strings.Contains
	cleanupStalePartials(context.Background(), nil, missing, "alpha", time.Hour)
}
