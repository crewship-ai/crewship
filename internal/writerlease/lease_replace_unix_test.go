//go:build linux || darwin

package writerlease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseDetectsDatabaseReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owner, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(owner.Verify(), ErrLost) {
		t.Fatal("replacement accepted as original owner")
	}
}
