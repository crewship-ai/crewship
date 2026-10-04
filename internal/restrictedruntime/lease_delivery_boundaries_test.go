//go:build linux

package restrictedruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLeaseDeliveryStorageRefusalLeavesNoTemporaryAuthority(t *testing.T) {
	for _, stage := range []string{"missing parent", "directory target"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "lease.json")
			if stage == "missing parent" {
				target = filepath.Join(root, "missing", "lease.json")
			} else if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeLease(target, time.Now().Add(time.Second)); err == nil {
				t.Fatal("unwritable lease accepted")
			}
			leftovers, err := filepath.Glob(filepath.Join(root, ".lease-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("temporary authority left behind: %v, %v", leftovers, err)
			}
			if stage == "directory target" {
				info, err := os.Stat(target)
				if err != nil || !info.IsDir() {
					t.Fatalf("original target changed: %v, %v", info, err)
				}
			}
		})
	}
}

func TestTrustedLeaseEntrypointsRefuseCancelledAndExpiredAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RunLeaseGuard(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled trusted guard = %v", err)
	}
	// Invalid deadlines are refused before accessing the fixed /broker path.
	if err := RenewLease(time.Now().Add(-time.Second)); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired renewal = %v", err)
	}
}

func TestNativeManagerRejectsUnapprovedLimitsAndImageIdentity(t *testing.T) {
	limits := NativeLimits()
	if limits.MemoryBytes != 1<<30 || limits.NanoCPUs != 2e9 || limits.PIDs != 256 {
		t.Fatalf("native containment limits changed: %+v", limits)
	}
	for _, tc := range []struct {
		name, image string
		limits      Limits
	}{
		{"missing limits", "sha256:" + strings.Repeat("a", 64), Limits{}},
		{"missing image", "", limits},
		{"mutable image tag", "alpine:latest", limits},
		{"bad digest prefix", "image::" + strings.Repeat("a", 64), limits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewNative(filepath.Join(t.TempDir(), "state"), Docker{Image: tc.image}, nil, nil, tc.limits)
			if !errors.Is(err, ErrDenied) || m != nil {
				t.Fatalf("invalid native configuration admitted: %v", err)
			}
		})
	}
}
