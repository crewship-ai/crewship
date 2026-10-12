package pages

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
)

func TestProjectStoragePressureAdmissionMargins(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		sources               int
		sourceBytes, gitBytes int64
		want                  bool
	}{
		{name: "empty"},
		{name: "source count below", sources: 223},
		{name: "source count at limit", sources: 224, want: true},
		{name: "source bytes below", sources: 1, sourceBytes: (96 << 20) - 1},
		{name: "source bytes at limit", sources: 1, sourceBytes: 96 << 20, want: true},
		{name: "git bytes below", gitBytes: (96 << 20) - 1},
		{name: "git bytes at limit", gitBytes: 96 << 20, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &ProjectStore{Directory: t.TempDir()}
			root, err := store.root("ws", true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			for i := 0; i < tc.sources; i++ {
				f, err := root.Create(fmt.Sprintf("source-%d.yaml", i))
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					err = f.Truncate(tc.sourceBytes)
				}
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("source fixture: %v %v", err, closeErr)
				}
			}
			if err := root.Mkdir("git", 0700); err != nil {
				t.Fatal(err)
			}
			f, err := root.Create("git/object")
			if err != nil {
				t.Fatal(err)
			}
			err = f.Truncate(tc.gitBytes)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("git fixture: %v %v", err, closeErr)
			}
			// Scratch files outside Git are not retained source snapshots.
			f, err = root.Create("scratch.tmp")
			if err != nil {
				t.Fatal(err)
			}
			err = f.Truncate(100 << 20)
			closeErr = f.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("scratch fixture: %v %v", err, closeErr)
			}
			got, err := store.StoragePressure(t.Context(), "ws")
			if err != nil || got != tc.want {
				t.Fatalf("pressure %v want %v: %v", got, tc.want, err)
			}
		})
	}
}

func TestProjectStoragePressureRejectsUnsafeStorageAndCancellation(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	root, err := store.root("ws", true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Symlink(t.TempDir(), "outside"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoragePressure(t.Context(), "ws"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("symlink accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.StoragePressure(ctx, "ws"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err = root.Remove("outside"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoragePressure(t.Context(), ""); err == nil {
		t.Fatal("accepted empty workspace")
	}
	broken := &ProjectStore{Directory: t.TempDir() + "/file"}
	if err = os.WriteFile(broken.Directory, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.StoragePressure(t.Context(), "ws"); err == nil {
		t.Fatal("accepted non-directory storage root")
	}
}
