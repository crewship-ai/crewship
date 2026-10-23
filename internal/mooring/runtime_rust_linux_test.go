//go:build linux

package mooring

import (
	"context"
	"os"
	"testing"
	"time"
)

// This opt-in acceptance uses the actual built Rust child. The default Go
// verification loop must remain usable without a Rust compiler or binary.
func TestRuntimeActualRustChild(t *testing.T) {
	binary := os.Getenv("MOORING_TEST_RUST_BINARY")
	if binary == "" {
		t.Skip("set MOORING_TEST_RUST_BINARY to the built mooring-collab")
	}
	r, err := StartRuntime(t.Context(), RuntimeConfig{Enabled: true, Binary: binary, Versions: runtimeTestVersions})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	// This child slice has no content dispatcher. A post-bootstrap message
	// must terminate it; it cannot manufacture an authoritative receipt.
	if err = r.RPC.Write([]byte(`{"type":"unimplemented_content"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Rust child accepted unsupported post-bootstrap traffic")
	}
	if _, err = r.RPC.Read(); err == nil {
		t.Fatal("unsupported traffic yielded a content frame")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	second, err := StartRuntime(ctx, RuntimeConfig{Enabled: true, Binary: binary, Versions: runtimeTestVersions})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	cancel()
	select {
	case <-second.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Rust child survived host cancellation")
	}
}
