package resourcelifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// os.WriteFile exposes the destination before its contents are written. Hold
// the writer's nonce lock while representing that exact reader-visible state:
// it must never be mistaken for another legitimate database's ownership.
func TestInstallationIdentityPartialOwnerDoesNotRekeyLiveDatabase(t *testing.T) {
	for _, owner := range []string{"abc", strings.Repeat("x", 64)} {
		t.Run("owner_"+owner, func(t *testing.T) {
			root, db := t.TempDir(), migratedDB(t)
			first, err := LoadIdentity(context.Background(), root, db.DB, db.location)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { first.Close() })
			nonce := nonceOf(t, db)
			if err := os.WriteFile(filepath.Join(root, "installations", nonce+".db"), []byte(owner), 0o600); err != nil {
				t.Fatal(err)
			}
			second, err := LoadIdentity(context.Background(), root, db.DB, db.location)
			if second != nil {
				second.Close()
				t.Errorf("partial owner granted a second identity %q while %q is still held", second.ID, first.ID)
			}
			if err == nil {
				t.Error("partial owner must fail closed")
			}
			if after := nonceOf(t, db); after != nonce {
				t.Errorf("partial owner changed live database nonce: %s -> %s", nonce, after)
			}
		})
	}
}

func TestClaimLocationPublishesCompletePrivateOwnerWithoutReplacing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owner.db")
	const contenders = 16
	type result struct {
		owner string
		err   error
	}
	results := make(chan result, contenders)
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		owner := fmt.Sprintf("%064x", i+1)
		go func() {
			<-start
			results <- result{owner, claimLocation(path, owner)}
		}()
	}
	close(start)
	var winner string
	for i := 0; i < contenders; i++ {
		r := <-results
		if r.err == nil {
			if winner != "" {
				t.Errorf("two published owners: %s and %s", winner, r.owner)
			}
			winner = r.owner
		} else if !errors.Is(r.err, errLocationClaimed) {
			t.Errorf("unexpected publication error: %v", r.err)
		}
	}
	if winner == "" {
		t.Fatal("no owner published")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != winner+"\n" {
		t.Fatalf("owner file = %q, error %v", contents, err)
	}
	if err := claimLocation(path, winner); err != nil {
		t.Fatalf("same owner rejected: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("owner mode = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "owner.db" {
		t.Fatalf("publication leaked temporary files: %v, error %v", entries, err)
	}
}

func TestClaimLocationDoesNotReplaceInvalidOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.db")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claimLocation(path, strings.Repeat("a", 64)); err == nil || errors.Is(err, errLocationClaimed) {
		t.Fatalf("invalid owner must fail closed, not authorize rekey: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "partial" {
		t.Fatalf("existing invalid owner was overwritten: %q, %v", data, err)
	}
}
