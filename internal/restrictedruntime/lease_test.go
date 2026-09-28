//go:build linux

package restrictedruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseGuardFailsClosedWithoutRenewal(t *testing.T) {
	for _, name := range []string{"never admitted", "issued once", "malformed", "removed", "too far ahead"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lease.json")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			switch name {
			case "issued once", "removed":
				if err := writeLease(path, time.Now().Add(150*time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(path, []byte("untrusted nonsense"), 0600); err != nil {
					t.Fatal(err)
				}
			case "too far ahead":
				b, _ := time.Now().Add(time.Hour).MarshalJSON()
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "removed" {
				time.AfterFunc(50*time.Millisecond, func() { _ = os.Remove(path) })
			}
			err := monitorLease(ctx, path, 100*time.Millisecond, time.Millisecond)
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("guard failed to expire independently: %v", err)
			}
		})
	}
}

func TestLeaseDeliveryRejectsUnboundedAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease.json")
	for _, expires := range []time.Time{{}, time.Now().Add(-time.Second), time.Now().Add(time.Minute)} {
		if err := writeLease(path, expires); !errors.Is(err, ErrDenied) {
			t.Fatalf("bad deadline accepted: %v", err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected deadline materialized: %v", err)
	}
}
