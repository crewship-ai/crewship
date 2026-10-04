package resourcelifecycle

import (
	"context"
	"testing"
	"time"
)

func TestCacheEvictionPreservesSelectedArtifact(t *testing.T) {
	for _, selector := range []string{"selected", "crewship-cache:selected"} {
		t.Run(selector, func(t *testing.T) {
			r, f, now := retentionFixture(t)
			if _, err := r.DB.Exec(`UPDATE crews SET cached_image=? WHERE id='live'`, selector); err != nil {
				t.Fatal(err)
			}
			f.images = []CacheImage{cacheImage("selected", 48*time.Hour), cacheImage("orphan", 48*time.Hour)}
			r.Tick(context.Background())
			*now = now.Add(2 * time.Hour)
			r.Tick(context.Background())
			if len(f.untagged) != 1 || f.untagged[0] != "orphan" {
				t.Fatalf("removed %v; want only orphan identity", f.untagged)
			}
		})
	}
}

func TestCacheEvictionSelectedOwnerDeleted(t *testing.T) {
	r, f, now := retentionFixture(t)
	if _, err := r.DB.Exec(`UPDATE crews SET cached_image='old' WHERE id='deleted'`); err != nil {
		t.Fatal(err)
	}
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background())
	*now = now.Add(2 * time.Hour)
	r.Tick(context.Background())
	if len(f.untagged) != 1 || f.untagged[0] != "old" {
		t.Fatalf("deleted owner still retained image: %v", f.untagged)
	}
}

func TestCacheEvictionUnknownReferencesKeepArtifact(t *testing.T) {
	r, f, now := retentionFixture(t)
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background())
	*now = now.Add(2 * time.Hour)
	if _, err := r.DB.Exec(`ALTER TABLE crews RENAME COLUMN cached_image TO unavailable`); err != nil {
		t.Fatal(err)
	}
	r.Tick(context.Background())
	if len(f.untagged) != 0 {
		t.Fatalf("removed with unreadable references: %v", f.untagged)
	}
}

func TestCacheEvictionRechecksSelectionBeforeRemoval(t *testing.T) {
	r, f, now := retentionFixture(t)
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background())
	*now = now.Add(2 * time.Hour)
	// The candidate was previously unreferenced. Selection changes after that
	// snapshot; the final removal path must independently check current intent.
	if _, err := r.DB.Exec(`UPDATE crews SET cached_image='old' WHERE id='live'`); err != nil {
		t.Fatal(err)
	}
	r.evict(context.Background(), f.images[0])
	if len(f.untagged) != 0 {
		t.Fatalf("removed newly selected image: %v", f.untagged)
	}
}
