package resourcelifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An empty owner is the publication window of an in-place write. It must
// never be interpreted as a different database and fork a second live nonce.
func TestInstallationIdentityPartialOwnerCannotForkLiveHolder(t *testing.T) {
	root, db := t.TempDir(), migratedDB(t)
	held, err := LoadIdentity(t.Context(), root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	nonce := nonceOf(t, db)
	if err := os.WriteFile(filepath.Join(root, "installations", nonce+".db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	other, err := LoadIdentity(context.Background(), root, db.DB, db.location)
	if other != nil {
		defer other.Close()
	}
	if err == nil || other != nil {
		t.Fatalf("partial owner forked a live identity: %+v %v", other, err)
	}
	if got := nonceOf(t, db); got != nonce {
		t.Fatal("malformed owner changed the database nonce")
	}
}
