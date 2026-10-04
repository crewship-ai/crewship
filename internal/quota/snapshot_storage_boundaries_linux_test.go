//go:build linux

package quota

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotImportPublishesOnlyAfterCompleteVerifiedStream(t *testing.T) {
	u := newUnitBackend(t)
	key := Key{"crew", "database", "data", 1}
	meta, image, _ := u.paths(key)
	// The unit fixture records physical tools; this test checks the durable
	// catalog boundary, not whether the bytes form a real ext4 filesystem.
	u.attach = func(_ context.Context, d Descriptor) error {
		if _, err := os.Stat(meta); !os.IsNotExist(err) {
			t.Fatalf("metadata published before attachment validation: %v", err)
		}
		info, err := os.Stat(image)
		if err != nil || info.Size() != MinBytes {
			t.Fatalf("incomplete image: %v, %v", info, err)
		}
		if d.Key != key || d.Bytes != MinBytes {
			t.Fatalf("wrong descriptor: %+v", d)
		}
		return nil
	}
	got, err := u.Import(t.Context(), key, MinBytes, io.LimitReader(snapshotUnitZeros{}, MinBytes))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := u.read(key)
	if err != nil || persisted != got {
		t.Fatalf("published catalog = %+v, %v; want %+v", persisted, err, got)
	}
	if len(u.ran("e2fsck")) != 1 {
		t.Fatal("image published without offline verification")
	}
	if u.activeSnapshots[key.id()] {
		t.Fatal("completed import retained exclusive ownership")
	}
	if _, err := u.Import(t.Context(), key, MinBytes, io.LimitReader(snapshotUnitZeros{}, MinBytes)); !errors.Is(err, ErrDenied) {
		t.Fatalf("existing generation overwritten: %v", err)
	}
}

func TestSnapshotImportRefusalsRemoveUnpublishedImageAndReleaseOwnership(t *testing.T) {
	refusal := errors.New("storage verification refused")
	for _, stage := range []string{"short stream", "cancelled stream", "filesystem check", "attach"} {
		t.Run(stage, func(t *testing.T) {
			u := newUnitBackend(t)
			key := Key{"crew", "database", "data", 1}
			var src io.Reader = io.LimitReader(snapshotUnitZeros{}, MinBytes)
			ctx := t.Context()
			switch stage {
			case "short stream":
				src = io.LimitReader(snapshotUnitZeros{}, 7)
			case "cancelled stream":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "filesystem check":
				u.run = func(context.Context, string, ...string) ([]byte, error) { return nil, refusal }
			case "attach":
				u.attach = func(context.Context, Descriptor) error { return refusal }
			}
			_, err := u.Import(ctx, key, MinBytes, src)
			if err == nil {
				t.Fatal("invalid import accepted")
			}
			if stage == "cancelled stream" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation hidden: %v", err)
			}
			if stage == "attach" && !errors.Is(err, refusal) {
				t.Fatalf("attachment refusal hidden: %v", err)
			}
			meta, image, _ := u.paths(key)
			for _, p := range []string{meta, image} {
				if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
					t.Errorf("failed import left %s: %v", filepath.Base(p), statErr)
				}
			}
			if u.activeSnapshots[key.id()] {
				t.Fatal("failed import retained exclusive ownership")
			}
		})
	}
}

func TestSnapshotImportRefusesCatalogAndReservationObstacles(t *testing.T) {
	for _, stage := range []string{"image exists", "reservation parent is file", "missing images directory"} {
		t.Run(stage, func(t *testing.T) {
			u := newUnitBackend(t)
			key := Key{"crew", "database", "data", 1}
			_, image, _ := u.paths(key)
			switch stage {
			case "image exists":
				if err := os.WriteFile(image, []byte("existing image"), 0600); err != nil {
					t.Fatal(err)
				}
			case "reservation parent is file":
				p := filepath.Join(u.root, "blocked")
				if err := os.WriteFile(p, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				u.reservePath = filepath.Join(p, "lock")
			case "missing images directory":
				if err := os.Remove(filepath.Join(u.root, "images")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := u.Import(t.Context(), key, MinBytes, io.LimitReader(snapshotUnitZeros{}, MinBytes)); err == nil {
				t.Fatal("storage obstacle ignored")
			}
			if stage == "image exists" {
				raw, err := os.ReadFile(image)
				if err != nil || string(raw) != "existing image" {
					t.Fatalf("existing bytes changed: %q, %v", raw, err)
				}
			}
		})
	}
}

func TestSnapshotReaderPropagatesCancellationBeforeTouchingSource(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := snapshotReader{ctx: ctx, reader: snapshotUnreadableReader{t}}
	if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %d, %v", n, err)
	}
}

type snapshotUnreadableReader struct{ t *testing.T }

func (r snapshotUnreadableReader) Read([]byte) (int, error) {
	r.t.Fatal("cancelled reader touched source")
	return 0, io.EOF
}
