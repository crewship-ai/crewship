//go:build linux

package quota

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServeNamespaceRejectsMissingBackendWithoutPanic(t *testing.T) {
	if err := ServeNamespace(t.Context(), filepath.Join(t.TempDir(), "helper.sock"), 1000, nil, "installation-a", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing backend admitted: %v", err)
	}
}

func TestServePreflightCannotBindNamespaceForForbiddenPeer(t *testing.T) {
	for _, uid := range []uint32{1001, 1002} {
		u := newUnitBackend(t)
		ready := false
		socket := filepath.Join(t.TempDir(), "helper.sock")
		if err := ServeNamespace(t.Context(), socket, uid, u.Backend, "installation-a", func() error { ready = true; return nil }); !errors.Is(err, ErrDenied) {
			t.Fatalf("agent/broker configured as host: %v", err)
		}
		if ready {
			t.Fatal("forbidden peer published readiness")
		}
		for _, path := range []string{socket, filepath.Join(u.root, "namespace")} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("denied server changed authority at %s: %v", path, err)
			}
		}
		if err := Serve(t.Context(), socket, uid, u.Backend); !errors.Is(err, ErrDenied) {
			t.Fatalf("unscoped server admitted forbidden peer: %v", err)
		}
		if err := ServeWithReady(t.Context(), socket, uid, u.Backend, nil); !errors.Is(err, ErrDenied) {
			t.Fatalf("readiness wrapper admitted forbidden peer: %v", err)
		}
	}
}
