package pipeline

import (
	"errors"
	"testing"
)

func TestSaveRefusesPublishedCallTarget(t *testing.T) {
	db := openStoreTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	target, err := store.Save(t.Context(), validSaveInput("target"))
	if err != nil {
		t.Fatal(err)
	}
	in := validSaveInput("caller")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"caller","steps":[{"id":"call","type":"call_pipeline","pipeline_slug":"target"}]}`
	if _, err := store.Save(t.Context(), in); err != nil {
		t.Fatal("legacy target", err)
	}
	guardPublishedVersion(t, store, target)
	if _, err := db.Exec(`UPDATE pipelines SET head_version=2 WHERE id=?`, target.ID); err != nil {
		t.Fatal(err)
	}
	in.Name = "refused"
	for _, slug := range []string{"caller", "new-caller"} {
		in.Slug = slug
		if _, err := store.Save(t.Context(), in); !errors.Is(err, ErrInvalidTrigger) {
			t.Fatalf("%s accepted publication call: %v", slug, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE slug='new-caller' OR (slug='caller' AND name='refused')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected save mutated state: %d %v", n, err)
	}
}
