package pipeline

import (
	"context"
	"testing"
)

// Appearance — icon + colour — is presentation, and the whole reason it
// is a pair of COLUMNS rather than two more keys in definition_json is
// that definition_json is hashed into DefinitionHash, which
// pipeline_versions is keyed on and the HMAC save_token binds to.
// Recolouring a row must not mint a routine version.
//
// That is the invariant these cover: setting an icon changes the icon
// and nothing else, and above all does not move the definition hash.

func TestSetAppearance_PersistsAndReadsBack(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, err := s.Save(ctx, validSaveInput("appearance-demo"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Icon != "" || saved.Color != "" {
		t.Fatalf("a fresh routine should have no appearance set, got %q/%q", saved.Icon, saved.Color)
	}

	if err := s.SetAppearance(ctx, saved.WorkspaceID, saved.Slug, "receipt", "amber"); err != nil {
		t.Fatalf("set appearance: %v", err)
	}

	got, err := s.GetBySlug(ctx, saved.WorkspaceID, saved.Slug)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Icon != "receipt" || got.Color != "amber" {
		t.Fatalf("appearance did not persist: got %q/%q", got.Icon, got.Color)
	}
}

func TestSetAppearance_DoesNotTouchTheDefinitionHash(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, err := s.Save(ctx, validSaveInput("hash-stability"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	before := saved.DefinitionHash
	beforeDef := saved.DefinitionJSON

	if err := s.SetAppearance(ctx, saved.WorkspaceID, saved.Slug, "rocket", "violet"); err != nil {
		t.Fatalf("set appearance: %v", err)
	}

	got, err := s.GetBySlug(ctx, saved.WorkspaceID, saved.Slug)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The point of the whole design. If this ever fails, recolouring a
	// routine has started producing versions and invalidating save
	// tokens issued against the old hash.
	if got.DefinitionHash != before {
		t.Fatalf("definition hash moved on a recolour: %q -> %q", before, got.DefinitionHash)
	}
	if got.DefinitionJSON != beforeDef {
		t.Fatalf("definition body changed on a recolour")
	}
}

func TestSetAppearance_ClearsWithEmptyValues(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, _ := s.Save(ctx, validSaveInput("clearable"))
	if err := s.SetAppearance(ctx, saved.WorkspaceID, saved.Slug, "rocket", "violet"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Empty means "unset", not "store an empty string": the UI falls
	// back to deriving from the slug, so clearing has to be reachable.
	if err := s.SetAppearance(ctx, saved.WorkspaceID, saved.Slug, "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ := s.GetBySlug(ctx, saved.WorkspaceID, saved.Slug)
	if got.Icon != "" || got.Color != "" {
		t.Fatalf("appearance not cleared: %q/%q", got.Icon, got.Color)
	}
}

func TestSetAppearance_UnknownRoutine(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)

	err := s.SetAppearance(context.Background(), "ws_test", "no-such-routine", "rocket", "violet")
	if err == nil {
		t.Fatal("expected an error for a routine that does not exist")
	}
}

func TestSetAppearance_ScopedToWorkspace(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, _ := s.Save(ctx, validSaveInput("tenant-scoped"))
	// Same slug, another tenant: must not be reachable.
	if err := s.SetAppearance(ctx, "ws_other", saved.Slug, "rocket", "violet"); err == nil {
		t.Fatal("appearance was settable across workspaces")
	}
	got, _ := s.GetBySlug(ctx, saved.WorkspaceID, saved.Slug)
	if got.Icon != "" {
		t.Fatalf("another workspace wrote our appearance: %q", got.Icon)
	}
}
