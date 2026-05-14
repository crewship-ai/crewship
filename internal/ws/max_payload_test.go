package ws

import (
	"testing"
)

// TestWsMaxInboundFrameBytes_Constant pins the documented cap. Regression
// guard: if a future refactor raises this without a security review, the
// CR will trip on this test asking why.
func TestWsMaxInboundFrameBytes_Constant(t *testing.T) {
	const want = 64 * 1024
	if wsMaxInboundFrameBytes != want {
		t.Fatalf("wsMaxInboundFrameBytes = %d, want %d (raise this only with security review — see D5 in the 2026-05-14 pentest)", wsMaxInboundFrameBytes, want)
	}
}
