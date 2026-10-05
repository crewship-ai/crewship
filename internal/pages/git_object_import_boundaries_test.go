package pages

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProjectObjectImportRejectsUnverifiedBytes(t *testing.T) {
	// Git's independently known empty blob ID exercises the actual wire checksum.
	const emptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := t.Context()
	for _, tc := range []struct {
		name, id, kind string
		data           []byte
	}{
		{"invalid ID", "../escape", "blob", nil},
		{"unsupported kind", emptyBlob, "tag", nil},
		{"checksum mismatch", emptyBlob, "blob", []byte("tampered")},
		{"oversize", emptyBlob, "blob", make([]byte, MaxTransferBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.ImportGitObject(ctx, "ws", "page", tc.id, tc.kind, tc.data); err == nil {
				t.Fatal("accepted invalid Git object")
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.ImportGitObject(canceled, "ws", "page", emptyBlob, "blob", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := (&ProjectStore{}).ImportGitObject(ctx, "ws", "page", emptyBlob, "blob", nil); err == nil {
		t.Fatal("accepted unconfigured storage")
	}
	if err := store.ImportGitObject(ctx, "ws", "page", emptyBlob, "blob", nil); err != nil {
		t.Fatal(err)
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	kind, err := runProjectGit(ctx, repo, nil, "cat-file", "-t", emptyBlob)
	if err != nil || strings.TrimSpace(string(kind)) != "blob" {
		t.Fatalf("imported object unreadable: %s %v", kind, err)
	}
	data, err := runProjectGit(ctx, repo, nil, "cat-file", "blob", emptyBlob)
	if err != nil || len(data) != 0 {
		t.Fatalf("object content changed: %q %v", data, err)
	}
}
