package notify

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// categoryKeyRe pulls the `key: "..."` field out of each CATEGORY entry in
// lib/notification-categories.ts. The trailing `hint:` is what distinguishes
// a category from a GROUP — groups share the {key, label} prefix, so matching
// on that alone would count them too.
//
// Deliberately a regex rather than a TS parser: the file is a plain literal
// by design, and a parser dependency would cost far more than the shape
// constraint this imposes.
var categoryKeyRe = regexp.MustCompile(`\{\s*key:\s*"([^"]+)",\s*label:\s*"[^"]*",\s*hint:`)

// TestFrontendCategoriesMatchBackend pins the TS mirror of the category
// vocabulary against the Go original.
//
// The two lists are physically separate — Go validates every write and drives
// the DB CHECK, TS only renders — and a drift between them is SILENT: a
// category missing from the TS side simply never appears as a row in the
// preference matrix, so a user cannot subscribe to it and nobody gets an
// error. That is exactly the failure taxonomy v2 exists to fix (four of the
// original nine categories were unreachable), so it gets a guard rather than
// a comment asking people to remember.
func TestFrontendCategoriesMatchBackend(t *testing.T) {
	path := filepath.Join("..", "..", "lib", "notification-categories.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	matches := categoryKeyRe.FindAllStringSubmatch(string(src), -1)
	got := make([]string, 0, len(matches))
	for _, m := range matches {
		got = append(got, m[1])
	}
	if len(got) == 0 {
		t.Fatalf("found no category entries in %s — did the file's shape change? "+
			"This test greps `{ key: \"...\", label:` and needs updating alongside.", path)
	}

	if len(got) != len(AllCategories) {
		t.Errorf("frontend lists %d categories, backend has %d\n  frontend: %v\n  backend:  %v",
			len(got), len(AllCategories), got, AllCategories)
	}
	// Order matters: both sides render the matrix rows in list order, and a
	// silently reordered grid is a usability regression even when the set
	// matches.
	for i, want := range AllCategories {
		if i >= len(got) {
			t.Errorf("frontend is missing category %q (position %d)", want, i)
			continue
		}
		if got[i] != want {
			t.Errorf("category %d: frontend has %q, backend has %q", i, got[i], want)
		}
	}
	for i := len(AllCategories); i < len(got); i++ {
		t.Errorf("frontend has extra category %q that the backend does not define", got[i])
	}
}
