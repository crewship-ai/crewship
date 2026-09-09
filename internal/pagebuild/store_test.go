package pagebuild

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactStoreIsolationIntegrityAndBounds(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Directory: dir}
	ctx := context.Background()
	a := &Artifact{Format: ArtifactFormat, JavaScript: "console.log('Český dashboard')", CSS: "body{}", Toolchain: "test"}
	digest, err := s.Put(ctx, "a", a)
	if err != nil {
		t.Fatal(err)
	}
	reopened := &Store{Directory: dir}
	got, err := reopened.Get(ctx, "a", digest)
	if err != nil || got.JavaScript != a.JavaScript {
		t.Fatalf("reopen %v", err)
	}
	if _, err := s.Get(ctx, "b", digest); err == nil {
		t.Fatal("cross-workspace artifact read")
	}
	path := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte("a"))), digest+".json")
	if err := os.WriteFile(path, []byte(`{"format":"crewship-page-preview/v1","javascript":"changed","css":"","toolchain":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "a", digest); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
	a.JavaScript = strings.Repeat("x", MaxArtifactBytes+1)
	if _, _, err := a.Encode(); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestArtifactStoreQuotaAdmission(t *testing.T) {
	for _, kind := range []string{"entries", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			s := &Store{Directory: t.TempDir()}
			root, err := s.root("ws", true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			count, size := 256, int64(1)
			if kind == "bytes" {
				count, size = 1, 128<<20
			}
			for i := 0; i < count; i++ {
				f, err := root.OpenFile(fmt.Sprintf("fixture-%d", i), os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(size)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.Put(context.Background(), "ws", &Artifact{Format: ArtifactFormat, JavaScript: "export {};", Toolchain: "test"})
			if !errors.Is(err, ErrStoreFull) {
				t.Fatalf("quota admission: %v", err)
			}
		})
	}
}
