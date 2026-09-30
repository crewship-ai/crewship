//go:build linux

package quota

import (
	"errors"
	"os"
	"testing"
)

func TestQuotaBackendRequiresPrivilegedOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		// SKIP-WAIVER(#2703): root cannot exercise rejection of an unprivileged backend owner; privileged tests require quota_live.
		t.Skip("unprivileged admission assertion")
	}
	if _, err := NewBackend(t.TempDir(), 64<<20, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprivileged filesystem backend: %v", err)
	}
}
