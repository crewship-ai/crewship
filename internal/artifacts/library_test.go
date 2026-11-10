package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func libraryRequest(base int64, tokens string) VersionRequest {
	return VersionRequest{BaseVersion: base, EntryPoint: "index.html", Files: map[string]VersionFile{
		"index.html":         {Content: []byte("<main>library</main>"), MediaType: "text/html"},
		"design-system.json": {Content: []byte(tokens), MediaType: "application/json"},
	}}
}

// libraryFixture adds a second project ("consumer") beside versionFixture's
// "page", which acts as the library.
func libraryFixture(t *testing.T) (*VersionStore, VersionKey, VersionKey, VersionAuthorize, LibraryVersionAuthorize) {
	t.Helper()
	s, lib, a, db := versionFixture(t, VersionOptions{})
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('consumer','ws','consumer','Consumer','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	consumer := VersionKey{"ws", "consumer"}
	if _, err := s.Apply(t.Context(), consumer, versionRequest(0, "consumer"), a); err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, tx *sql.Tx, _ int64) (string, error) { return a(ctx, tx) }
	return s, lib, consumer, a, read
}

func TestLibraryPublishPinAndUpdate(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, lib, libraryRequest(0, `{"colors":{"primary":"#111"},"radii":{"sm":"2px"}}`), a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); !errors.Is(err, ErrLibraryUnreadable) {
		t.Fatalf("pin before publication: %v", err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	pin, changed, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read)
	if err != nil || !changed || pin.LibraryVersion != 1 || pin.LibraryName != "Page" || len(pin.SHA256) != 64 || !strings.Contains(string(pin.Content), "#111") {
		t.Fatalf("pin %+v %v %v", pin, changed, err)
	}
	// A retried first pin is a no-op, not a conflict.
	again, changed, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read)
	if err != nil || changed || again.CopiedAt != pin.CopiedAt {
		t.Fatalf("idempotent pin %+v %v %v", again, changed, err)
	}

	// v2 edits a token, removes one and adds one; then the owner restores v1
	// as v3. Neither edit nor restore moves the publication or the pin.
	if _, err = s.Apply(ctx, lib, libraryRequest(1, `{"colors":{"primary":"#222","accent":"#f00"}}`), a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, lib, 2, 1, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadLibraryPin(ctx, consumer, a)
	if err != nil || got.LibraryVersion != 1 || got.SHA256 != pin.SHA256 {
		t.Fatalf("pin after restore %+v %v", got, err)
	}
	up, err := s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return read })
	if err != nil || up.UpdateAvailable || up.PublishedVersion != 1 || !up.LibraryAvailable {
		t.Fatalf("no update yet %+v %v", up, err)
	}

	if _, err = s.PublishLibrary(ctx, lib, 2, a); err != nil {
		t.Fatal(err)
	}
	up, err = s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return read })
	if err != nil || !up.UpdateAvailable || up.PinnedVersion != 1 || up.PublishedVersion != 2 {
		t.Fatalf("update %+v %v", up, err)
	}
	if strings.Join(up.Changes.Added, ",") != "colors.accent" || strings.Join(up.Changes.Removed, ",") != "radii.sm" || strings.Join(up.Changes.Changed, ",") != "colors.primary" {
		t.Fatalf("changes %+v", up.Changes)
	}
	// Applying the update is compare-and-swap on the pinned version.
	if _, _, err = s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 2}, a, read); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update %v", err)
	}
	pin, changed, err = s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{2, "page", 1}, a, read)
	if err != nil || !changed || pin.LibraryVersion != 2 || !strings.Contains(string(pin.Content), "#222") {
		t.Fatalf("update %+v %v %v", pin, changed, err)
	}
}

func TestLibraryPinSurvivesLostLibraryAccessButBlocksUpdate(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, lib, libraryRequest(0, `{"colors":{"primary":"#111"}}`), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, lib, libraryRequest(1, `{"colors":{"primary":"#333"}}`), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 2, a); err != nil {
		t.Fatal(err)
	}
	revoked := func(context.Context, *sql.Tx, int64) (string, error) { return "", ErrLibraryUnreadable }
	up, err := s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return revoked })
	if err != nil || up.LibraryAvailable || up.UpdateAvailable || up.PublishedVersion != 0 || len(up.Changes.Changed) != 0 {
		t.Fatalf("revoked check leaks %+v %v", up, err)
	}
	if _, _, err = s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{2, "page", 1}, a, revoked); !errors.Is(err, ErrLibraryUnreadable) {
		t.Fatalf("revoked update %v", err)
	}
	got, err := s.ReadLibraryPin(ctx, consumer, a)
	if err != nil || got.LibraryVersion != 1 || !strings.Contains(string(got.Content), "#111") {
		t.Fatalf("revoked pinner lost the copy %+v %v", got, err)
	}
	// A different actor for the library than for the consumer is refused.
	other := func(context.Context, *sql.Tx, int64) (string, error) { return "someone-else", nil }
	if _, _, err = s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{2, "page", 1}, a, other); !errors.Is(err, ErrVersionAuthority) {
		t.Fatalf("mixed actors %v", err)
	}
}

func TestLibraryPublishValidation(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	cases := []struct {
		name   string
		files  VersionRequest
		target int64
	}{
		{"no tokens file", versionRequest(0, "plain"), 1},
		{"not an object", libraryRequest(1, `[1,2]`), 2},
		{"not json", libraryRequest(2, `{`), 3},
		{"too large", libraryRequest(3, `{"x":"`+strings.Repeat("a", MaxLibraryBytes)+`"}`), 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Apply(ctx, lib, c.files, a); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishLibrary(ctx, lib, c.target, a); !errors.Is(err, ErrVersionInvalid) {
				t.Fatalf("publish %v", err)
			}
		})
	}
	if _, err := s.PublishLibrary(ctx, lib, 99, a); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("missing version %v", err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, consumer.PageID, LibraryPinExpect{Version: 1}, a, read); !errors.Is(err, ErrVersionInvalid) {
		t.Fatalf("self pin %v", err)
	}
	if _, err := s.ReadLibraryPin(ctx, consumer, a); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("no pin %v", err)
	}
}

func TestDiffLibraryTokens(t *testing.T) {
	cases := []struct {
		name, from, to          string
		added, removed, changed string
	}{
		{"identical", `{"a":1}`, `{"a":1}`, "", "", ""},
		{"nested", `{"t":{"light":{"bg":"#fff"}}}`, `{"t":{"light":{"bg":"#000"},"dark":{"bg":"#000"}}}`, "t.dark.bg", "", "t.light.bg"},
		{"leaf to object", `{"a":1}`, `{"a":{"b":1}}`, "a.b", "a", ""},
		{"literal dots and nested paths", `{"a.b":1,"a":{"b":2}}`, `{"a.b":3,"a":{"b":4}}`, "", "", `"a.b",a.b`},
		{"empty and spaced keys", `{"a":1," a ":2,"":{"a":3}}`, `{"a":1," a ":4,"":{"a":5}}`, "", "", `" a ","".a`},
		{"arrays are leaves", `{"a":[1]}`, `{"a":[1,2]}`, "", "", "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := diffLibraryTokens([]byte(c.from), []byte(c.to))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(d.Added, ",") != c.added || strings.Join(d.Removed, ",") != c.removed || strings.Join(d.Changed, ",") != c.changed {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestLibraryPinAppliesOnlyTheReviewedState(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	db := s.db
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('other-lib','ws','other-lib','Other library','owner','{}')`); err != nil {
		t.Fatal(err)
	}
	other := VersionKey{"ws", "other-lib"}
	for _, k := range []VersionKey{lib, other} {
		if _, err := s.Apply(ctx, k, libraryRequest(0, `{"colors":{"primary":"#100"}}`), a); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(ctx, k, libraryRequest(1, `{"colors":{"primary":"#200"}}`), a); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(ctx, k, libraryRequest(2, `{"colors":{"primary":"#300"}}`), a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PublishLibrary(ctx, lib, 2, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, other, 2, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 2}, a, read); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		library string
		expect  LibraryPinExpect
	}{
		// The consumer reviewed v2, then the owner published v3.
		{"publication moved after review", lib.PageID, LibraryPinExpect{2, lib.PageID, 2}},
		// The caller reviewed a switch from "other-lib" v2, but "page" v2 is pinned.
		{"switch with equal version numbers from the wrong library", other.PageID, LibraryPinExpect{2, other.PageID, 2}},
		{"pinned library without version", other.PageID, LibraryPinExpect{Version: 2, PinnedLibrary: lib.PageID}},
	}
	if _, err := s.PublishLibrary(ctx, lib, 3, a); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := s.PinLibrary(ctx, consumer, c.library, c.expect, a, read)
			if !errors.Is(err, ErrVersionConflict) && !errors.Is(err, ErrVersionInvalid) {
				t.Fatalf("applied an unreviewed state: %v", err)
			}
			got, _ := s.ReadLibraryPin(ctx, consumer, a)
			if got.LibraryPageID != lib.PageID || got.LibraryVersion != 2 {
				t.Fatalf("pin changed %+v", got)
			}
		})
	}
	// The reviewed switch, naming the library actually pinned, applies.
	got, changed, err := s.PinLibrary(ctx, consumer, other.PageID, LibraryPinExpect{2, lib.PageID, 2}, a, read)
	if err != nil || !changed || got.LibraryPageID != other.PageID || got.LibraryVersion != 2 {
		t.Fatalf("reviewed switch %+v %v %v", got, changed, err)
	}
}

func TestLibraryPinReadRejectsACorruptCopy(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, lib, libraryRequest(0, `{"colors":{"primary":"#111"}}`), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE artifact_library_pins SET content=CAST('{"colors":{}}' AS BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLibraryPin(ctx, consumer, a); !errors.Is(err, ErrLibraryPinCorrupt) {
		t.Fatalf("corrupt copy served: %v", err)
	}
}
