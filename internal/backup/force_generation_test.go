package backup_test

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForceGenerationWrongIdentity(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	target := filepath.Join(t.TempDir(), "data")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target}
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	paths := []string{"crewship.db", "secrets.env"}
	originals := map[string]os.FileInfo{}
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(target, p))
		if err != nil {
			t.Fatal(err)
		}
		originals[p] = info
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	opts.Force = true
	opts.Identities = []age.Identity{other}
	if _, err = backup.RecoverInstance(t.Context(), opts); err == nil {
		t.Fatal("accepted the wrong recovery identity")
	}
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(target, p))
		if err != nil || !os.SameFile(originals[p], info) {
			t.Errorf("original %s changed after rejected recovery: %v", p, err)
		}
	}
}
