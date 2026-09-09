package pagebuild

import (
	"context"
	"crypto/sha256"
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
