//go:build linux && quota_live

package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLiveRecoveryPreservesPublishedDataAfterCapacityReduction(t *testing.T) {
	root := liveAdmissionRoot(t)
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"recovery-capacity", "database", "data", 1}
	d, err := b.Ensure(t.Context(), key, 64<<20, Owner{})
	if err != nil {
		b.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Remove(context.Background(), key); err != nil {
			t.Error(err)
		}
		b.Close()
	})
	canary := filepath.Join(d.Mount, "canary")
	if err := os.WriteFile(canary, []byte("preserve published data"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after metadata publication but before the allocation marker was removed.
	record := filepath.Join(root, "images", d.ID+".allocating")
	if err := os.WriteFile(record, []byte("finished allocation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewBackend(root, MinBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	b = restarted
	report, err := b.RecoverReport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !report.OverCapacity || !slices.Contains(report.Mounted, d.ID) || len(report.Cleaned) != 0 || len(report.Quarantined) != 0 || !strings.Contains(report.Summary(), "exceed the configured capacity") {
		t.Fatalf("recovery lost published volume: %+v", report)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("completed allocation marker retained: %v", err)
	}
	if got, err := os.ReadFile(canary); err != nil || string(got) != "preserve published data" {
		t.Fatalf("published data changed: %q %v", got, err)
	}
	if _, err := b.Ensure(t.Context(), Key{"recovery-capacity", "database", "new", 1}, MinBytes, Owner{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("over-capacity allocation admitted: %v", err)
	}
}

func TestLiveEnsureQuarantinesUnpublishedImageBeforeAllocating(t *testing.T) {
	root := liveAdmissionRoot(t)
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"recovery-orphan", "database", "data", 1}
	image := filepath.Join(root, "images", key.id()+".ext4")
	const original = "unpublished image bytes"
	if err := os.WriteFile(image, []byte(original), 0600); err != nil {
		b.Close()
		t.Fatal(err)
	}
	d, err := b.Ensure(t.Context(), key, MinBytes, Owner{})
	if err != nil {
		b.Close()
		t.Fatal(err)
	}
	defer func() {
		if err := b.Remove(context.Background(), key); err != nil {
			t.Error(err)
		}
		b.Close()
	}()
	if _, err := os.Stat(d.Mount); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), key.id()+".ext4") {
			got, err := os.ReadFile(filepath.Join(root, "quarantine", entry.Name()))
			if err != nil || string(got) != original {
				t.Fatalf("quarantine changed bytes: %q %v", got, err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("unpublished image was not preserved in quarantine")
	}
}
