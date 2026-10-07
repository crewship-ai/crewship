package resourcelifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireExistingIdentityNeverCreatesOrRekeys(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := migratedDB(t)
	if _, err := AcquireExistingIdentity(ctx, root, db.DB, db.location); err == nil {
		t.Fatal("accepted missing identity")
	}
	if _, err := os.Stat(filepath.Join(root, "installations")); !os.IsNotExist(err) {
		t.Fatal("created identity state")
	}
	live, err := LoadIdentity(ctx, root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	nonce := nonceOf(t, db)
	id := live.ID
	if _, err := AcquireExistingIdentity(ctx, root, db.DB, db.location); !errors.Is(err, ErrIdentityInUse) {
		t.Fatalf("live identity accepted: %v", err)
	}
	live.Close()
	existing, err := AcquireExistingIdentity(ctx, root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	existing.Close()
	if existing.ID != id || nonceOf(t, db) != nonce {
		t.Fatal("changed existing identity")
	}
	if _, err := AcquireExistingIdentity(ctx, root, db.DB, db.location+"-copy"); err == nil {
		t.Fatal("copied database gained authority")
	}
	if nonceOf(t, db) != nonce {
		t.Fatal("rekeyed nonce")
	}
}
